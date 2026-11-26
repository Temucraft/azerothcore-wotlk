//go:build e2e

package startzone_test

import (
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

// mod-temucraft-starting-zone 5a1b4cd: a new character is asked once where to begin, a few seconds after it enters the
// world. A client that says "TSZ HELLO" (the Temucraft client's TemucraftStartingZone.lua) gets the "Embark on your
// Journey" window as addon messages (BEGIN~n, Z~... per zone, END); any other client gets the gossip menu (608500)
// after FIRST_ASK_MS + HELLO_WAIT_MS (4 + 4 s). Once a zone is picked (staying counts) the character is marked in
// mod_temucraft_starting_zone_chosen and is never asked again, also not after a relog.
//
// The harness's own first login runs before a hook can be armed, so each test asks again with
// `.temucraft startzone reset` (the GM command marks the character pending and clears the choice) and logs in on a
// client hooked before it enters the world (tcwire.LoginObserved). Needs Temucraft.StartingZone.Enable = 1,
// MaxLevel >= 1 and Enforce = 1 (the defaults).
const (
	startMenu   uint32 = 608500
	prefix             = "TSZ"
	askTimeout         = 20 * time.Second // FIRST_ASK_MS + HELLO_WAIT_MS + the login itself
	quietPeriod        = 14 * time.Second // longer than FIRST_ASK_MS + HELLO_WAIT_MS: nothing may come
)

// startWatch records the starting zone gossip menus and TSZ addon lines one client receives.
type startWatch struct {
	mu     sync.Mutex
	menus  []tcwire.GossipMenu
	addon  []string // TSZ bodies, prefix removed
	system []string
}

func (s *startWatch) hook(op uint16, data []byte) {
	switch {
	case op == tcwire.SmsgGossipMessage:
		if g, ok := tcwire.ParseGossip(data); ok && g.MenuID == startMenu {
			s.mu.Lock()
			s.menus = append(s.menus, g)
			s.mu.Unlock()
		}
	case tcwire.IsChatOpcode(op):
		c, ok := tcwire.ParseChatOp(op, data)
		if !ok {
			return
		}
		s.mu.Lock()
		switch {
		case c.Lang == tcwire.LangAddon && strings.HasPrefix(c.Text, prefix+"\t"):
			s.addon = append(s.addon, c.Text[len(prefix)+1:])
		case c.Type == tcwire.ChatMsgSystem:
			s.system = append(s.system, tcwire.Plain(c.Text))
		}
		s.mu.Unlock()
	}
}

func (s *startWatch) snapshot() (menus []tcwire.GossipMenu, addon, system []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tcwire.GossipMenu(nil), s.menus...), append([]string(nil), s.addon...), append([]string(nil), s.system...)
}

func waitUntil(timeout time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true
}

// chosenZone reads the character's row in mod_temucraft_starting_zone_chosen (and whether it is still pending).
func chosenZone(t *testing.T, bot *e2eharness.ScenarioBot) (zone uint32, chosen, pending bool) {
	t.Helper()
	low := tcwire.Low(bot.GUID)
	err := bot.CharDB.QueryRow("SELECT `zone` FROM `mod_temucraft_starting_zone_chosen` WHERE `guid` = ?", low).Scan(&zone)
	chosen = err == nil
	var one int
	pending = bot.CharDB.QueryRow("SELECT 1 FROM `mod_temucraft_starting_zone_pending` WHERE `guid` = ?", low).Scan(&one) == nil
	return zone, chosen, pending
}

// newStarter makes a level 1 character in its own starting zone, asks for the starting zone again and logs it out,
// ready for an observed login.
func newStarter(t *testing.T, prefixName string, race, class uint8) (*e2eharness.ScenarioBot, string, uint64) {
	t.Helper()
	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: prefixName, Race: race, Class: class})
	if !tcwire.TableExists(t, bot.CharDB, "mod_temucraft_starting_zone_chosen") {
		e2eharness.Preconditionf(t, "no mod_temucraft_starting_zone_chosen table: mod-temucraft-starting-zone's characters SQL isn't applied")
	}
	chat := tcwire.Collect(t, bot.World)
	tcwire.Command(t, bot.World, chat, ".temucraft startzone reset", "will be asked for a starting zone again")
	if _, chosen, pending := chosenZone(t, bot); chosen || !pending {
		e2eharness.Assertf(t, "after .temucraft startzone reset: chosen=%v pending=%v, want not chosen and pending", chosen, pending)
	}
	bot.Save(t)
	user, guid := bot.User, bot.GUID
	tcwire.Logout(t, bot.World)
	return bot, user, guid
}

// An old client (no HELLO): the gossip menu comes, its first line is "Stay in <the zone it stands in>" (Northshire for
// a new Human), staying is a choice (no teleport, "You begin in <zone>"), and after a relog nothing is offered again.
func TestStartZone_GossipStayIsFinal(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "startzone", "gossip", "relog"}, Runtime: "med", Category: "temucraft/startzone"})

	bot, user, guid := newStarter(t, "TcSzGo", e2eharness.RaceHuman, e2eharness.ClassWarrior)

	watch := &startWatch{}
	w := tcwire.LoginObserved(t, user, guid, e2eharness.RaceHuman, watch.hook)
	if !waitUntil(askTimeout, func() bool { m, _, _ := watch.snapshot(); return len(m) > 0 }) {
		_, addon, _ := watch.snapshot()
		e2eharness.Assertf(t, "no starting zone gossip menu (%d) within %v of a login without HELLO (TSZ lines: %q)", startMenu, askTimeout, addon)
	}
	menus, addon, _ := watch.snapshot()
	if len(addon) > 0 {
		e2eharness.Assertf(t, "a client that never said HELLO got the TSZ window: %q", addon)
	}
	menu := menus[0]
	t.Logf("starting zone menu: %q", menu.Texts())
	if len(menu.Options) < 2 {
		e2eharness.Assertf(t, "the menu has %d lines, want staying plus the other Alliance zones: %q", len(menu.Options), menu.Texts())
	}
	stay := menu.Options[0]
	if !strings.HasPrefix(tcwire.Plain(stay.Text), "Stay in ") {
		e2eharness.Assertf(t, "first line %q, want \"Stay in <zone>\" for the zone the character stands in", tcwire.Plain(stay.Text))
	}
	here := strings.TrimPrefix(tcwire.Plain(stay.Text), "Stay in ")
	if here != "Northshire" {
		t.Logf("note: the new Human stands in %q, not Northshire (moved by the harness?)", here)
	}
	if stay.BoxText != "" {
		t.Logf("note: the stay line has a box %q (it moves nobody)", stay.BoxText)
	}
	for _, o := range menu.Options[1:] {
		if !strings.HasPrefix(tcwire.Plain(o.Text), "Begin in ") {
			e2eharness.Assertf(t, "line %q: every other line should be \"Begin in <zone>\"", tcwire.Plain(o.Text))
		}
	}

	_, _, sysBefore := watch.snapshot()
	x0, y0, _, _, map0 := w.Position()
	tcwire.GossipSelect(t, w, menu, stay)
	if !waitUntil(tcwire.DefaultReplyTimeout, func() bool {
		_, _, sys := watch.snapshot()
		for _, s := range sys[len(sysBefore):] {
			if strings.Contains(s, "You begin in "+here) {
				return true
			}
		}
		return false
	}) {
		_, _, sys := watch.snapshot()
		e2eharness.Assertf(t, "picking %q: no \"You begin in %s\" (got %q)", tcwire.Plain(stay.Text), here, sys[len(sysBefore):])
	}
	e2eharness.FlushWorld(t, w)
	if x, y, _, _, mapID := w.Position(); mapID != map0 || e2eharness.Distance3D(x, y, 0, x0, y0, 0) > 20 {
		e2eharness.Assertf(t, "staying moved the character from map %d (%.0f, %.0f) to map %d (%.0f, %.0f)", map0, x0, y0, mapID, x, y)
	}
	zone, chosen, pending := chosenZone(t, bot)
	if !chosen || pending {
		e2eharness.Assertf(t, "after staying: chosen=%v (zone %d) pending=%v, want chosen and not pending", chosen, zone, pending)
	}
	t.Logf("stayed: chosen zone %d", zone)
	tcwire.Logout(t, w)

	// a relog: never asked again
	again := &startWatch{}
	w = tcwire.LoginObserved(t, user, guid, e2eharness.RaceHuman, again.hook)
	time.Sleep(quietPeriod)
	if menus, addon, _ := again.snapshot(); len(menus) > 0 || len(addon) > 0 {
		e2eharness.Assertf(t, "asked again after a choice and a relog: %d menus, TSZ %q", len(menus), addon)
	}
	tcwire.Logout(t, w)
	t.Logf("PASS gossip fallback: stay is a choice, not asked again after a relog")
}

// The Temucraft client (HELLO right after entering the world): the window comes as TSZ lines and no gossip menu; a
// pick of another zone teleports there ("DONE", "You begin in <zone>", the hearthstone is set), and after a relog
// with HELLO again neither the window nor the menu comes.
func TestStartZone_WindowPickIsFinal(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "startzone", "addon", "relog"}, Runtime: "med", Category: "temucraft/startzone"})

	bot, user, guid := newStarter(t, "TcSzWin", e2eharness.RaceHuman, e2eharness.ClassMage)
	name := bot.Name

	watch := &startWatch{}
	w := tcwire.LoginObserved(t, user, guid, e2eharness.RaceHuman, watch.hook)
	tcwire.SendAddon(t, w, name, prefix, "HELLO")
	if !waitUntil(askTimeout, func() bool {
		_, addon, _ := watch.snapshot()
		for _, a := range addon {
			if a == "END" {
				return true
			}
		}
		return false
	}) {
		menus, addon, _ := watch.snapshot()
		e2eharness.Assertf(t, "no TSZ window (BEGIN ... END) within %v of HELLO (TSZ %q, %d gossip menus)", askTimeout, addon, len(menus))
	}
	menus, addon, _ := watch.snapshot()
	if len(menus) > 0 {
		e2eharness.Assertf(t, "the client said HELLO but also got the gossip menu: %q", menus[0].Texts())
	}
	// Z~<index>~<here>~<map>~<zone>~<area>~<x>~<y>~<name>
	type zoneLine struct {
		index, name string
		here        bool
		mapID       string
	}
	var zones []zoneLine
	for _, a := range addon {
		f := strings.Split(a, "~")
		if f[0] == "Z" && len(f) >= 9 {
			zones = append(zones, zoneLine{index: f[1], here: f[2] == "1", mapID: f[3], name: f[8]})
		}
	}
	t.Logf("window: %q", addon)
	if len(zones) < 2 || !zones[0].here {
		e2eharness.Assertf(t, "window zones %+v: want the current zone first (here=1) and at least one other", zones)
	}
	var pick *zoneLine
	for i := range zones {
		if !zones[i].here {
			pick = &zones[i]
			break
		}
	}
	if pick == nil {
		e2eharness.Assertf(t, "no other zone to pick in %+v", zones)
	}

	_, before, sysBefore := watch.snapshot()
	tcwire.SendAddon(t, w, name, prefix, "PICK~"+pick.index)
	want := "You begin in " + pick.name
	if !waitUntil(15*time.Second, func() bool {
		_, _, sys := watch.snapshot()
		for _, s := range sys[len(sysBefore):] {
			if strings.Contains(s, want) {
				return true
			}
		}
		return false
	}) {
		_, _, sys := watch.snapshot()
		e2eharness.Assertf(t, "PICK~%s: no %q (got %q)", pick.index, want, sys[len(sysBefore):])
	}
	_, after, _ := watch.snapshot()
	done := false
	for _, a := range after[len(before):] {
		done = done || a == "DONE"
	}
	if !done {
		e2eharness.Assertf(t, "PICK~%s: no TSZ DONE (got %q)", pick.index, after[len(before):])
	}
	if err := w.WaitForSessionPhase(client.PhaseInWorld, 30*time.Second); err != nil {
		t.Logf("waiting in world after the teleport: %v", err)
	}
	e2eharness.FlushWorld(t, w)
	zone, chosen, pending := chosenZone(t, bot)
	if !chosen || pending {
		e2eharness.Assertf(t, "after the pick: chosen=%v (zone %d) pending=%v, want chosen and not pending", chosen, zone, pending)
	}
	var homeMap uint32
	if err := bot.CharDB.QueryRow("SELECT `mapId` FROM `character_homebind` WHERE `guid` = ?", tcwire.Low(guid)).Scan(&homeMap); err == nil {
		t.Logf("hearthstone set to map %d (picked %s on map %s)", homeMap, pick.name, pick.mapID)
	}
	tcwire.Logout(t, w)

	again := &startWatch{}
	w = tcwire.LoginObserved(t, user, guid, e2eharness.RaceHuman, again.hook)
	tcwire.SendAddon(t, w, name, prefix, "HELLO")
	time.Sleep(quietPeriod)
	if menus, addon, _ := again.snapshot(); len(menus) > 0 || len(addon) > 0 {
		e2eharness.Assertf(t, "asked again after a pick and a relog: %d menus, TSZ %q", len(menus), addon)
	}
	tcwire.Logout(t, w)
	t.Logf("PASS window: %q picked, not asked again after a relog", pick.name)
}
