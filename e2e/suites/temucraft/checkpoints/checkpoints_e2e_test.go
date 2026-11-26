//go:build e2e

package checkpoints_test

import (
	"regexp"
	"strconv"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// mod-temucraft-checkpoints d2f70eb:
//   - HideDormant (default 1): a dormant checkpoint's stone is out of every player's phase until it wakes, so it can't
//     be seen, hovered or clicked before its boss is dead (Anub'Rekhan's stone stood there before the pull);
//   - an instance's progress starts with its map: a new run (here: leave, `.instance unbind all`, enter again) has no
//     awake checkpoints of the old run.
//
// Uses Shadowfang Keep (map 33) and the default rows of mod_temucraft_checkpoint_defaults.sql: the entrance (unlock
// type 0, always awake) as the control, the first boss checkpoint that wakes when its creature is dead (type 1) as
// the one that must stay hidden. Needs Temucraft.Checkpoints.Enable = 1 and HideDormant = 1.
const sfk uint32 = 33

type checkpoint struct {
	id, unlockType, entry uint32
	name                  string
	x, y, z               float32
}

func loadCheckpoints(t *testing.T) (entrance, boss checkpoint, ids []checkpoint) {
	t.Helper()
	db, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer db.Close()
	if !tcwire.TableExists(t, db, "mod_temucraft_checkpoint") {
		e2eharness.Preconditionf(t, "no mod_temucraft_checkpoint table: mod-temucraft-checkpoints' world SQL isn't applied")
	}
	rows, err := db.Query("SELECT `id`, `unlock_type`, `gameobject_entry`, `name`, `position_x`, `position_y`, `position_z` "+
		"FROM `mod_temucraft_checkpoint` WHERE `map` = ? AND `enabled` = 1 ORDER BY `sort`, `id`", sfk)
	if err != nil {
		e2eharness.HarnessFailf(t, "mod_temucraft_checkpoint: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c checkpoint
		if err := rows.Scan(&c.id, &c.unlockType, &c.entry, &c.name, &c.x, &c.y, &c.z); err != nil {
			e2eharness.HarnessFailf(t, "mod_temucraft_checkpoint row: %v", err)
		}
		ids = append(ids, c)
		if c.unlockType == 0 && entrance.id == 0 && (c.x != 0 || c.y != 0) {
			entrance = c
		}
		if c.unlockType == 1 && boss.id == 0 && (c.x != 0 || c.y != 0) {
			boss = c
		}
	}
	if entrance.id == 0 || boss.id == 0 {
		e2eharness.Preconditionf(t, "Shadowfang Keep needs an always-awake checkpoint and a creature-dead one with a position (got %+v / %+v)", entrance, boss)
	}
	return entrance, boss, ids
}

var listLine = regexp.MustCompile(`^(\d+) \[\d+\] .* - (awake|dormant)(, respawn here)? \(guid`)

// states runs `.temucraft checkpoint list` and returns checkpoint id -> awake.
func states(t *testing.T, bot *e2eharness.ScenarioBot, chat *tcwire.Collector, want int) map[uint32]bool {
	t.Helper()
	m := chat.Mark()
	bot.GM(t, ".temucraft checkpoint list")
	out := map[uint32]bool{}
	deadline := time.Now().Add(tcwire.DefaultReplyTimeout)
	for len(out) < want && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		for _, line := range chat.SystemSince(m) {
			if g := listLine.FindStringSubmatch(line); g != nil {
				id, _ := strconv.Atoi(g[1])
				out[uint32(id)] = g[2] == "awake"
			}
		}
	}
	if len(out) < want {
		e2eharness.Assertf(t, ".temucraft checkpoint list: %d of %d checkpoints listed (got %q)", len(out), want, chat.SystemSince(m))
	}
	return out
}

func stoneNear(bot *e2eharness.ScenarioBot, entry uint32) bool {
	return bot.World.FindGameObjectByEntry(entry, 6) != 0
}

func waitStone(bot *e2eharness.ScenarioBot, entry uint32, visible bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for stoneNear(bot, entry) != visible {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
	return true
}

func enter(t *testing.T, bot *e2eharness.ScenarioBot, c checkpoint) {
	t.Helper()
	bot.Teleport(t, c.x, c.y, c.z+0.5, sfk)
	bot.WaitInWorld(t, 0)
	bot.FlushWorld(t)
}

// A fresh run: only the always-awake checkpoints are awake; the boss's stone can't be seen before it wakes and shows
// once it does; after leaving, unbinding and entering again (a new run) nothing of the old run is awake.
func TestCheckpoints_DormantHiddenAndFreshRun(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "checkpoints", "instances"}, Runtime: "med", Category: "temucraft/checkpoints"})

	entrance, boss, all := loadCheckpoints(t)
	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcChkp", Level: 80})
	pad := e2eharness.PackagePad(t)
	bot.TeleportPad(t, pad)
	chat := tcwire.Collect(t, bot.World)
	bot.GM(t, ".instance unbind all")

	checkFresh := func(run string) {
		t.Helper()
		st := states(t, bot, chat, len(all))
		for _, c := range all {
			if (c.unlockType == 1 || c.unlockType == 2 || c.unlockType >= 4) && st[c.id] {
				e2eharness.Assertf(t, "%s: checkpoint %d %q (unlock type %d) is awake before its boss was touched", run, c.id, c.name, c.unlockType)
			}
		}
		if !st[entrance.id] {
			t.Logf("%s: the entrance checkpoint %d isn't awake yet", run, entrance.id)
		}
	}

	// run 1: GM mode off, or the bot sees every phase
	enter(t, bot, entrance)
	bot.GM(t, ".gm off")
	bot.FlushWorld(t)
	checkFresh("first run")
	if !waitStone(bot, entrance.entry, true, 10*time.Second) {
		e2eharness.Preconditionf(t, "the awake entrance stone (%d, checkpoint %d) isn't visible at the entrance: can't judge the dormant one", entrance.entry, entrance.id)
	}

	bot.Teleport(t, boss.x, boss.y, boss.z+0.5, sfk)
	bot.FlushWorld(t)
	time.Sleep(3 * time.Second) // the stone's phase is set on the instance's first look
	if waitStone(bot, boss.entry, true, 2*time.Second) {
		e2eharness.Assertf(t, "the dormant stone of checkpoint %d %q is visible before its creature is dead (HideDormant)", boss.id, boss.name)
	}
	bot.GM(t, ".temucraft checkpoint wake")
	if !waitStone(bot, boss.entry, true, 10*time.Second) {
		e2eharness.Assertf(t, "checkpoint %d %q woke but its stone didn't appear", boss.id, boss.name)
	}
	if st := states(t, bot, chat, len(all)); !st[boss.id] {
		e2eharness.Assertf(t, "after .temucraft checkpoint wake, checkpoint %d is still dormant", boss.id)
	}
	t.Logf("first run: boss stone hidden while dormant, shown once awake")

	// run 2: out, unbind, in again
	bot.GM(t, ".gm on")
	bot.TeleportPad(t, pad)
	bot.GM(t, ".instance unbind all")
	bot.FlushWorld(t)
	enter(t, bot, entrance)
	bot.GM(t, ".gm off")
	bot.FlushWorld(t)
	checkFresh("new run")
	bot.Teleport(t, boss.x, boss.y, boss.z+0.5, sfk)
	bot.FlushWorld(t)
	time.Sleep(3 * time.Second)
	if stoneNear(bot, boss.entry) {
		e2eharness.Assertf(t, "new run: the stone of checkpoint %d %q is visible again before its boss (old run's state carried over?)", boss.id, boss.name)
	}

	bot.GM(t, ".gm on")
	bot.TeleportPad(t, pad)
	bot.GM(t, ".instance unbind all")
	t.Logf("PASS checkpoints: dormant stone hidden until it wakes; a new run starts with nothing awake")
}
