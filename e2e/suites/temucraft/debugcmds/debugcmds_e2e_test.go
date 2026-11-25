//go:build e2e

package debugcmds_test

import (
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// RBAC_PERM_COMMAND_GPS. mod-temucraft eac1afc links it (and the other read-only debug commands) to role 1010, the
// role every security 0 account gets on Temucraft.
const permGps uint32 = 505

const (
	goXyzPrefix = ".go xyz "
	// The .go xyz line is queued as a 1 ms player event, so it comes a world tick or two after the .gps output.
	// Lines are counted for this long after the first one (or after the .gps output when there is none).
	goXyzWindow = 1500 * time.Millisecond
	// The .gps output's second line ("X: .. Y: .. Z: .. Orientation: ..").
	gpsLine = "Orientation:"
)

// Both mod-temucraft (Temucraft.GpsGoXyz.Enable) and mod-ryans-debug (RyansDebug.GpsGoXyz.Enable) can add the
// line; 24f25a7 keeps it to one. Without either module there's nothing to count.
func needGoXyzModule(t *testing.T) {
	t.Helper()
	worldDB, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer worldDB.Close()
	if !tcwire.ModuleStrings(t, worldDB, "mod-temucraft") && !tcwire.ModuleStrings(t, worldDB, "mod-ryans-debug") {
		t.Skip("neither mod-temucraft nor mod-ryans-debug is installed (no module_string rows)")
	}
}

func goXyzLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), goXyzPrefix) {
			out = append(out, l)
		}
	}
	return out
}

// gps sends .gps and returns every system line it produced: the .gps output and the .go xyz lines. The settle
// before the mark lets a .go xyz line from an earlier FlushWorld (it sends .gps too) land first.
func gps(t *testing.T, bot *e2eharness.ScenarioBot, chat *tcwire.Collector) (output bool, lines []string) {
	t.Helper()
	time.Sleep(goXyzWindow)
	m := chat.Mark()
	bot.GM(t, ".gps")
	_, output = chat.WaitSystem(m, gpsLine, tcwire.DefaultReplyTimeout)
	if _, ok := chat.WaitSystem(m, goXyzPrefix, goXyzWindow); ok {
		time.Sleep(goXyzWindow) // a second line would come right behind the first
	}
	return output, chat.SystemSince(m)
}

func assertOneGoXyz(t *testing.T, bot *e2eharness.ScenarioBot, who string) {
	t.Helper()
	chat := tcwire.Collect(t, bot.World)
	output, lines := gps(t, bot, chat)
	if !output {
		e2eharness.Assertf(t, "%s: .gps gave no position (got %q)", who, lines)
	}
	got := goXyzLines(lines)
	if len(got) != 1 {
		e2eharness.Assertf(t, "%s: .gps gave %d \".go xyz\" lines, want exactly 1: %q", who, len(got), got)
	}
	t.Logf("PASS %s: one line %q", who, got[0])
}

// A GM level 3 account (the harness default) gets exactly one ".go xyz" line after .gps, even with both modules
// that add it built in (mod-temucraft 24f25a7).
func TestGps_AdminGetsOneGoXyzLine(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "commands", "gps"}, Runtime: "short", Category: "temucraft/debugcmds"})
	needGoXyzModule(t)

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcGpsA", Level: 10})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	assertOneGoXyz(t, bot, "GM level 3")
}

// A player account (security 0) may use .gps now (role 1010 has permission 505, mod-temucraft eac1afc), and its
// .gps also gives exactly one ".go xyz" line.
func TestGps_PlayerGetsOneGoXyzLine(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "commands", "gps", "rbac"}, Runtime: "short", Category: "temucraft/debugcmds"})
	needGoXyzModule(t)

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcGpsP", Level: 10})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	tcwire.AsPlayer(t, bot)
	assertOneGoXyz(t, bot, "security 0")
}

// An account that is denied permission 505 (rbac_account_permissions, granted = 0) gets neither the .gps output nor
// a ".go xyz" line: both modules only add the line for accounts that may use .gps.
func TestGps_DeniedGetsNoGoXyzLine(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "commands", "gps", "rbac"}, Runtime: "short", Category: "temucraft/debugcmds"})
	needGoXyzModule(t)

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcGpsD", Level: 10})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	tcwire.DenyPermission(t, bot.AuthDB, tcwire.AccountID(t, bot.AuthDB, bot.User), permGps)
	tcwire.AsPlayer(t, bot) // relogs, which loads the deny

	chat := tcwire.Collect(t, bot.World)
	output, lines := gps(t, bot, chat)
	if output {
		e2eharness.Assertf(t, ".gps still ran with permission %d denied: %q", permGps, lines)
	}
	if got := goXyzLines(lines); len(got) > 0 {
		e2eharness.Assertf(t, "%d \".go xyz\" lines with permission %d denied: %q", len(got), permGps, got)
	}
	t.Logf("PASS no .gps output and no .go xyz line without permission %d (replies: %q)", permGps, lines)
}

// The words a refused command answers with (AzerothCore acore_string 6 and 193, and any "permission" text). A player
// account normally gets no answer at all for a command it can't see: the text goes out as chat.
var refusals = []string{"does not exist", "permission"}

func refused(lines []string) (string, bool) {
	for _, l := range lines {
		for _, r := range refusals {
			if strings.Contains(strings.ToLower(l), r) {
				return l, true
			}
		}
	}
	return "", false
}

// mod-temucraft eac1afc: a security 0 account may run the read-only debug commands (.lookup item, .distance,
// .mmap loc, .list auras) and gets their normal output, but is still refused the lookups of players, accounts and
// IPs. The refusal is checked after a later .gps answered: commands run in the order they are sent, so by then any
// answer to the lookup would have come.
func TestPlayerDebug_ReadOnlyCommandsAllowed(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "commands", "rbac", "multi_bot"}, Runtime: "short", Category: "temucraft/debugcmds"})

	bots := e2eharness.NewScenario(t, e2eharness.ScenarioOpts{Prefix: "TcDbg", Count: 2, Level: 10})
	bot, other := bots[0], bots[1]
	e2eharness.TeleportAllPad(t, bots, e2eharness.PackagePad(t))
	tcwire.AsPlayer(t, bot)
	chat := tcwire.Collect(t, bot.World)

	cases := []struct {
		cmd    string
		target uint64 // 0 = none, else the selected unit
		want   string
	}{
		{".lookup item Hearthstone", 0, "Hearthstone"}, // LANG_ITEM_LIST_CHAT
		{".distance", other.GUID, "The distance is"},   // LANG_DISTANCE, needs a selected unit
		{".mmap loc", 0, "mmap tileloc:"},              // cs_mmaps
		{".list auras", bot.GUID, "auras:"},            // LANG_COMMAND_TARGET_LISTAURAS
	}
	for _, c := range cases {
		if c.target != 0 {
			bot.World.SetTarget(c.target)
			time.Sleep(100 * time.Millisecond)
		}
		m := chat.Mark()
		bot.GM(t, c.cmd)
		got, ok := chat.WaitSystem(m, c.want, tcwire.DefaultReplyTimeout)
		if !ok {
			e2eharness.Assertf(t, "security 0: %q gave no %q (got %q)", c.cmd, c.want, chat.SystemSince(m))
		}
		if line, bad := refused(chat.SystemSince(m)); bad {
			e2eharness.Assertf(t, "security 0: %q was refused: %q", c.cmd, line)
		}
		t.Logf("%s: %q", c.cmd, got)
	}

	m := chat.Mark()
	bot.GM(t, ".lookup player ip 127.0.0.1")
	bot.GM(t, ".gps")
	if _, ok := chat.WaitSystem(m, gpsLine, tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Preconditionf(t, "security 0: no .gps answer after the lookup (got %q)", chat.SystemSince(m))
	}
	for _, l := range chat.SystemSince(m) {
		for _, answer := range []string{"Characters at account", "No players found", "(GUID ", "Result limit"} {
			if strings.Contains(l, answer) {
				e2eharness.Assertf(t, "security 0 ran .lookup player ip: %q", l)
			}
		}
	}
	t.Logf("PASS the read-only debug commands answered a player account, .lookup player ip did not")
}
