//go:build e2e

package challenges_test

import (
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/client"
	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

const (
	challengeSelfFound = 7     // bit of Self-Found in mod_temucraft_challenges.Challenges
	auraSelfFound      = 95417 // Temucraft.Challenges.AuraFirst (95410) + 7
	refusalWindow      = 3 * time.Second
)

func challengeMask(t *testing.T, bot *e2eharness.ScenarioBot) uint32 {
	t.Helper()
	bot.Save(t)
	var mask uint32
	err := bot.CharDB.QueryRow("SELECT `Challenges` FROM `mod_temucraft_challenges` WHERE `guid` = ?", tcwire.Low(bot.GUID)).Scan(&mask)
	if err != nil {
		return 0
	}
	return mask
}

// trade from `from` to `to` must be refused: `from` gets the Self-Found notification and `to` never sees a trade
// request (CanInitTrade stops it before TRADE_STATUS_BEGIN_TRADE).
func assertTradeRefused(t *testing.T, from, to *e2eharness.ScenarioBot, fromChat *tcwire.Collector) {
	t.Helper()
	begun := make(chan struct{}, 1)
	cancel := to.World.AddTradeStatusHook(func(s client.TradeStatusInfo) {
		if s.Status == client.TradeStatusBeginTrade {
			select {
			case begun <- struct{}{}:
			default:
			}
		}
	})
	defer cancel()
	m := fromChat.Mark()
	from.InitiateTrade(t, to)
	if _, ok := fromChat.WaitNote(m, "don't trade", tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Assertf(t, "%s -> %s trade: no Self-Found notification (got %q)", from.Name, to.Name, fromChat.NotesSince(m))
	}
	select {
	case <-begun:
		e2eharness.Assertf(t, "%s -> %s trade reached the other player", from.Name, to.Name)
	case <-time.After(refusalWindow):
	}
}

// mail from `from` to `to` must be refused with the Self-Found notification, and no mail row is written.
func assertMailRefused(t *testing.T, from, to *e2eharness.ScenarioBot, fromChat *tcwire.Collector) {
	t.Helper()
	m := fromChat.Mark()
	tcwire.SendMail(t, from.World, from.GUID, to.Name, "e2e self-found", "should not arrive", 0)
	if _, ok := fromChat.WaitNote(m, "don't send or receive mail", tcwire.DefaultReplyTimeout); !ok {
		e2eharness.Assertf(t, "%s -> %s mail: no Self-Found notification (got %q)", from.Name, to.Name, fromChat.NotesSince(m))
	}
	var n int
	if err := to.CharDB.QueryRow("SELECT COUNT(*) FROM `mail` WHERE `receiver` = ? AND `subject` = 'e2e self-found'", tcwire.Low(to.GUID)).Scan(&n); err != nil {
		e2eharness.HarnessFailf(t, "mail count: %v", err)
	}
	if n != 0 {
		e2eharness.Assertf(t, "%s -> %s: the refused mail was written (%d rows)", from.Name, to.Name, n)
	}
}

// Self-Found: taken at level 1 with `.temucraft challenge join selffound` (aura and DB row), it blocks trading and
// mail both ways between the Self-Found character and anyone else, and it can't be taken past level 1.
func TestChallenges_SelfFoundRules(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "challenges", "multi_bot"}, Runtime: "med", Category: "temucraft/challenges"})

	bots := e2eharness.NewScenario(t, e2eharness.ScenarioOpts{
		Prefix: "TcSelf",
		Bots:   []e2eharness.BotSpec{{Role: "self", Level: 1}, {Role: "other", Level: 1}},
	})
	self, other := e2eharness.ByRole(t, bots, "self"), e2eharness.ByRole(t, bots, "other")
	e2eharness.TeleportAllPad(t, bots, e2eharness.PackagePad(t))
	selfChat, otherChat := tcwire.Collect(t, self.World), tcwire.Collect(t, other.World)

	tcwire.Command(t, self.World, selfChat, ".temucraft challenge join selffound", "Self-Found taken.")
	e2eharness.WaitAura(t, self.World, auraSelfFound, 5*time.Second)
	if mask := challengeMask(t, self); mask&(1<<challengeSelfFound) == 0 {
		e2eharness.Assertf(t, "Self-Found not saved (Challenges=%#x)", mask)
	}
	tcwire.Command(t, self.World, selfChat, ".temucraft challenge", "[x] Self-Found")

	assertTradeRefused(t, self, other, selfChat)
	assertTradeRefused(t, other, self, otherChat)
	assertMailRefused(t, self, other, selfChat)
	assertMailRefused(t, other, self, otherChat)

	// past level 1 the challenge can't be taken
	other.GM(t, ".levelup 1")
	other.FlushWorld(t)
	tcwire.Command(t, other.World, otherChat, ".temucraft challenge join selffound", "can only be taken at level")
	if mask := challengeMask(t, other); mask&(1<<challengeSelfFound) != 0 {
		e2eharness.Assertf(t, "a level 2 character got Self-Found")
	}
	t.Logf("PASS Self-Found rules")
}
