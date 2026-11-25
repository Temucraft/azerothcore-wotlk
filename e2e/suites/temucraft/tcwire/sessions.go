//go:build e2e

package tcwire

import (
	"strings"
	"testing"
	"time"

	"github.com/azerothcore/AzerothGhost/client"
	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
)

// AsPlayer turns the bot's account into a player account (security 0, the "GM Curated Player" role 1010 on
// Temucraft) and logs the character in again, since the session reads its security and permissions at login. Do
// every GM setup (teleport, items, spawns) before this: the new session can't use GM commands any more. The account
// gets GM level 3 back at test end.
func AsPlayer(t *testing.T, b *e2eharness.ScenarioBot) {
	t.Helper()
	if err := e2eharness.SetGM(b.AuthDB, b.User, 0); err != nil {
		e2eharness.HarnessFailf(t, "set account %s to security 0: %v", b.User, err)
	}
	authDB, user := b.AuthDB, b.User
	t.Cleanup(func() { _ = e2eharness.SetGM(authDB, user, 3) })
	b.Relog(t)
	t.Logf("%s is a player account (security 0) now", b.User)
}

// Logout logs a world client out and closes it.
func Logout(t *testing.T, w *client.WorldClient) {
	t.Helper()
	if w == nil {
		return
	}
	_ = w.SendLogout()
	if err := w.WaitForLogout(30 * time.Second); err != nil {
		t.Logf("logout wait: %v (closing anyway)", err)
	}
	w.Close()
}

// LoginObserved logs an existing character in on a world client of its own, with hook installed before the
// character enters the world. The harness's Relog can only hook the new session after the login, so it misses what
// the server sends while it runs the OnLogin scripts (learned and removed spells, for example). Log the bot's own
// session out first (Logout(t, bot.World)). The client is closed at test end; FlushWorld on it waits until the
// login scripts have run.
func LoginObserved(t *testing.T, user string, charGUID uint64, race uint8, hook func(opcode uint16, data []byte)) *client.WorldClient {
	t.Helper()
	auth := client.NewAuthClient(user, e2eharness.DefaultPassword)
	realms, err := auth.Authenticate(e2eharness.AuthAddr)
	if err != nil || len(realms) == 0 {
		e2eharness.HarnessFailf(t, "auth %s: %v (realms=%d)", user, err, len(realms))
	}
	w := client.NewWorldClient(strings.ToUpper(user), append([]byte(nil), auth.SessionKey()...), func(string, ...interface{}) {})
	w.SetLogLevel(client.LogWarn)
	w.SetCharRace(race)
	cancel := w.AddPacketHook(hook)
	t.Cleanup(func() { cancel(); w.Close() })

	chars := make(chan []client.CharEnumEntry, 1)
	w.OnCharList = func(list []client.CharEnumEntry) {
		select {
		case chars <- append([]client.CharEnumEntry(nil), list...):
		default:
		}
	}
	if err := w.Connect(realms[0].Address); err != nil {
		e2eharness.HarnessFailf(t, "connect world %s: %v", realms[0].Address, err)
	}
	go func() { _ = w.Run() }()
	if err := w.WaitForSessionPhase(client.PhaseAuthed, 20*time.Second); err != nil {
		e2eharness.HarnessFailf(t, "world auth %s: %v", user, err)
	}
	_ = w.SendReadyForAccountDataTimes()
	_ = w.SendRealmSplit()
	if err := w.RequestCharList(); err != nil {
		e2eharness.HarnessFailf(t, "char enum: %v", err)
	}
	select {
	case list := <-chars:
		found := false
		for _, c := range list {
			found = found || c.GUID == charGUID
		}
		if !found {
			e2eharness.HarnessFailf(t, "character %#x is not on account %s", charGUID, user)
		}
	case <-time.After(15 * time.Second):
		e2eharness.HarnessFailf(t, "no SMSG_CHAR_ENUM for %s", user)
	}
	if err := w.LoginCharacter(charGUID); err != nil {
		e2eharness.HarnessFailf(t, "login %#x: %v", charGUID, err)
	}
	if err := w.WaitForLogin(60 * time.Second); err != nil {
		e2eharness.HarnessFailf(t, "wait login %#x: %v", charGUID, err)
	}
	return w
}
