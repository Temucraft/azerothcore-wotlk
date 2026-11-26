//go:build e2e

package rbac_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
)

// The modules' db-auth base files are re-run (the DB updater after a change, tools/rbac-repair.sh). They used to
// start with DELETE FROM rbac_linked_permissions / rbac_permissions for their whole range, and the ON DELETE CASCADE
// took every other role's links with it: re-running mod-temucraft-player-settings' file dropped the PTR role 1000
// links of the optional players file (07ab3b4). These files now update their permissions in place and add their own
// links with INSERT IGNORE. Database only: no worldserver needed.
//
// It writes to the auth database of E2E_AUTH_DSN, which on Temucraft is shared by Live and the PTR. It runs only on a
// database whose name has "e2e" in it (the isolated acore_auth_e2e). There is no way to run it on the shared one.

type rbacFile struct {
	path  string            // below the source root ("1) Source")
	perms []uint32          // permissions the file defines
	links map[uint32]uint32 // links the file itself makes: linkedId -> one role it links it to (checked)
	keep  map[uint32]uint32 // links of another role that must survive a re-run: linkedId -> role
}

var files = []rbacFile{
	{
		path:  "modules/mod-temucraft-player-settings/data/sql/db-auth/base/mod_temucraft_player_settings_rbac.sql",
		perms: []uint32{103850, 103851},
		links: map[uint32]uint32{103850: 1014, 103851: 1014},
		keep:  map[uint32]uint32{103850: 1000, 103851: 1000}, // data/sql/optional/mod_temucraft_player_settings_rbac_players.sql
	},
	{
		path:  "modules/mod-temucraft-courier/data/sql/db-auth/base/mod_temucraft_auras_rbac_player.sql",
		perms: []uint32{103650},
		links: map[uint32]uint32{103650: 1010},
		keep:  map[uint32]uint32{103650: 1000},
	},
	{
		path:  "modules/mod-temucraft-courier/data/sql/db-auth/base/mod_temucraft_courier_rbac_player.sql",
		perms: []uint32{102250, 102251},
		links: map[uint32]uint32{102250: 1014, 102251: 1010},
		keep:  map[uint32]uint32{102251: 1000},
	},
}

func sourceRoot() string {
	if dir := os.Getenv("E2E_SOURCE_DIR"); dir != "" {
		return dir
	}
	return filepath.Join("..", "..", "..", "..") // e2e/suites/temucraft/rbac -> the source root
}

// statements splits a module SQL file into statements (full-line "--" comments dropped; these files have no ';' in
// their strings).
func statements(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		e2eharness.Preconditionf(t, "read %s: %v (set E2E_SOURCE_DIR to the source root)", path, err)
	}
	var kept []string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	var out []string
	for _, s := range strings.Split(strings.Join(kept, "\n"), ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func run(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx) // one connection: SET @var must reach the statements after it
	if err != nil {
		e2eharness.HarnessFailf(t, "auth DB connection: %v", err)
	}
	defer conn.Close()
	for _, stmt := range statements(t, path) {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			e2eharness.Assertf(t, "%s: %v\n%s", filepath.Base(path), err, stmt)
		}
	}
}

func linked(t *testing.T, db *sql.DB, role, perm uint32) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM `rbac_linked_permissions` WHERE `id` = ? AND `linkedId` = ?", role, perm).Scan(&n); err != nil {
		e2eharness.HarnessFailf(t, "rbac_linked_permissions: %v", err)
	}
	return n > 0
}

func isolatedAuth(t *testing.T, db *sql.DB) {
	t.Helper()
	var name string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&name); err != nil {
		e2eharness.HarnessFailf(t, "auth DB name: %v", err)
	}
	if !strings.Contains(strings.ToLower(name), "e2e") {
		t.Skipf("auth database %q isn't an isolated e2e one (no \"e2e\" in the name): this test only runs on acore_auth_e2e", name)
	}
	var roles int
	if err := db.QueryRow("SELECT COUNT(*) FROM `rbac_permissions` WHERE `id` IN (1000, 1010, 1014)").Scan(&roles); err != nil || roles < 3 {
		e2eharness.Preconditionf(t, "the Temucraft roles 1000/1010/1014 aren't in %s (%d, %v): apply mod-temucraft's temucraft_rbac_roles.sql", name, roles, err)
	}
	t.Logf("auth database %s", name)
}

// Each file, run twice: its permissions are there, its own links are there, and a link of another role to one of its
// permissions (role 1000, the PTR player role) is still there.
func TestRBAC_RerunKeepsOtherRolesLinks(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "rbac", "db"}, Runtime: "short", Category: "temucraft/rbac"})

	db, err := e2eharness.OpenAuthDB()
	if err != nil {
		e2eharness.Preconditionf(t, "auth DB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	isolatedAuth(t, db)

	for _, f := range files {
		f := f
		t.Run(filepath.Base(f.path), func(t *testing.T) {
			path := filepath.Join(sourceRoot(), filepath.FromSlash(f.path))
			run(t, db, path) // the permissions exist before the links below can point at them
			for perm, role := range f.keep {
				res, err := db.Exec("INSERT IGNORE INTO `rbac_linked_permissions` (`id`, `linkedId`) VALUES (?, ?)", role, perm)
				if err != nil {
					e2eharness.HarnessFailf(t, "link %d -> %d: %v", role, perm, err)
				}
				if n, _ := res.RowsAffected(); n > 0 { // ours: take it away again at the end
					role, perm := role, perm
					t.Cleanup(func() {
						db.Exec("DELETE FROM `rbac_linked_permissions` WHERE `id` = ? AND `linkedId` = ?", role, perm)
					})
				}
			}
			for pass := 1; pass <= 2; pass++ {
				run(t, db, path)
				for _, perm := range f.perms {
					var name string
					if err := db.QueryRow("SELECT `name` FROM `rbac_permissions` WHERE `id` = ?", perm).Scan(&name); err != nil {
						e2eharness.Assertf(t, "run %d: permission %d is gone: %v", pass, perm, err)
					}
				}
				for perm, role := range f.links {
					if !linked(t, db, role, perm) {
						e2eharness.Assertf(t, "run %d: the file's own link %d -> %d is missing", pass, role, perm)
					}
				}
				for perm, role := range f.keep {
					if !linked(t, db, role, perm) {
						e2eharness.Assertf(t, "run %d: re-running %s dropped role %d's link to %d", pass, filepath.Base(f.path), role, perm)
					}
				}
			}
			t.Logf("PASS %s: re-run twice, own links there, role links kept %v", filepath.Base(f.path), f.keep)
		})
	}
}
