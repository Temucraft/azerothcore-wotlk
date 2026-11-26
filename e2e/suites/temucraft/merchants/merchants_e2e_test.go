//go:build e2e

package merchants_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// mod-temucraft-merchants (mod_temucraft_merchants_crates.sql): the supply quartermasters (601971, 601973) take a
// Sealed Crate (items 602010-602015) in a repeatable quest of the same id, for Merchant's Favor (601700) and
// reputation with 2233 and 2234 (c486e1b).
const (
	npcQuartermaster   uint32 = 601971
	questSealedCrate   uint32 = 602010 // Sealed Apprentice Crate, quest level 5
	itemSealedCrate    uint32 = 602010
	itemMerchantsFavor uint32 = 601700
	currencyFavor      uint32 = 342
)

var crateFactions = []uint32{2233, 2234}

func reputations(t *testing.T, bot *e2eharness.ScenarioBot) map[uint32]int32 {
	t.Helper()
	bot.Save(t)
	out := map[uint32]int32{}
	for _, f := range crateFactions {
		out[f] = tcwire.Reputation(t, bot.CharDB, bot.GUID, f)
	}
	return out
}

// Turning in a Sealed Apprentice Crate at a supply quartermaster: the crate is taken, Merchant's Favor is given, and
// the standing with 2233 or 2234 goes up.
//
// Setup in the order a player gets there: the quest is in the log first (.quest add, INCOMPLETE), then the crate
// comes (.additem), which completes the quest's one objective (RequiredItemId1 = the crate) through the item-added
// quest check. The 2026-11-26 run accepted the quest through CMSG_QUESTGIVER_ACCEPT_QUEST with the crate already in
// the bags and found status NONE: the accept never took, so the oracle never ran. The turn-in (complete + reward) is
// still done through the quest giver packets, as a player does.
func TestCrates_TurnInGivesFavorAndReputation(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "merchants", "quests", "reputation"}, Runtime: "short", Category: "temucraft/merchants"})

	worldDB, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer worldDB.Close()
	var reward uint32
	if err := worldDB.QueryRow("SELECT `RewardItem1` FROM `quest_template` WHERE `ID` = ?", questSealedCrate).Scan(&reward); err != nil || reward != itemMerchantsFavor {
		e2eharness.Preconditionf(t, "quest %d with reward %d not found (reward=%d, %v): is mod-temucraft-merchants' SQL applied?", questSealedCrate, itemMerchantsFavor, reward, err)
	}

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcCrate", Level: 10})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	// the quest before the quartermaster is spawned: .quest add works on the selected player or yourself, and a
	// selected creature makes it fail
	bot.AddQuest(t, questSealedCrate)
	bot.AddItemWait(t, itemSealedCrate, 1)
	bot.FlushWorld(t)
	if status, ok := bot.QuestStatusAfterSave(t, questSealedCrate); !ok || status != e2eharness.QuestStatusComplete {
		e2eharness.Assertf(t, "quest %d not complete after the crate (%d) came into the bags (status=%s, row=%v): its objective "+
			"(RequiredItemId1/RequiredItemCount1 in mod_temucraft_merchants_crates.sql) doesn't count the crate",
			questSealedCrate, itemSealedCrate, e2eharness.QuestStatusName(status), ok)
	}
	qm := bot.Spawn(t, npcQuartermaster, 15*time.Second)
	before := reputations(t, bot)
	favorBefore := bot.InventoryCount(t, itemMerchantsFavor)
	bot.GM(t, ".gm off")
	bot.FlushWorld(t)

	if err := bot.World.QuestgiverHello(qm); err != nil {
		e2eharness.HarnessFailf(t, "questgiver hello: %v", err)
	}
	bot.FlushWorld(t)
	if err := bot.World.QuestgiverCompleteQuest(qm, questSealedCrate); err != nil {
		e2eharness.HarnessFailf(t, "complete %d: %v", questSealedCrate, err)
	}
	bot.FlushWorld(t)
	if err := bot.World.QuestgiverChooseReward(qm, questSealedCrate, 0); err != nil {
		e2eharness.HarnessFailf(t, "choose reward %d: %v", questSealedCrate, err)
	}
	bot.FlushWorld(t)

	after := reputations(t, bot)
	favor := bot.InventoryCount(t, itemMerchantsFavor)
	crates := bot.InventoryCount(t, itemSealedCrate)
	t.Logf("reputation before %v after %v; Merchant's Favor %d -> %d; crates left %d", before, after, favorBefore, favor, crates)
	if crates != 0 {
		e2eharness.Assertf(t, "the Sealed Crate was not taken (%d left)", crates)
	}
	if favor <= favorBefore {
		e2eharness.Assertf(t, "no Merchant's Favor (%d) for the crate (%d before, %d after)", itemMerchantsFavor, favorBefore, favor)
	}
	gained := false
	for _, f := range crateFactions {
		gained = gained || after[f] > before[f]
	}
	if !gained {
		e2eharness.Assertf(t, "no reputation with %v for the crate (before %v, after %v)", crateFactions, before, after)
	}
	t.Logf("PASS crate turned in: Favor and reputation given")
}

// Every currency has its own bit in the player's known currencies (PLAYER_FIELD_KNOWN_CURRENCIES), BitIndex in
// CurrencyTypes.dbc. Merchant's Favor had BitIndex 0 (no bit: never shown in the currency tab), and then a bit that
// another currency used (patch-T fix). The server's overrides in currencytypes_dbc must not share a bit, and the
// Favor needs a real one. The rows of the server's CurrencyTypes.dbc file are not in the database, so a clash with
// a stock currency can't be seen from here.
func TestCurrency_OverridesHaveOwnBits(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "merchants", "dbc"}, Runtime: "short", Category: "temucraft/merchants"})

	db, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer db.Close()
	if !tcwire.TableExists(t, db, "currencytypes_dbc") {
		t.Skip("this world database has no currencytypes_dbc table")
	}

	rows, err := db.Query("SELECT `BitIndex`, GROUP_CONCAT(`ID` ORDER BY `ID`) FROM `currencytypes_dbc` GROUP BY `BitIndex` HAVING COUNT(*) > 1")
	if err != nil {
		e2eharness.HarnessFailf(t, "currencytypes_dbc: %v", err)
	}
	var clashes []string
	for rows.Next() {
		var bit int
		var ids string
		if rows.Scan(&bit, &ids) == nil {
			clashes = append(clashes, fmt.Sprintf("bit %d: %s", bit, ids))
		}
	}
	rows.Close()
	if len(clashes) > 0 {
		e2eharness.Assertf(t, "currencytypes_dbc rows share a BitIndex: %s", strings.Join(clashes, "; "))
	}

	var favorBit int
	switch err := db.QueryRow("SELECT `BitIndex` FROM `currencytypes_dbc` WHERE `ID` = ?", currencyFavor).Scan(&favorBit); {
	case err != nil:
		t.Logf("no currencytypes_dbc row for Merchant's Favor (%d): %v", currencyFavor, err)
	case favorBit < 1 || favorBit > 64:
		e2eharness.Assertf(t, "Merchant's Favor (%d) has BitIndex %d; a known-currency bit is 1-64", currencyFavor, favorBit)
	default:
		t.Logf("Merchant's Favor uses bit %d", favorBit)
	}
	t.Logf("PASS no two currencytypes_dbc rows share a bit")
}
