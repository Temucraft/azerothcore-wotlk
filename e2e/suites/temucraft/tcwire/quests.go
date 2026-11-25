//go:build e2e

package tcwire

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"testing"
	"time"

	"github.com/azerothcore/AzerothGhost/client"
	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
)

// Quest and spell opcodes the harness names but doesn't decode for us.
const (
	SmsgQuestgiverQuestDetails uint16 = 0x188
	SmsgItemPushResult         uint16 = 0x166
	SmsgSpellStart             uint16 = 0x131
	SmsgSpellGo                uint16 = 0x132
	SmsgLearnedSpell           uint16 = 0x12B
	SmsgRemovedSpell           uint16 = 0x203
)

// QuestOffer is one quest a quest giver lists (gossip quest section or SMSG_QUESTGIVER_QUEST_LIST).
type QuestOffer struct {
	ID    uint32
	Icon  uint32
	Title string
}

func readQuestEntries(r *bytes.Reader, count uint32) []QuestOffer {
	var out []QuestOffer
	for i := uint32(0); i < count; i++ {
		var q QuestOffer
		var level int32
		var flags uint32
		var repeatable uint8
		if binary.Read(r, binary.LittleEndian, &q.ID) != nil {
			break
		}
		binary.Read(r, binary.LittleEndian, &q.Icon)
		binary.Read(r, binary.LittleEndian, &level)
		binary.Read(r, binary.LittleEndian, &flags)
		binary.Read(r, binary.LittleEndian, &repeatable)
		q.Title = readCString(r)
		out = append(out, q)
	}
	return out
}

// ParseGossipQuests decodes the quest section that follows the options of an SMSG_GOSSIP_MESSAGE.
func ParseGossipQuests(data []byte) ([]QuestOffer, bool) {
	r := bytes.NewReader(data)
	var guid uint64
	var menuID, textID, count uint32
	if binary.Read(r, binary.LittleEndian, &guid) != nil {
		return nil, false
	}
	binary.Read(r, binary.LittleEndian, &menuID)
	binary.Read(r, binary.LittleEndian, &textID)
	if binary.Read(r, binary.LittleEndian, &count) != nil || count > 64 {
		return nil, false
	}
	for i := uint32(0); i < count; i++ {
		var index, money uint32
		var icon, coded uint8
		binary.Read(r, binary.LittleEndian, &index)
		binary.Read(r, binary.LittleEndian, &icon)
		binary.Read(r, binary.LittleEndian, &coded)
		binary.Read(r, binary.LittleEndian, &money)
		_ = readCString(r)
		_ = readCString(r)
	}
	var quests uint32
	if binary.Read(r, binary.LittleEndian, &quests) != nil || quests > 64 {
		return nil, false
	}
	return readQuestEntries(r, quests), true
}

// ParseQuestList decodes an SMSG_QUESTGIVER_QUEST_LIST.
func ParseQuestList(data []byte) ([]QuestOffer, bool) {
	r := bytes.NewReader(data)
	var guid uint64
	if binary.Read(r, binary.LittleEndian, &guid) != nil {
		return nil, false
	}
	_ = readCString(r) // greeting
	var delay, emote uint32
	var count uint8
	binary.Read(r, binary.LittleEndian, &delay)
	binary.Read(r, binary.LittleEndian, &emote)
	if binary.Read(r, binary.LittleEndian, &count) != nil {
		return nil, false
	}
	return readQuestEntries(r, uint32(count)), true
}

// QuestOffers talks to a quest giver (CMSG_QUESTGIVER_HELLO) and returns the quests it shows. The server answers
// with a gossip menu (options and quests), a quest list, or straight with the details of its only quest; all three
// are read. Fails the test when nothing comes back.
func QuestOffers(t *testing.T, w *client.WorldClient, npc uint64) []QuestOffer {
	t.Helper()
	got := make(chan []QuestOffer, 1)
	cancel := w.AddPacketHook(func(op uint16, data []byte) {
		if len(data) < 8 || binary.LittleEndian.Uint64(data) != npc {
			return
		}
		var offers []QuestOffer
		var ok bool
		switch op {
		case SmsgGossipMessage:
			offers, ok = ParseGossipQuests(data)
		case client.SmsgQuestgiverQuestList:
			offers, ok = ParseQuestList(data)
		case SmsgQuestgiverQuestDetails:
			if len(data) >= 20 {
				offers, ok = []QuestOffer{{ID: binary.LittleEndian.Uint32(data[16:])}}, true
			}
		}
		if !ok {
			return
		}
		select {
		case got <- offers:
		default:
		}
	})
	defer cancel()
	if err := w.QuestgiverHello(npc); err != nil {
		e2eharness.HarnessFailf(t, "questgiver hello %#x: %v", npc, err)
	}
	select {
	case offers := <-got:
		return offers
	case <-time.After(DefaultReplyTimeout):
		e2eharness.Assertf(t, "no gossip, quest list or quest details from %#x after CMSG_QUESTGIVER_HELLO", npc)
	}
	return nil
}

// QuestIDs lists the ids of the offers (for logs and lookups).
func QuestIDs(offers []QuestOffer) []uint32 {
	out := make([]uint32, 0, len(offers))
	for _, q := range offers {
		out = append(out, q.ID)
	}
	return out
}

// ReadPackedGUID reads a packed guid (mask byte, then one byte per set bit).
func ReadPackedGUID(r *bytes.Reader) uint64 {
	mask, err := r.ReadByte()
	if err != nil {
		return 0
	}
	var guid uint64
	for i := uint(0); i < 8; i++ {
		if mask&(1<<i) != 0 {
			b, err := r.ReadByte()
			if err != nil {
				return guid
			}
			guid |= uint64(b) << (i * 8)
		}
	}
	return guid
}

// SpellOfCast returns the caster and spell id of an SMSG_SPELL_START / SMSG_SPELL_GO body.
func SpellOfCast(data []byte) (caster uint64, spell uint32, ok bool) {
	r := bytes.NewReader(data)
	caster = ReadPackedGUID(r)
	_ = ReadPackedGUID(r) // caster unit
	var castCount uint8
	if binary.Read(r, binary.LittleEndian, &castCount) != nil {
		return 0, 0, false
	}
	if binary.Read(r, binary.LittleEndian, &spell) != nil {
		return 0, 0, false
	}
	return caster, spell, true
}

// Reputation returns a character's saved standing with a faction (0 when there is no row). Save first.
func Reputation(t *testing.T, charDB *sql.DB, guid uint64, faction uint32) int32 {
	t.Helper()
	var standing int32
	err := charDB.QueryRow("SELECT `standing` FROM `character_reputation` WHERE `guid` = ? AND `faction` = ?", Low(guid), faction).Scan(&standing)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		e2eharness.HarnessFailf(t, "character_reputation %d: %v", faction, err)
	}
	return standing
}

// TableExists reports whether a table is in the database the handle points at.
func TableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM `information_schema`.`TABLES` WHERE `TABLE_SCHEMA` = DATABASE() AND `TABLE_NAME` = ?", table).Scan(&n); err != nil {
		e2eharness.HarnessFailf(t, "information_schema %s: %v", table, err)
	}
	return n > 0
}

// ModuleStrings reports whether a module's texts are in module_string (its world SQL was applied).
func ModuleStrings(t *testing.T, worldDB *sql.DB, module string) bool {
	t.Helper()
	var n int
	if err := worldDB.QueryRow("SELECT COUNT(*) FROM `module_string` WHERE `module` = ?", module).Scan(&n); err != nil {
		e2eharness.HarnessFailf(t, "module_string %s: %v", module, err)
	}
	return n > 0
}
