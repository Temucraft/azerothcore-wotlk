//go:build e2e

package hearthstone_test

import (
	"encoding/binary"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

const spellHearthstone uint32 = 8690

func saved(t *testing.T, bot *e2eharness.ScenarioBot) (int64, bool) {
	t.Helper()
	bot.Save(t)
	return tcwire.CharSetting(t, bot.CharDB, bot.GUID, "hearthstone")
}

// `.temucraft hearthstone [minutes]`: the status line, a choice that is saved and still there after a relog, and a
// refusal for minutes the server doesn't offer (the saved choice must not change). Assumes the default choices
// (Temucraft.Travel.Hearthstone.Choices = 0,15,30,45,60).
func TestHearthstone_CommandChoice(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "travel", "hearthstone"}, Runtime: "short", Category: "temucraft/hearthstone"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcHsCmd", Level: 80})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	chat := tcwire.Collect(t, bot.World)

	tcwire.Command(t, bot.World, chat, ".temucraft hearthstone", "Choose with .temucraft hearthstone")
	tcwire.Command(t, bot.World, chat, ".temucraft hearthstone 15", "cooldown set to 15 minutes")
	if v, ok := saved(t, bot); !ok || v != 15 {
		e2eharness.Assertf(t, "choice 15 not saved (value=%d, row=%v)", v, ok)
	}

	bot.Relog(t)
	chat = tcwire.Collect(t, bot.World)
	tcwire.Command(t, bot.World, chat, ".temucraft hearthstone", "cooldown 15 minutes")

	tcwire.Command(t, bot.World, chat, ".temucraft hearthstone 7", "isn't one of the choices")
	if v, _ := saved(t, bot); v != 15 {
		e2eharness.Assertf(t, "a refused choice changed the saved one to %d", v)
	}
	t.Logf("PASS hearthstone command choice")
}

// The same choice from an innkeeper: a plain innkeeper (no script of its own) shows "Hearthstone cooldown (...)"
// above its menu, the next page lists the choices, and picking one saves it.
func TestHearthstone_InnkeeperGossip(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "travel", "hearthstone", "gossip"}, Runtime: "med", Category: "temucraft/hearthstone"})

	worldDB, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer worldDB.Close()
	var innkeeper uint32
	if err := worldDB.QueryRow("SELECT `entry` FROM `creature_template` WHERE `npcflag` & 65536 AND `ScriptName` = '' AND `entry` < 100000 ORDER BY `entry` LIMIT 1").Scan(&innkeeper); err != nil {
		e2eharness.Preconditionf(t, "no plain innkeeper template: %v", err)
	}

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcHsInn", Level: 80})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	chat := tcwire.Collect(t, bot.World)
	guid := bot.Spawn(t, innkeeper, 15*time.Second)

	menu := tcwire.GossipHello(t, bot.World, guid)
	line, ok := menu.Find("Hearthstone cooldown (")
	if !ok {
		e2eharness.Assertf(t, "innkeeper %d: no hearthstone line in %q", innkeeper, menu.Texts())
	}
	wait, cancel := tcwire.ArmGossip(bot.World, guid)
	tcwire.GossipSelect(t, bot.World, menu, line)
	choices, ok := wait(tcwire.DefaultReplyTimeout)
	cancel()
	if !ok {
		e2eharness.Assertf(t, "no choices page after %q", tcwire.Plain(line.Text))
	}
	t.Logf("choices: %q", choices.Texts())
	pick, ok := choices.Find("45 minutes")
	if !ok {
		e2eharness.Assertf(t, "no \"45 minutes\" choice in %q", choices.Texts())
	}
	m := chat.Mark()
	tcwire.GossipSelect(t, bot.World, choices, pick)
	if _, ok := chat.WaitSystem(m, "cooldown set to 45 minutes", tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Assertf(t, "picking 45 minutes: no confirmation (got %q)", chat.SystemSince(m))
	}
	if v, ok := saved(t, bot); !ok || v != 45 {
		e2eharness.Assertf(t, "innkeeper choice not saved (value=%d, row=%v)", v, ok)
	}
	t.Logf("PASS innkeeper hearthstone choice")
}

// "No cooldown" really clears the Hearthstone cooldown after a cast: the server sends SMSG_CLEAR_COOLDOWN for 8690
// shortly after the spell goes off.
func TestHearthstone_NoCooldownClearsAfterCast(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "travel", "hearthstone"}, Runtime: "med", Category: "temucraft/hearthstone"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcHsCd", Level: 80})
	pad := e2eharness.PackagePad(t)
	bot.TeleportPad(t, pad)
	chat := tcwire.Collect(t, bot.World)
	tcwire.Command(t, bot.World, chat, ".temucraft hearthstone 0", "cooldown set to no cooldown")
	bot.Learn(t, spellHearthstone)
	bot.FlushWorld(t)

	wait, cancel := tcwire.ArmPacket(bot.World, tcwire.SmsgClearCooldown, func(d []byte) bool {
		return len(d) >= 4 && binary.LittleEndian.Uint32(d) == spellHearthstone
	})
	defer cancel()
	bot.Cast(t, spellHearthstone, 0, 20*time.Second)
	if _, ok := wait(10 * time.Second); !ok {
		e2eharness.Assertf(t, "no SMSG_CLEAR_COOLDOWN for the Hearthstone with \"no cooldown\" chosen")
	}
	bot.WaitInWorld(t, 0)
	bot.TeleportPad(t, pad)
	t.Logf("PASS no cooldown after the Hearthstone")
}
