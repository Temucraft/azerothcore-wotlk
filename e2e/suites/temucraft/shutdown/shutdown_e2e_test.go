//go:build e2e

package shutdown_test

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
)

// A clean stop with playerbots online (crashes at shutdown in Channel::SendToAll, 2026-11-24/25; core 7ffa9e215 and
// mod-playerbots 7eac4fe): `.server shutdown` while a real player and random bots are in the world must end without a
// crash dump, and playerbots' OnShutdown must first save the real players ("Saved N players before the bots log out"
// in Playerbots.log, N >= 1 with this test's character online), then log the bots out.
//
// NEEDS AN EXCLUSIVE REALM: it stops the worldserver. It runs only with E2E_SHUTDOWN_OK=1, and needs the paths the
// worldserver writes to on this machine:
//
//	E2E_PLAYERBOTS_LOG  Playerbots.log of the worldserver (required)
//	E2E_CRASH_DIR       the folder crash dumps / gdb backtraces go to (optional: without it the crash check is skipped)
//
// After it, start the worldserver again (or let the restarter do it) before running other suites.
var savedLine = regexp.MustCompile(`Saved (\d+) players before the bots log out`)

func TestShutdown_CleanWithBotsOnline(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"long", "temucraft", "shutdown", "exclusive", "playerbots"}, Runtime: "long", Category: "temucraft/shutdown"})
	if os.Getenv("E2E_SHUTDOWN_OK") != "1" {
		t.Skip("stops the worldserver: set E2E_SHUTDOWN_OK=1 on an exclusive realm (and E2E_PLAYERBOTS_LOG)")
	}
	logPath := os.Getenv("E2E_PLAYERBOTS_LOG")
	if logPath == "" {
		e2eharness.Preconditionf(t, "E2E_PLAYERBOTS_LOG is not set (the worldserver's Playerbots.log)")
	}
	crashDir := os.Getenv("E2E_CRASH_DIR")

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcStop", Level: 10})
	var others int
	if err := bot.CharDB.QueryRow("SELECT COUNT(*) FROM `characters` WHERE `online` = 1 AND `guid` <> ?", uint32(bot.GUID&0xFFFFFFFF)).Scan(&others); err != nil {
		e2eharness.HarnessFailf(t, "online characters: %v", err)
	}
	if others == 0 {
		e2eharness.Preconditionf(t, "no other character is online: start the random playerbots (AiPlayerbot.RandomBotAutologin) first")
	}
	t.Logf("%d other characters online (bots)", others)

	logStart := int64(0)
	if st, err := os.Stat(logPath); err == nil {
		logStart = st.Size()
	}
	started := time.Now()
	bot.Save(t)

	bot.GM(t, ".server shutdown 5")
	// the server closes the socket when it stops; wait for the log line instead of the socket
	var saved int
	deadline := time.Now().Add(3 * time.Minute)
	for saved == 0 {
		if time.Now().After(deadline) {
			e2eharness.Assertf(t, "no \"Saved N players before the bots log out\" in %s within 3 minutes of .server shutdown", logPath)
		}
		time.Sleep(2 * time.Second)
		saved = readSaved(t, logPath, logStart)
	}
	if saved < 1 {
		e2eharness.Assertf(t, "Playerbots.log says %d players were saved; this test's character was online", saved)
	}
	t.Logf("Playerbots.log: saved %d players before the bots logged out", saved)

	// give the process time to finish (or crash) after the bots logged out
	time.Sleep(30 * time.Second)
	if crashDir == "" {
		t.Logf("E2E_CRASH_DIR not set: crash dump check skipped")
	} else {
		entries, err := os.ReadDir(crashDir)
		if err != nil {
			e2eharness.HarnessFailf(t, "read %s: %v", crashDir, err)
		}
		for _, e := range entries {
			if info, err := e.Info(); err == nil && info.ModTime().After(started) {
				e2eharness.Assertf(t, "a crash file was written during the shutdown: %s", filepath.Join(crashDir, e.Name()))
			}
		}
	}
	t.Logf("PASS clean shutdown with %d bots online: players saved first, no crash file", others)
}

// readSaved returns N of the last "Saved N players ..." line written after offset (-1 = none yet).
func readSaved(t *testing.T, path string, offset int64) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() < offset {
		offset = 0 // the log was rotated by the restart
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return 0
	}
	found := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if g := savedLine.FindStringSubmatch(sc.Text()); g != nil {
			n, _ := strconv.Atoi(g[1])
			found = n
			if n == 0 {
				found = -1
			}
		}
	}
	if found == -1 {
		return -1
	}
	return found
}
