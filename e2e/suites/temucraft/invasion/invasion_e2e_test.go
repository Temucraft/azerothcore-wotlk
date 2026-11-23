//go:build e2e

package invasion_test

import (
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// Every Elemental Invasion announcement used to crash the worldserver: it went through ChatHandler(nullptr), which
// asserts on its missing session (crashes 023, 024, 026 and 027). It now goes to everyone online. This starts an
// invasion with one player and checks that a second player gets the announcement and the world stays up.
// Needs Temucraft.Events.Invasion.Enable = 1 and Temucraft.Events.Invasion.Announce = 1.
func TestInvasion_AnnouncementReachesEveryone(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "events", "crash"}, Runtime: "short", Category: "temucraft/invasion"})

	bots := e2eharness.NewScenario(t, e2eharness.ScenarioOpts{Prefix: "TcInv", Count: 2, Level: 80})
	e2eharness.TeleportAllPad(t, bots, e2eharness.PackagePad(t))
	admin, other := bots[0], bots[1]
	adminChat := tcwire.Collect(t, admin.World)
	otherChat := tcwire.Collect(t, other.World)

	// whatever happens, end the invasion again (this also schedules the next one)
	t.Cleanup(func() {
		if e2eharness.SessionAlive(admin.Session) {
			_ = admin.World.SendGMCommand(".temucraft invasion stop")
		}
	})

	m := otherChat.Mark()
	tcwire.Command(t, admin.World, adminChat, ".temucraft invasion start", "invasion started")
	got, ok := otherChat.WaitSystem(m, "rifts have torn open", 10*time.Second)
	if !ok {
		e2eharness.Assertf(t, "the other player got no invasion announcement (is Temucraft.Events.Invasion.Announce = 1?); system messages: %q",
			otherChat.SystemSince(m))
	}
	if !strings.Contains(got, "Elemental Invasion") {
		e2eharness.Assertf(t, "the announcement has no [Elemental Invasion] tag: %q", got)
	}
	t.Logf("announcement: %q", got)

	admin.AssertWorldAlive(t)
	other.AssertWorldAlive(t)
	tcwire.Command(t, admin.World, adminChat, ".temucraft invasion stop", "stopped")
	t.Logf("PASS the announcement reached the other player and the world stayed up")
}
