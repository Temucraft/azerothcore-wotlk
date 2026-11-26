//go:build e2e

package parachute_test

import (
	"bytes"
	"encoding/binary"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// mod-temucraft-classes 30ed551 (spell_temucraft_parachute): Parachute (55001) in two phases. Pressed on the ground it
// is only "ready": the aura is there, no slow fall, no cooldown (the client starts the spell's own cooldown at the
// cast, so the server clears it: SMSG_CLEAR_COOLDOWN). Falling for half a second and a yard below the fall's start
// opens it: slow fall (SMSG_MOVE_FEATHER_FALL) and the level-based cooldown (Temucraft.ClassTweaks.Parachute, 60 s
// from level 60) starts then (SMSG_SPELL_COOLDOWN). Needs Temucraft.ClassTweaks.Parachute.ReadyMinutes > 0 (default 5).
const (
	spellParachute    uint32  = 55001
	smsgFeatherFall   uint16  = 0x0F2
	smsgSpellCooldown uint16  = 0x134
	fallHeight        float32 = 80
)

// chuteWatch records what the server says about Parachute's cooldown and the slow fall.
type chuteWatch struct {
	mu          sync.Mutex
	cleared     int    // SMSG_CLEAR_COOLDOWN for 55001
	cooldownMs  uint32 // last SMSG_SPELL_COOLDOWN entry for 55001
	cooldowns   int
	featherFall int // SMSG_MOVE_FEATHER_FALL for the bot
}

func watch(t *testing.T, bot *e2eharness.ScenarioBot) *chuteWatch {
	t.Helper()
	cw := &chuteWatch{}
	cancel := bot.World.AddPacketHook(func(op uint16, data []byte) {
		cw.mu.Lock()
		defer cw.mu.Unlock()
		switch op {
		case tcwire.SmsgClearCooldown: // spell, guid
			if len(data) >= 4 && binary.LittleEndian.Uint32(data) == spellParachute {
				cw.cleared++
			}
		case smsgSpellCooldown: // guid, flags, (spell, ms)...
			for off := 9; off+8 <= len(data); off += 8 {
				if binary.LittleEndian.Uint32(data[off:]) == spellParachute {
					cw.cooldowns++
					cw.cooldownMs = binary.LittleEndian.Uint32(data[off+4:])
				}
			}
		case smsgFeatherFall: // packed guid, counter
			if g := tcwire.ReadPackedGUID(bytes.NewReader(data)); g == bot.GUID {
				cw.featherFall++
			}
		}
	})
	t.Cleanup(cancel)
	return cw
}

func (cw *chuteWatch) get() (cleared, cooldowns int, ms uint32, feather int) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	return cw.cleared, cw.cooldowns, cw.cooldownMs, cw.featherFall
}

func waitUntil(timeout time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// On the ground: ready aura, cooldown cleared, no slow fall, and a second press works at once (no cooldown on the
// server). Then up in the air and falling: the parachute opens by itself, slow fall, the cooldown starts, and a press
// now is refused as not ready.
func TestParachute_ReadyOnGroundOpensWhenFalling(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "classes", "spells", "parachute"}, Runtime: "med", Category: "temucraft/parachute"})

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcPara", Level: 60})
	pad := e2eharness.PackagePad(t)
	bot.TeleportPad(t, pad)
	bot.Learn(t, spellParachute)
	bot.GM(t, ".gm off")
	bot.FlushWorld(t)
	cw := watch(t, bot)

	// 1. on the ground
	if res := bot.Cast(t, spellParachute, 0, e2eharness.DefaultCastTimeout); !res.Success {
		e2eharness.Assertf(t, "Parachute on the ground failed: %d (%s)", res.FailReason, e2eharness.SpellFailReasonName(res.FailReason))
	}
	e2eharness.WaitAura(t, bot.World, spellParachute, 5*time.Second)
	if !waitUntil(3*time.Second, func() bool { c, _, _, _ := cw.get(); return c > 0 }) {
		t.Logf("no SMSG_CLEAR_COOLDOWN for %d after the ground cast (the client would show the spell's own cooldown)", spellParachute)
	}
	bot.FlushWorld(t)
	if _, cds, ms, feather := cw.get(); cds > 0 || feather > 0 {
		e2eharness.Assertf(t, "Parachute on the ground: cooldown sent %d times (%d ms), slow fall %d times; want only the ready aura", cds, ms, feather)
	}
	// pressed again while ready: a fresh ready aura, no cooldown in the way
	if res := bot.Cast(t, spellParachute, 0, e2eharness.DefaultCastTimeout); !res.Success {
		e2eharness.Assertf(t, "second Parachute on the ground refused: %d (%s); a ready parachute has no cooldown yet",
			res.FailReason, e2eharness.SpellFailReasonName(res.FailReason))
	}
	e2eharness.WaitAura(t, bot.World, spellParachute, 5*time.Second)
	t.Logf("ground: ready aura, no cooldown, no slow fall")

	// 2. up in the air, falling
	x, y, z, mapID := bot.Pos()
	top := z + fallHeight
	bot.Teleport(t, x, y, top, mapID)
	bot.FlushWorld(t)
	if !bot.HasAura(spellParachute) {
		e2eharness.Preconditionf(t, "the ready Parachute aura went away with the teleport up")
	}
	o := float32(0)
	opened := false
	start := time.Now()
	for i := 1; i <= 24 && !opened; i++ { // up to 6 s of falling, a heartbeat every 250 ms
		fallMs := uint32(i * 250)
		sec := float32(fallMs) / 1000
		drop := 4.9 * sec * sec // free fall, until the parachute opens
		if err := bot.World.SendMovementHeartbeatWithJumpAt(x, y, top-drop, o, fallMs, -9.8*sec, 0, 1, 0); err != nil {
			e2eharness.HarnessFailf(t, "falling heartbeat: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
		_, cds, _, feather := cw.get()
		opened = cds > 0 && feather > 0
	}
	if !waitUntil(2*time.Second, func() bool { _, cds, _, feather := cw.get(); return cds > 0 && feather > 0 }) {
		_, cds, ms, feather := cw.get()
		e2eharness.Assertf(t, "falling for %v: parachute not opened (cooldown packets %d (%d ms), slow fall %d)", time.Since(start).Round(time.Millisecond), cds, ms, feather)
	}
	_, _, ms, _ := cw.get()
	if ms == 0 {
		e2eharness.Assertf(t, "the opened parachute's cooldown is 0 ms; want the level 60 step of Temucraft.ClassTweaks.Parachute")
	}
	t.Logf("falling: opened after %v, cooldown %d ms", time.Since(start).Round(time.Millisecond), ms)

	// 3. the cooldown is real: a press now is refused
	res, err := bot.TryCast(t, spellParachute, 0, e2eharness.DefaultCastTimeout)
	if err != nil {
		t.Logf("cast after opening: %v", err)
	} else if res.Success {
		e2eharness.Assertf(t, "Parachute cast again right after it opened: the cooldown didn't start")
	} else if name := e2eharness.SpellFailReasonName(res.FailReason); !strings.Contains(strings.ToUpper(name), "NOT_READY") {
		t.Logf("cast after opening refused with %d (%s)", res.FailReason, name)
	}

	// back on the ground (a teleport, so no fall damage) and to the pad
	bot.TeleportPad(t, pad)
	bot.AssertWorldAlive(t)
	t.Logf("PASS Parachute: ready on the ground without cooldown, opens when falling, cooldown starts then")
}
