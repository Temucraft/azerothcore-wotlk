//go:build e2e

package classes_test

import (
	"encoding/binary"
	"sync/atomic"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

const (
	spellParachute         uint32 = 55001 // the trainer Parachute of mod-temucraft-classes (also the Flexweave Underlay's spell)
	skillEngineering       uint32 = 202
	spellEarthmotherEmbrac uint32 = 100176 // the Tauren racial heal (Temucraft.ClassTweaks.RacialHeals.SpellFirst + 4)
)

// a9b7a06: the core's conditions row (17, 55001, skill 202 >= 350) belongs to the Flexweave Underlay cloak; it made
// the trainer Parachute fail for everyone without 350 Engineering. No conditions row may tie 55001 to Engineering.
func TestParachute_NoEngineeringCondition(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "classes", "spells"}, Runtime: "short", Category: "temucraft/classes"})

	db, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM `conditions` WHERE `SourceTypeOrReferenceId` = 17 AND `SourceEntry` = ? "+
		"AND `ConditionTypeOrReference` = 7 AND `ConditionValue1` = ?", spellParachute, skillEngineering).Scan(&n); err != nil {
		e2eharness.HarnessFailf(t, "conditions: %v", err)
	}
	if n > 0 {
		e2eharness.Assertf(t, "%d conditions rows still require Engineering (%d) to cast %d", n, skillEngineering, spellParachute)
	}
	t.Logf("PASS no Engineering condition on %d", spellParachute)
}

// A level 10 character without Engineering casts Parachute: the cast goes off (SMSG_SPELL_GO) instead of failing.
func TestParachute_CastWithoutEngineering(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"short", "temucraft", "classes", "spells"}, Runtime: "short", Category: "temucraft/classes"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcChute", Level: 10})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	bot.Learn(t, spellParachute)
	bot.GM(t, ".gm off")
	bot.FlushWorld(t)

	res := bot.Cast(t, spellParachute, 0, e2eharness.DefaultCastTimeout)
	if !res.Success {
		e2eharness.Assertf(t, "Parachute (%d) failed without Engineering: reason %d (%s)", spellParachute, res.FailReason, e2eharness.SpellFailReasonName(res.FailReason))
	}
	t.Logf("PASS Parachute cast without Engineering (aura on: %v)", bot.HasAura(spellParachute))
}

// 72c6442: the racial heals (100172-100187) carry the "Racial" rank but no skill line, so mod-temucraft-classes'
// Heritage took them away at every login and ClassChanges taught them again: "You have learned Earthmother's
// Embrace" on each login. A Tauren logs in again twice on a client hooked before it enters the world: neither login
// may remove (SMSG_REMOVED_SPELL) or teach (SMSG_LEARNED_SPELL, the client's "You have learned") 100176, and the
// spell is still known.
func TestRacialHeal_NoRelearnOnLogin(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "classes", "racials", "relog"}, Runtime: "med", Category: "temucraft/classes"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcHeal", Level: 10, Race: e2eharness.RaceTauren, Class: e2eharness.ClassWarrior})
	bot.TeleportPad(t, e2eharness.PackagePad(t))
	bot.FlushWorld(t)
	if !bot.World.KnowsSpell(spellEarthmotherEmbrac) {
		e2eharness.Preconditionf(t, "a new Tauren doesn't know Earthmother's Embrace (%d): mod-temucraft-classes off, or Temucraft.ClassChanges.RacialHeals = 0", spellEarthmotherEmbrac)
	}
	bot.Save(t)
	user, guid := bot.User, bot.GUID
	tcwire.Logout(t, bot.World)

	for login := 2; login <= 3; login++ {
		var learned, removed atomic.Int32
		w := tcwire.LoginObserved(t, user, guid, e2eharness.RaceTauren, func(op uint16, data []byte) {
			if len(data) < 4 || binary.LittleEndian.Uint32(data) != spellEarthmotherEmbrac {
				return
			}
			switch op {
			case tcwire.SmsgLearnedSpell:
				learned.Add(1)
			case tcwire.SmsgRemovedSpell:
				removed.Add(1)
			}
		})
		e2eharness.FlushWorld(t, w) // the OnLogin scripts ran before this command
		if learned.Load() > 0 || removed.Load() > 0 {
			e2eharness.Assertf(t, "login %d: Earthmother's Embrace (%d) removed %d times and learned %d times; it should just stay",
				login, spellEarthmotherEmbrac, removed.Load(), learned.Load())
		}
		if !w.KnowsSpell(spellEarthmotherEmbrac) {
			e2eharness.Assertf(t, "login %d: Earthmother's Embrace (%d) is gone", login, spellEarthmotherEmbrac)
		}
		t.Logf("login %d: %d not removed, not learned again, still known", login, spellEarthmotherEmbrac)
		tcwire.Logout(t, w)
	}
	t.Logf("PASS Earthmother's Embrace stays through two more logins without a relearn")
}
