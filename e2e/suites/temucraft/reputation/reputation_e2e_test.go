//go:build e2e

package reputation_test

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/client"
	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// The 3.3.5a client keeps reputation in a list of 128 slots (CGReputationInfo::m_factionStandings[128]) and does not
// check the slot number the server sends. Slot 128 and up is written over the client's reputation headers, and the
// client crashes the next time it adds or frees a header: talking to an NPC, gaining reputation, logging in or out.
// On 2026-11-23 the Lost Lands factions 2314, 2318, 2319 and 2321 had slots 128-131 in faction_dbc.
const (
	smsgSetFactionVisible  uint16 = 0x123
	smsgSetFactionStanding uint16 = 0x124
	clientReputationSlots         = 128
	npcSirMaloryWheeler    uint32 = 15007 // League of Arathor: talking to him made Forsaken characters crash
	factionUndercity       uint32 = 68
)

// The Lost Lands factions that had slots past 127 (an SQL list).
const lostLandsFactions = "2314, 2318, 2319, 2321"

// slotWatch records every reputation slot number the server sends after login.
type slotWatch struct {
	mu   sync.Mutex
	seen []uint32
}

func watchSlots(t *testing.T, w *client.WorldClient) *slotWatch {
	t.Helper()
	s := &slotWatch{}
	cancel := w.AddPacketHook(func(op uint16, data []byte) {
		var slots []uint32
		switch op {
		case smsgSetFactionVisible: // uint32 slot
			if len(data) >= 4 {
				slots = append(slots, binary.LittleEndian.Uint32(data))
			}
		case smsgSetFactionStanding: // float bonus, uint8 effect, uint32 count, count x (uint32 slot, int32 standing)
			if len(data) >= 9 {
				count := binary.LittleEndian.Uint32(data[5:])
				for i := uint32(0); i < count && 9+8*int(i)+8 <= len(data); i++ {
					slots = append(slots, binary.LittleEndian.Uint32(data[9+8*int(i):]))
				}
			}
		}
		if len(slots) > 0 {
			s.mu.Lock()
			s.seen = append(s.seen, slots...)
			s.mu.Unlock()
		}
	})
	t.Cleanup(cancel)
	return s
}

func (s *slotWatch) snapshot() []uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint32(nil), s.seen...)
}

// No faction override in the world database may give a reputation slot the client doesn't have. faction_dbc wins
// over the server's Faction.dbc, so a bad row there reaches every player at login.
func TestReputation_FactionSlotsFitTheClient(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "reputation", "crash"}, Runtime: "short", Category: "temucraft/reputation"})

	db, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world database: %v", err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT `ID`, `ReputationIndex` FROM `faction_dbc` WHERE `ReputationIndex` >= ?", clientReputationSlots)
	if err != nil {
		e2eharness.HarnessFailf(t, "faction_dbc: %v", err)
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var id uint32
		var index int32
		if rows.Scan(&id, &index) == nil {
			bad = append(bad, fmt.Sprintf("%d (slot %d)", id, index))
		}
	}
	if len(bad) > 0 {
		e2eharness.Assertf(t, "faction_dbc gives reputation slots the 3.3.5a client doesn't have (0-%d): %s",
			clientReputationSlots-1, strings.Join(bad, ", "))
	}
	t.Logf("PASS every faction_dbc reputation slot is below %d", clientReputationSlots)
}

// A Forsaken character talks to Sir Malory Wheeler and gains reputation, then logs out and in twice. Every slot the
// server sends must be below 128, no Lost Lands faction may come back into the character's reputation, and the
// world must stay up (the server used to assert while building an oversized reputation list at login).
func TestReputation_ForsakenGossipAndRelog(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "reputation", "crash"}, Runtime: "short", Category: "temucraft/reputation"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcRep", Level: 10, Race: e2eharness.RaceUndead, Class: e2eharness.ClassWarrior})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	slots := watchSlots(t, bot.World)

	// gossip makes the NPC's faction visible (SMSG_SET_FACTION_VISIBLE when it wasn't yet)
	npc := e2eharness.SpawnNPCAndWait(t, bot.World, npcSirMaloryWheeler, 15*time.Second)
	tcwire.SendGossipHello(t, bot.World, npc)
	// a reputation change sends SMSG_SET_FACTION_STANDING with every slot that needs sending
	bot.World.SetTarget(bot.GUID)
	time.Sleep(100 * time.Millisecond)
	bot.GM(t, fmt.Sprintf(".modify reputation %d 3000", factionUndercity))
	bot.FlushWorld(t)
	time.Sleep(time.Second)

	seen := slots.snapshot()
	for _, s := range seen {
		if s >= clientReputationSlots {
			e2eharness.Assertf(t, "the server sent reputation slot %d; the client only has 0-%d (all sent: %v)", s, clientReputationSlots-1, seen)
		}
	}
	t.Logf("reputation slots sent after login: %v", seen)

	bot.Save(t)
	var n int
	if err := bot.CharDB.QueryRow("SELECT COUNT(*) FROM `character_reputation` WHERE `guid` = ? AND `faction` IN ("+lostLandsFactions+")", tcwire.Low(bot.GUID)).Scan(&n); err != nil {
		e2eharness.HarnessFailf(t, "character_reputation: %v", err)
	}
	if n > 0 {
		e2eharness.Assertf(t, "%d Lost Lands faction rows (%s) came back into the character's reputation", n, lostLandsFactions)
	}

	bot.Relog(t)
	bot.Relog(t)
	bot.AssertWorldAlive(t)
	t.Logf("PASS every reputation slot fit the client, no Lost Lands rows, two relogs, world alive")
}
