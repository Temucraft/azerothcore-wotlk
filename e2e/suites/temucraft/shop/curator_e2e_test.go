//go:build e2e

package shop_test

import (
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

const (
	curatorEntry uint32 = 608000 // npc_temucraft_shop_curator
	tokenItem    uint32 = 608000 // Temucraft Token (Temucraft.Shop.TokenItem)
	categories          = 8
)

// The Temucraft Curator's gossip: eight "Browse:" categories that open a vendor window, "How do I earn tokens?"
// that tells the balance, and (when the server sells tokens for gold) a gold for token trade that really moves the
// gold and the token. `.temucraft shop summon` brings the curator to the bot, so no world spawn is needed.
func TestShop_CuratorGossip(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "shop", "gossip"}, Runtime: "med", Category: "temucraft/shop"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcShop", Level: 80})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	chat := tcwire.Collect(t, bot.World)

	bot.GM(t, ".temucraft shop summon")
	curator := bot.WaitUnit(t, curatorEntry, 10*time.Second)

	menu := tcwire.GossipHello(t, bot.World, curator)
	t.Logf("curator menu %d: %q", menu.MenuID, menu.Texts())
	if n := menu.Count("Browse:"); n != categories {
		e2eharness.Assertf(t, "curator shows %d Browse: categories, want %d", n, categories)
	}
	earn, ok := menu.Find("How do I earn tokens?")
	if !ok {
		e2eharness.Assertf(t, "curator has no \"How do I earn tokens?\" option")
	}

	// the balance line
	m := chat.Mark()
	tcwire.GossipSelect(t, bot.World, menu, earn)
	if got, ok := chat.WaitSystem(m, "you have 0 token", tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Assertf(t, "earn tokens: no balance line for a new character (got %q)", chat.SystemSince(m))
	} else {
		t.Logf("balance: %q", got)
	}

	// a category opens a vendor window with goods in it
	menu = tcwire.GossipHello(t, bot.World, curator)
	browse, _ := menu.Find("Browse:")
	waitList, cancel := tcwire.ArmPacket(bot.World, tcwire.SmsgListInventory, tcwire.GUIDIs(curator))
	tcwire.GossipSelect(t, bot.World, menu, browse)
	list, ok := waitList(tcwire.DefaultReplyTimeout)
	cancel()
	if !ok {
		e2eharness.Assertf(t, "%q: no vendor window (SMSG_LIST_INVENTORY)", browse.Text)
	}
	if len(list) < 9 || list[8] == 0 {
		e2eharness.Assertf(t, "%q: the vendor window is empty", browse.Text)
	}
	t.Logf("%q: %d goods", tcwire.Plain(browse.Text), list[8])

	// gold for a token, when the server offers it
	menu = tcwire.GossipHello(t, bot.World, curator)
	buy, ok := menu.Find("Buy 1 token for gold.")
	if !ok {
		t.Logf("Temucraft.Shop.GoldPerToken is 0 on this realm: the gold trade is not checked")
		t.Logf("PASS curator menu and vendor categories")
		return
	}
	bot.ModMoney(t, buy.Money+10000)
	bot.FlushWorld(t)
	before := bot.MoneyAfterSave(t)
	tcwire.GossipSelect(t, bot.World, menu, buy)
	deadline := time.Now().Add(tcwire.DefaultReplyTimeout)
	for bot.InventoryCount(t, tokenItem) < 1 {
		if time.Now().After(deadline) {
			e2eharness.Assertf(t, "bought a token for %d copper but have none", buy.Money)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if after := bot.MoneyAfterSave(t); before-after != buy.Money {
		e2eharness.Assertf(t, "the token cost %d copper, want %d", before-after, buy.Money)
	}
	t.Logf("PASS curator menu, vendor categories and gold for a token")
}
