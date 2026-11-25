//go:build e2e

package firstmount_test

import (
	"fmt"
	"math/bits"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// mod-temucraft-first-mount: 602110-602119 are the classic races' first mount quests, 602140-602159 the custom races'
// (mod_temucraft_first_mount_races.sql, one per race, same quest giver as the home race).
const (
	raceQuestFirst uint32 = 602140
	raceQuestLast  uint32 = 602159
	questTauren    uint32 = 602115
	npcKar         uint32 = 3690 // Kar Stormsinger, Bloodhoof Village: the Tauren quest giver, and the home quest giver of races 14, 16 and 21
)

// Every custom race quest is for exactly one race: AllowableRaces is not 0 and has one bit, and where the module's
// temucraft_first_mount_race table names the quest's race, it is that race's bit (311465c: a wrong-race quest or
// drake reached other races).
func TestFirstMount_RaceQuestsAreForOneRace(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "firstmount", "quests"}, Runtime: "short", Category: "temucraft/firstmount"})

	db, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer db.Close()

	raceOf := map[uint32]uint32{}
	if tcwire.TableExists(t, db, "temucraft_first_mount_race") {
		rows, err := db.Query("SELECT `Quest`, `Race` FROM `temucraft_first_mount_race`")
		if err != nil {
			e2eharness.HarnessFailf(t, "temucraft_first_mount_race: %v", err)
		}
		for rows.Next() {
			var q, r uint32
			if rows.Scan(&q, &r) == nil {
				raceOf[q] = r
			}
		}
		rows.Close()
	}

	rows, err := db.Query("SELECT `ID`, `AllowableRaces` FROM `quest_template` WHERE `ID` BETWEEN ? AND ? ORDER BY `ID`", raceQuestFirst, raceQuestLast)
	if err != nil {
		e2eharness.HarnessFailf(t, "quest_template: %v", err)
	}
	defer rows.Close()
	var bad, ok []string
	for rows.Next() {
		var id uint32
		var races uint64
		if err := rows.Scan(&id, &races); err != nil {
			e2eharness.HarnessFailf(t, "quest_template row: %v", err)
		}
		switch race, known := raceOf[id]; {
		case races == 0:
			bad = append(bad, fmt.Sprintf("%d: AllowableRaces 0 (every race)", id))
		case bits.OnesCount64(races) != 1:
			bad = append(bad, fmt.Sprintf("%d: AllowableRaces %#x has %d races", id, races, bits.OnesCount64(races)))
		case known && races != 1<<(race-1):
			bad = append(bad, fmt.Sprintf("%d: AllowableRaces %#x, but it is the quest of race %d (%#x)", id, races, race, uint64(1)<<(race-1)))
		default:
			ok = append(ok, fmt.Sprintf("%d=race %d", id, bits.TrailingZeros64(races)+1))
		}
	}
	if len(bad)+len(ok) == 0 {
		e2eharness.Preconditionf(t, "no quest_template rows %d-%d (is mod-temucraft-first-mount's SQL applied?)", raceQuestFirst, raceQuestLast)
	}
	if len(bad) > 0 {
		e2eharness.Assertf(t, "first mount quests not limited to one race: %s", strings.Join(bad, "; "))
	}
	t.Logf("PASS %d race quests, one race each: %s", len(ok), strings.Join(ok, ", "))
}

// A Tauren talks to Kar Stormsinger, who also gives the copies for the races whose home race is Tauren (Pandaren
// Horde 14, Highmountain 16, Vulpera 21). Only the Tauren quest may be offered, none of 602140-602159 (534da04 /
// 311465c). The Tauren quest being there shows the list was read.
func TestFirstMount_TaurenOnlyOffersOwnQuest(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "firstmount", "quests"}, Runtime: "short", Category: "temucraft/firstmount"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcMount", Level: 10, Race: e2eharness.RaceTauren, Class: e2eharness.ClassWarrior})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	kar := bot.Spawn(t, npcKar, 15*time.Second) // quest relations go by entry, so a copy offers the same quests
	bot.GM(t, ".gm off")
	bot.FlushWorld(t)

	offers := tcwire.QuestOffers(t, bot.World, kar)
	ids := tcwire.QuestIDs(offers)
	t.Logf("Kar Stormsinger offers a Tauren: %v", ids)
	hasOwn := false
	for _, q := range offers {
		if q.ID == questTauren {
			hasOwn = true
		}
		if q.ID >= raceQuestFirst && q.ID <= raceQuestLast {
			e2eharness.Assertf(t, "a Tauren is offered another race's first mount quest %d %q (all: %v)", q.ID, q.Title, ids)
		}
	}
	if !hasOwn {
		e2eharness.Preconditionf(t, "the Tauren first mount quest %d is not offered (offers %v): module SQL missing, or the quest list wasn't read", questTauren, ids)
	}
	t.Logf("PASS a Tauren sees only quest %d of the first mount quests", questTauren)
}
