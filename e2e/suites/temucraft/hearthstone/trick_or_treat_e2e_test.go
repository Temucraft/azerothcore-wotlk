//go:build e2e

package hearthstone_test

import (
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
// Uses an innkeeper whose SmartAI casts 24751 on a gossip pick. Picks the first choice (index 0), the exact case that
// went wrong. Oracles: neither pick casts 24751, the bot has neither aura, no item is pushed, and the setting is saved.
func TestHearthstone_InnkeeperChoiceNoTrickOrTreat(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "travel", "hearthstone", "gossip"}, Runtime: "med", Category: "temucraft/hearthstone"})

	worldDB, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer worldDB.Close()
	var innkeeper uint32
	err = worldDB.QueryRow("SELECT ct.`entry` FROM `creature_template` ct JOIN `smart_scripts` ss ON ss.`entryorguid` = ct.`entry` AND ss.`source_type` = 0 "+
		"WHERE ct.`npcflag` & 65536 AND ct.`ScriptName` = '' AND ct.`entry` < 100000 AND ss.`event_type` = 62 AND ss.`action_param1` = ? "+
		"ORDER BY ct.`entry` LIMIT 1", spellTrickOrTreat).Scan(&innkeeper)
	if err != nil {
		e2eharness.Preconditionf(t, "no plain innkeeper whose SmartAI casts Trick or Treat on a gossip pick: %v", err)
	}

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
	t.Logf("PASS innkeeper %d: hearthstone choice saved, no Trick or Treat, no items", innkeeper)
}
