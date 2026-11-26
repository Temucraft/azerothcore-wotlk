//go:build e2e

package auras_test

import (
	"fmt"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// two of the aura toys (item -> look-only aura), from mod-temucraft-auras' list
const (
	toyA, auraA uint32 = 610110, 57901
	toyB, auraB uint32 = 610111, 70788
	permUse     uint32 = 103650 // Temucraft: use cosmetic aura toys
)

type toy struct {
	bag, slot uint8
	guid      uint64
	spell     uint32
}

func giveToy(t *testing.T, bot *e2eharness.ScenarioBot, entry uint32) toy {
	t.Helper()
	worldDB, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer worldDB.Close()
	bag, slot := bot.AddItemWait(t, entry, 1)
	return toy{bag: bag, slot: slot, guid: e2eharness.ItemGUID(tcwire.ItemLowGUID(t, bot, bag, slot)), spell: tcwire.ItemSpell(t, worldDB, entry)}
}

func use(t *testing.T, bot *e2eharness.ScenarioBot, chat *tcwire.Collector, it toy, want string) {
	t.Helper()
	m := chat.Mark()
	// an item the server refuses comes back as SMSG_INVENTORY_CHANGE_FAILURE (the equip error is its first byte)
	waitErr, cancel := tcwire.ArmPacket(bot.World, tcwire.SmsgInventoryChangeFailure, nil)
	defer cancel()
	tcwire.UseItem(t, bot.World, it.bag, it.slot, it.guid, it.spell)
	if _, ok := chat.WaitSystem(m, want, tcwire.DefaultReplyTimeout); !ok {
		equipErr := "none"
		if d, ok := waitErr(10 * time.Millisecond); ok && len(d) > 0 {
			equipErr = fmt.Sprintf("%d", d[0])
		}
		e2eharness.Assertf(t, "using the toy (bag %d slot %d, spell %d): no %q (system %q, notifications %q, equip error %s)",
			it.bag, it.slot, it.spell, want, chat.SystemSince(m), chat.NotesSince(m), equipErr)
	}
}

// checkPermission: the toys need RBAC permission 103650 (mod_temucraft_auras_rbac_player.sql), linked to the player
// roles 195 and 1010. A harness account is GM level 3 (role 1014 -> ... -> 1010 on Temucraft, 192 -> ... -> 195 on
// stock AC). Without the links the toy only says "You can't use this here.": say why instead.
func checkPermission(t *testing.T, bot *e2eharness.ScenarioBot) {
	t.Helper()
	var name string
	if err := bot.AuthDB.QueryRow("SELECT `name` FROM `rbac_permissions` WHERE `id` = ?", permUse).Scan(&name); err != nil {
		e2eharness.Preconditionf(t, "RBAC permission %d is not in the auth database (%v): apply mod-temucraft-courier's db-auth SQL (mod_temucraft_auras_rbac_player.sql)", permUse, err)
	}
	var links int
	if err := bot.AuthDB.QueryRow("SELECT COUNT(*) FROM `rbac_linked_permissions` WHERE `linkedId` = ? AND `id` IN (195, 1010)", permUse).Scan(&links); err != nil {
		e2eharness.HarnessFailf(t, "rbac_linked_permissions: %v", err)
	}
	if links == 0 {
		e2eharness.Preconditionf(t, "RBAC permission %d (%s) is linked to neither 195 nor 1010: re-run mod_temucraft_auras_rbac_player.sql, then .reload rbac", permUse, name)
	}
}

func savedAura(t *testing.T, bot *e2eharness.ScenarioBot) (int64, bool) {
	t.Helper()
	bot.Save(t)
	return tcwire.CharSetting(t, bot.CharDB, bot.GUID, "auras")
}

// The cosmetic aura toys: a toy switches its aura on (saved per character), the aura comes back after a relog and
// after death, a second toy replaces the first (one at a time), and using the chosen toy again switches it off and
// forgets it.
func TestAuras_ToySwitchesAndRemembers(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "auras"}, Runtime: "med", Category: "temucraft/auras"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcAura", Level: 80})
	checkPermission(t, bot)
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	chat := tcwire.Collect(t, bot.World)
	a, b := giveToy(t, bot, toyA), giveToy(t, bot, toyB)

	use(t, bot, chat, a, "Aura on.")
	e2eharness.WaitAura(t, bot.World, auraA, 5*time.Second)
	if v, ok := savedAura(t, bot); !ok || uint32(v) != auraA {
		e2eharness.Assertf(t, "aura choice not saved (value=%d, row=%v)", v, ok)
	}

	bot.Relog(t)
	chat = tcwire.Collect(t, bot.World)
	e2eharness.WaitAura(t, bot.World, auraA, 10*time.Second)

	bot.DieAndRepop(t)
	bot.GM(t, ".revive")
	bot.WaitAlive(t, 15*time.Second)
	e2eharness.WaitAura(t, bot.World, auraA, 10*time.Second)
	bot.TeleportPad(t, e2eharness.PackagePad(t))

	use(t, bot, chat, b, "Aura on.")
	e2eharness.WaitAura(t, bot.World, auraB, 5*time.Second)
	if !e2eharness.WaitAuraGone(t, bot.World, auraA, 5*time.Second) {
		e2eharness.Assertf(t, "the first toy's aura stayed after the second toy (one at a time)")
	}

	use(t, bot, chat, b, "Aura off.")
	if !e2eharness.WaitAuraGone(t, bot.World, auraB, 5*time.Second) {
		e2eharness.Assertf(t, "\"Aura off.\" but the aura stayed")
	}
	if v, ok := savedAura(t, bot); ok {
		e2eharness.Assertf(t, "aura switched off but the choice is still saved (%d)", v)
	}
	t.Logf("PASS aura toys")
}
