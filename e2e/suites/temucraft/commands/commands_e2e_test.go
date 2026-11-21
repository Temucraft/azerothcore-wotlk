//go:build e2e

package commands_test

import (
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// RBAC permission of `.temucraft hearthstone` (mod-temucraft-travel's block, db-auth SQL).
const permHearthstoneCommand uint32 = 103350

// Every Temucraft command lives under `.temucraft`, has its own RBAC permission, and is admin only by default
// (roles 192 and 1014). Harness accounts are GM level 3, so they hold the administrator role: each command must
// answer. Commands that change the character (self services, shop add) are left out; these only report.
func TestTemucraft_CommandsAnswer(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "commands"}, Runtime: "short", Category: "temucraft/commands"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcCmd", Level: 80})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	chat := tcwire.Collect(t, bot.World)

	cases := []struct{ cmd, want, module string }{
		{".temucraft hearthstone", "Hearthstone:", "mod-temucraft-travel"},
		{".temucraft challenge", "Challenges (", "mod-temucraft-challenges"},
		{".temucraft shop", "token", "mod-temucraft-shop"},
	}
	for _, c := range cases {
		got := tcwire.Command(t, bot.World, chat, c.cmd, c.want)
		t.Logf("%s (%s): %q", c.cmd, c.module, got)
	}

	// `.temucraft` alone lists its subcommands; hearthstone must be one of them.
	m := chat.Mark()
	bot.GM(t, ".temucraft")
	if _, ok := chat.WaitSystem(m, "hearthstone", tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Assertf(t, ".temucraft: hearthstone not in the subcommand list (got %q)", chat.SystemSince(m))
	}
	t.Logf("PASS every checked .temucraft command answered")
}

// A command whose permission is denied to the account doesn't run. The deny is written to rbac_account_permissions
// and loaded by a relog; the oracle is the database (no hearthstone setting is saved) and the missing reply. Another
// .temucraft command keeps working, so the deny is per command and not the whole tree.
func TestTemucraft_CommandDeniedByRBAC(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "commands", "rbac"}, Runtime: "short", Category: "temucraft/commands"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcRbac", Level: 80})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	if _, has := tcwire.CharSetting(t, bot.CharDB, bot.GUID, "hearthstone"); has {
		e2eharness.Preconditionf(t, "a fresh character already has a hearthstone setting")
	}

	tcwire.DenyPermission(t, bot.AuthDB, tcwire.AccountID(t, bot.AuthDB, bot.User), permHearthstoneCommand)
	bot.Relog(t)
	chat := tcwire.Collect(t, bot.World)

	m := chat.Mark()
	bot.GM(t, ".temucraft hearthstone 15")
	bot.FlushWorld(t) // every command sent before .gps has been handled
	if got, ok := chat.WaitSystem(m, "Hearthstone:", 2*time.Second); ok {
		e2eharness.Assertf(t, "denied .temucraft hearthstone still answered: %q", got)
	}
	bot.Save(t)
	if v, has := tcwire.CharSetting(t, bot.CharDB, bot.GUID, "hearthstone"); has {
		e2eharness.Assertf(t, "denied .temucraft hearthstone still saved the setting (%d)", v)
	}
	t.Logf("denied command replies: %q", strings.Join(chat.SystemSince(m), " | "))

	tcwire.Command(t, bot.World, chat, ".temucraft challenge", "Challenges (")
	t.Logf("PASS the denied command did nothing, the others still answer")
}
