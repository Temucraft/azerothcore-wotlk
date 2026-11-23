//go:build e2e

package channels_test

import (
	"fmt"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// Crash 016: a playerbot logging out left a chat channel and the channel sent to a member whose player pointer was
// junk. Channel members are now looked up instead of being added on the fly, a member with no player is skipped, and
// each channel has a lock (map threads join and leave the local channels on zone changes). This walks the paths
// around it: ownership handed over, a moderator change for someone who isn't in the channel, the owner dropping
// without a logout, chat afterwards, and zone changes in and out of cities on two continents.
func TestChannels_OwnerDropsAndNonMemberModerator(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "channels", "crash"}, Runtime: "short", Category: "temucraft/channels"})

	bots := e2eharness.NewScenario(t, e2eharness.ScenarioOpts{Prefix: "TcChan", Count: 3, Level: 10})
	e2eharness.TeleportAllPad(t, bots, e2eharness.PackagePad(t))
	a, b, c := bots[0], bots[1], bots[2]
	la, lb, lc := tcwire.WatchChannels(t, a.World), tcwire.WatchChannels(t, b.World), tcwire.WatchChannels(t, c.World)
	name := fmt.Sprintf("tcchan%d", time.Now().UnixNano()%1000000)

	// a creates the channel (and owns it), b and c join
	for _, j := range []struct {
		bot *e2eharness.ScenarioBot
		log *tcwire.ChannelLog
	}{{a, la}, {b, lb}, {c, lc}} {
		m := j.log.Len()
		tcwire.JoinChannel(t, j.bot.World, name)
		if !j.log.WaitNotify(m, name, tcwire.NotifyYouJoined, 5*time.Second) {
			e2eharness.HarnessFailf(t, "%s didn't join %s", j.bot.Name, name)
		}
	}

	// a hands the channel to b
	m := lc.Len()
	tcwire.ChannelSetOwner(t, a.World, name, b.Name)
	if !lc.WaitNotify(m, name, tcwire.NotifyOwnerChanged, 5*time.Second) {
		t.Logf("no owner-changed notice reached %s (announcements may be off for this channel)", c.Name)
	}

	// c leaves; b (the owner) then tries to make c a moderator: c isn't in the channel any more, so b must get
	// "player not found" and nothing may be added for c
	m = lc.Len()
	tcwire.LeaveChannel(t, c.World, name)
	if !lc.WaitNotify(m, name, tcwire.NotifyYouLeft, 5*time.Second) {
		e2eharness.HarnessFailf(t, "%s didn't leave %s", c.Name, name)
	}
	m = lb.Len()
	tcwire.ChannelModerator(t, b.World, name, c.Name)
	if !lb.WaitNotify(m, name, tcwire.NotifyPlayerNotFound, 5*time.Second) && !lb.WaitNotify(m, name, tcwire.NotifyNotMember, time.Second) {
		e2eharness.Assertf(t, "moderator change for a non-member: no 'not found' answer; notices: %+v", lb.Since(m))
	}

	// the owner drops without a logout (Player::CleanupChannels on the server, as for a bot logout)
	b.HardDisconnect(t)
	time.Sleep(3 * time.Second)

	// the one left still chats in the channel and hears itself
	m = la.Len()
	tcwire.ChannelSay(t, a.World, name, "still here")
	if !la.WaitChat(m, name, "still here", 5*time.Second) {
		e2eharness.Assertf(t, "after the owner dropped, %s's channel message didn't come back; events: %+v", a.Name, la.Since(m))
	}

	// zone changes join and leave the local channels (General, Trade) on the map threads
	for i := 0; i < 3; i++ {
		c.TeleNamed(t, "Ironforge")
		a.TeleNamed(t, "Darnassus")
		c.TeleNamed(t, "Darnassus")
		a.TeleNamed(t, "Ironforge")
	}

	a.AssertWorldAlive(t)
	c.AssertWorldAlive(t)
	t.Logf("PASS ownership handover, non-member moderator refused, owner drop, chat after it, zone changes; world alive")
}
