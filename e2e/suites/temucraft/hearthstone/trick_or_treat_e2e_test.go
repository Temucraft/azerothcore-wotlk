//go:build e2e

package hearthstone_test

import (
	"database/sql"
	"encoding/binary"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

const (
	spellTrickOrTreat   uint32 = 24751 // what innkeepers' SmartAI casts on (their menu, item 0)
	spellTrickedTreated uint32 = 24755 // its "Tricked or Treated" debuff
)

// castWatch records every SMSG_SPELL_START / SMSG_SPELL_GO of some spells and every item pushed to the bot.
type castWatch struct {
	mu     sync.Mutex
	casts  []uint32
	pushes []uint32
}

func watchCasts(t *testing.T, bot *e2eharness.ScenarioBot, spells ...uint32) *castWatch {
	t.Helper()
	cw := &castWatch{}
	cancel := bot.World.AddPacketHook(func(op uint16, data []byte) {
		switch op {
		case tcwire.SmsgSpellStart, tcwire.SmsgSpellGo:
			if _, spell, ok := tcwire.SpellOfCast(data); ok {
				for _, s := range spells {
					if s == spell {
						cw.mu.Lock()
						cw.casts = append(cw.casts, spell)
						cw.mu.Unlock()
					}
				}
			}
		case tcwire.SmsgItemPushResult: // guid, received, created, in chat, bag, slot, item entry
			if len(data) >= 29 {
				cw.mu.Lock()
				cw.pushes = append(cw.pushes, binary.LittleEndian.Uint32(data[25:]))
				cw.mu.Unlock()
			}
		}
	})
	t.Cleanup(cancel)
	return cw
}

func (cw *castWatch) snapshot() (casts, pushes []uint32) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	return append([]uint32(nil), cw.casts...), append([]uint32(nil), cw.pushes...)
}

// mod-temucraft-travel 3512616: the core runs the innkeeper's SmartAI GOSSIP_SELECT (menu id, item index) before any
// script, and innkeepers cast Trick or Treat on (their menu, item 0). The hearthstone option used to get index 0 and
// its choice page kept the innkeeper's menu id, so picking the option, or the first choice, played Trick or Treat.
// Now the option takes the last index and the choices go out under a menu id of their own.
//
// Uses an innkeeper whose SmartAI casts 24751 on a gossip pick, directly or through a timed action list, when the
// world database has one (pickInnkeeper); otherwise any plain innkeeper with a gossip menu. That one can't play
// Trick or Treat at all, so the item index and menu id checks carry the test then (they are what 3512616 changed).
// Picks the first choice (index 0), the exact case that went wrong. Oracles: neither pick casts 24751, the bot has
// neither aura, no item is pushed, and the setting is saved.
func TestHearthstone_InnkeeperChoiceNoTrickOrTreat(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "travel", "hearthstone", "gossip"}, Runtime: "med", Category: "temucraft/hearthstone"})

	worldDB, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer worldDB.Close()
	innkeeper, smart := pickInnkeeper(t, worldDB)

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcHsToT", Level: 80})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	chat := tcwire.Collect(t, bot.World)
	// a choice to move away from, so the pick below has to change the saved value
	tcwire.Command(t, bot.World, chat, ".temucraft hearthstone 45", "cooldown set to 45 minutes")
	guid := bot.Spawn(t, innkeeper, 15*time.Second)
	casts := watchCasts(t, bot, spellTrickOrTreat, spellTrickedTreated)

	menu := tcwire.GossipHello(t, bot.World, guid)
	line, ok := menu.Find("Hearthstone cooldown (")
	if !ok {
		e2eharness.Assertf(t, "innkeeper %d: no hearthstone line in %q", innkeeper, menu.Texts())
	}
	if line.Index == 0 {
		e2eharness.Assertf(t, "innkeeper %d: the hearthstone option has item index 0, the one its SmartAI answers with Trick or Treat", innkeeper)
	}
	wait, cancel := tcwire.ArmGossip(bot.World, guid)
	tcwire.GossipSelect(t, bot.World, menu, line)
	choices, ok := wait(tcwire.DefaultReplyTimeout)
	cancel()
	if !ok || len(choices.Options) == 0 {
		e2eharness.Assertf(t, "no choices page after %q", tcwire.Plain(line.Text))
	}
	if choices.MenuID == menu.MenuID {
		e2eharness.Assertf(t, "the choices page kept the innkeeper's menu id %d, so its first choice is (menu %d, item 0) again", menu.MenuID, menu.MenuID)
	}
	first := choices.Options[0]
	for _, o := range choices.Options {
		if o.Index < first.Index {
			first = o
		}
	}
	if strings.HasPrefix(tcwire.Plain(first.Text), ">") {
		e2eharness.Preconditionf(t, "the first choice %q is the current one (45 minutes); the realm's choices are not the defaults", tcwire.Plain(first.Text))
	}
	t.Logf("menu %d, hearthstone item %d; choices menu %d %q; picking item %d %q", menu.MenuID, line.Index, choices.MenuID, choices.Texts(), first.Index, tcwire.Plain(first.Text))

	m := chat.Mark()
	tcwire.GossipSelect(t, bot.World, choices, first)
	if _, ok := chat.WaitSystem(m, "cooldown set to", tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Assertf(t, "picking %q: no confirmation (got %q)", tcwire.Plain(first.Text), chat.SystemSince(m))
	}
	bot.FlushWorld(t) // the SmartAI cast would have been sent while the pick was handled

	cast, pushed := casts.snapshot()
	if len(cast) > 0 {
		e2eharness.Assertf(t, "the hearthstone gossip cast %v (Trick or Treat)", cast)
	}
	if bot.HasAura(spellTrickOrTreat) || bot.HasAura(spellTrickedTreated) {
		e2eharness.Assertf(t, "the bot has Trick or Treat (%d: %v) or Tricked or Treated (%d: %v) after the hearthstone gossip",
			spellTrickOrTreat, bot.HasAura(spellTrickOrTreat), spellTrickedTreated, bot.HasAura(spellTrickedTreated))
	}
	if len(pushed) > 0 {
		e2eharness.Assertf(t, "the hearthstone gossip gave items %v", pushed)
	}
	if v, ok := saved(t, bot); !ok || v == 45 {
		e2eharness.Assertf(t, "the innkeeper choice was not saved (value=%d, row=%v)", v, ok)
	}
	t.Logf("PASS innkeeper %d (Trick or Treat SmartAI: %v): hearthstone choice saved, no Trick or Treat, no items", innkeeper, smart)
}

// pickInnkeeper finds a plain innkeeper (npcflag INNKEEPER, no ScriptName, so mod-temucraft-travel adds its line, and a
// gossip menu) and prefers one whose SmartAI casts Trick or Treat on a gossip pick: a GOSSIP_SELECT (62) row that
// casts 24751 itself, or that starts a timed action list (action 80, or 87/88 random lists) whose rows cast it. The
// 2026-11-26 PTR run found no direct row (the stock data goes through action lists or has none), which failed the
// test as a precondition; now it falls back to any plain innkeeper and says so.
func pickInnkeeper(t *testing.T, worldDB *sql.DB) (entry uint32, smart bool) {
	t.Helper()
	const plain = "ct.`npcflag` & 65536 AND ct.`ScriptName` = '' AND ct.`gossip_menu_id` <> 0 AND ct.`entry` < 100000"
	err := worldDB.QueryRow("SELECT ct.`entry` FROM `creature_template` ct "+
		"JOIN `smart_scripts` ss ON ss.`entryorguid` = ct.`entry` AND ss.`source_type` = 0 AND ss.`event_type` = 62 "+
		"WHERE "+plain+" AND ct.`AIName` = 'SmartAI' AND ("+
		"(ss.`action_type` IN (11, 85, 86) AND ss.`action_param1` = ?) OR "+
		"(ss.`action_type` = 80 AND EXISTS (SELECT 1 FROM `smart_scripts` al WHERE al.`source_type` = 9 AND al.`entryorguid` = ss.`action_param1` "+
		"AND al.`action_type` IN (11, 85, 86) AND al.`action_param1` = ?)) OR "+
		"(ss.`action_type` IN (87, 88) AND EXISTS (SELECT 1 FROM `smart_scripts` al WHERE al.`source_type` = 9 "+
		"AND al.`entryorguid` BETWEEN ss.`action_param1` AND GREATEST(ss.`action_param1`, ss.`action_param2`, ss.`action_param3`, ss.`action_param4`, ss.`action_param5`, ss.`action_param6`) "+
		"AND al.`action_type` IN (11, 85, 86) AND al.`action_param1` = ?))) "+
		"ORDER BY ct.`entry` LIMIT 1", spellTrickOrTreat, spellTrickOrTreat, spellTrickOrTreat).Scan(&entry)
	if err == nil {
		return entry, true
	}
	if err != sql.ErrNoRows {
		e2eharness.HarnessFailf(t, "innkeeper SmartAI query: %v", err)
	}
	if err := worldDB.QueryRow("SELECT ct.`entry` FROM `creature_template` ct WHERE " + plain + " ORDER BY ct.`entry` LIMIT 1").Scan(&entry); err != nil {
		e2eharness.Preconditionf(t, "no plain innkeeper with a gossip menu in the world database: %v", err)
	}
	t.Logf("no innkeeper whose SmartAI casts Trick or Treat (%d) on a gossip pick; using plain innkeeper %d: the cast checks "+
		"can't fail with it, the item index and menu id checks are the oracle", spellTrickOrTreat, entry)
	return entry, false
}
