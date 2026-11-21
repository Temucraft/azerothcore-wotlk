//go:build e2e

package courier_test

import (
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

const courierEntry uint32 = 608400 // Temucraft.Courier.Creature

// `.temucraft courier` calls a courier that follows its owner: its gossip opens the owner's bank and mailbox, another
// player gets "works for someone else" and no menu, and a second call inside the cooldown is refused (game masters
// skip the cooldown, so the owner turns GM mode off first).
func TestCourier_BankMailOwnerAndCooldown(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "courier", "gossip", "multi_bot"}, Runtime: "med", Category: "temucraft/courier"})

	bots := e2eharness.NewScenario(t, e2eharness.ScenarioOpts{
		Prefix: "TcCour",
		Bots:   []e2eharness.BotSpec{{Role: "owner", Level: 80}, {Role: "other", Level: 80}},
	})
	owner, other := e2eharness.ByRole(t, bots, "owner"), e2eharness.ByRole(t, bots, "other")
	e2eharness.TeleportAllPad(t, bots, e2eharness.PackagePad(t))
	chat := tcwire.Collect(t, owner.World)
	otherChat := tcwire.Collect(t, other.World)

	tcwire.Command(t, owner.World, chat, ".temucraft courier", "Your courier is here")
	courier := owner.WaitUnit(t, courierEntry, 10*time.Second)

	menu := tcwire.GossipHello(t, owner.World, courier)
	t.Logf("courier menu: %q", menu.Texts())
	bank, okBank := menu.Find("Open my bank.")
	_, okMail := menu.Find("Open my mailbox.")
	if !okBank || !okMail {
		e2eharness.Assertf(t, "courier menu lacks the bank or mailbox line: %q", menu.Texts())
	}

	waitBank, cancelBank := tcwire.ArmPacket(owner.World, tcwire.SmsgShowBank, tcwire.GUIDIs(courier))
	tcwire.GossipSelect(t, owner.World, menu, bank)
	if _, ok := waitBank(tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Assertf(t, "\"Open my bank.\" sent no SMSG_SHOW_BANK")
	}
	cancelBank()

	menu = tcwire.GossipHello(t, owner.World, courier)
	mail, _ := menu.Find("Open my mailbox.")
	waitMail, cancelMail := tcwire.ArmPacket(owner.World, tcwire.SmsgShowMailbox, tcwire.GUIDIs(courier))
	tcwire.GossipSelect(t, owner.World, menu, mail)
	if _, ok := waitMail(tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Assertf(t, "\"Open my mailbox.\" sent no SMSG_SHOW_MAILBOX")
	}
	cancelMail()

	// another player: refused, and no menu
	other.WaitUnit(t, courierEntry, 10*time.Second)
	waitMenu, cancelMenu := tcwire.ArmGossip(other.World, courier)
	m := otherChat.Mark()
	tcwire.SendGossipHello(t, other.World, courier)
	if _, ok := otherChat.WaitSystem(m, "works for someone else", tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Assertf(t, "another player talking to the courier got no refusal (got %q)", otherChat.SystemSince(m))
	}
	if g, ok := waitMenu(time.Second); ok && len(g.Options) > 0 {
		e2eharness.Assertf(t, "another player got the courier's menu: %q", g.Texts())
	}
	cancelMenu()

	// the cooldown
	owner.GM(t, ".gm off")
	owner.FlushWorld(t)
	tcwire.Command(t, owner.World, chat, ".temucraft courier", "on another errand")
	t.Logf("PASS courier bank, mailbox, owner check and cooldown")
}
