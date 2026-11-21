//go:build e2e

package reagentbag_test

import (
	"fmt"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

const (
	prefix     = "TPG"
	linenCloth = uint32(2589) // trade goods: fits the reagent bag
	bread      = uint32(4540) // food: does not fit
)

// bagState is what one "RS ... RI ... RE" answer says.
type bagState struct {
	auto        string
	slots, used int
	items       string // the RI parts joined
}

// ask sends one TPG command and reads the bag state the server answers with (every R* command ends with RS, RI..., RE).
func ask(t *testing.T, bot *e2eharness.ScenarioBot, chat *tcwire.Collector, cmd string) (bagState, []string) {
	t.Helper()
	m := chat.Mark()
	tcwire.SendAddon(t, bot.World, bot.Name, prefix, cmd)
	if _, ok := chat.WaitAddon(m, prefix, "RE", tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Assertf(t, "TPG %q: no RE (got %q)", cmd, chat.AddonSince(m, prefix))
	}
	var st bagState
	var errs []string
	for _, line := range chat.AddonSince(m, prefix) {
		switch {
		case strings.HasPrefix(line, "RS "):
			fmt.Sscanf(line, "RS %s %d %d", &st.auto, &st.slots, &st.used)
		case strings.HasPrefix(line, "RI "):
			st.items += line[3:]
		case strings.HasPrefix(line, "ER "):
			errs = append(errs, line[3:])
		}
	}
	t.Logf("TPG %q -> auto=%s slots=%d used=%d items=%q errors=%q", cmd, st.auto, st.slots, st.used, st.items, errs)
	return st, errs
}

func storedCount(t *testing.T, bot *e2eharness.ScenarioBot, entry uint32) int {
	t.Helper()
	var n int
	err := bot.CharDB.QueryRow("SELECT COALESCE(SUM(`count`), 0) FROM `mod_temucraft_reagent_bag` WHERE `guid` = ? AND `item` = ?", tcwire.Low(bot.GUID), entry).Scan(&n)
	if err != nil {
		e2eharness.HarnessFailf(t, "mod_temucraft_reagent_bag: %v", err)
	}
	return n
}

// The reagent bag's addon protocol (prefix TPG, R* commands whispered to yourself in the addon language), the way
// the TemucraftProfGear addon drives it: RG state, RT auto deposit switch, RD one stack in, RW take out, RA deposit
// all, and the ER refusal for an item that isn't a reagent. Each step is checked in the answer, the bags and the
// characters DB.
func TestReagentBag_AddonCommands(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "professions", "addon"}, Runtime: "med", Category: "temucraft/reagentbag"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcReag", Level: 80})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	chat := tcwire.Collect(t, bot.World)

	st, _ := ask(t, bot, chat, "RG")
	if st.slots <= 0 || st.used != 0 {
		e2eharness.Assertf(t, "RG on a new character: slots=%d used=%d, want slots>0 and nothing used", st.slots, st.used)
	}

	// auto deposit off, so the items given below stay in the bags until the test moves them
	if st, _ = ask(t, bot, chat, "RT 0"); st.auto != "0" {
		e2eharness.Assertf(t, "RT 0: auto deposit is %q", st.auto)
	}
	bot.Save(t)
	if v, ok := tcwire.CharSetting(t, bot.CharDB, bot.GUID, "reagentbag"); !ok || v != 0 {
		e2eharness.Assertf(t, "RT 0 not saved (value=%d, row=%v)", v, ok)
	}

	bag, slot := bot.AddItemWait(t, linenCloth, 20)
	st, errs := ask(t, bot, chat, fmt.Sprintf("RD %d %d", bag, slot))
	if len(errs) > 0 || !strings.Contains(st.items, fmt.Sprintf("%d:20;", linenCloth)) || st.used != 1 {
		e2eharness.Assertf(t, "RD: 20 Linen Cloth not in the reagent bag (used=%d items=%q errors=%q)", st.used, st.items, errs)
	}
	if n := bot.InventoryCount(t, linenCloth); n != 0 {
		e2eharness.Assertf(t, "RD: %d Linen Cloth still in the bags", n)
	}
	if n := storedCount(t, bot, linenCloth); n != 20 {
		e2eharness.Assertf(t, "RD: the DB holds %d Linen Cloth, want 20", n)
	}

	st, _ = ask(t, bot, chat, fmt.Sprintf("RW %d 5", linenCloth))
	if !strings.Contains(st.items, fmt.Sprintf("%d:15;", linenCloth)) {
		e2eharness.Assertf(t, "RW 5: bag shows %q, want 15 left", st.items)
	}
	if n := bot.InventoryCount(t, linenCloth); n != 5 {
		e2eharness.Assertf(t, "RW 5: %d Linen Cloth in the bags, want 5", n)
	}

	st, _ = ask(t, bot, chat, "RA")
	if !strings.Contains(st.items, fmt.Sprintf("%d:20;", linenCloth)) {
		e2eharness.Assertf(t, "RA: bag shows %q, want all 20 back", st.items)
	}
	if n := bot.InventoryCount(t, linenCloth); n != 0 {
		e2eharness.Assertf(t, "RA: %d Linen Cloth left in the bags", n)
	}

	bag, slot = bot.AddItemWait(t, bread, 5)
	if _, errs = ask(t, bot, chat, fmt.Sprintf("RD %d %d", bag, slot)); len(errs) == 0 {
		e2eharness.Assertf(t, "RD with food: no ER refusal")
	}
	if n := bot.InventoryCount(t, bread); n != 5 {
		e2eharness.Assertf(t, "RD with food: %d bread in the bags, want 5 (it must not move)", n)
	}
	t.Logf("PASS reagent bag addon commands")
}
