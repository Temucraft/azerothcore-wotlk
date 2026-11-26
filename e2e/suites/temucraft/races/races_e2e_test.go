//go:build e2e

package races_test

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/client"
	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
	"github.com/azerothcore/azerothcore-wotlk/e2e/suites/temucraft/tcwire"
)

// Character creation for every Temucraft race (1-30) and class (1-9, 11): the create is accepted (SMSG_CHAR_CREATE
// success), the character starts where playercreateinfo puts it (death knights in Acherus, map 609), it has
// equipped gear, and the races whose racial line was missing (the SkillRaceClassInfo rows of 2026-11-26: Pandaren
// 13, Nightborne 15, Highmountain 16, Zandalari 19, Kul Tiran 20, Vulpera 21, Mechagnome 24, Earthen 27 and 28)
// know their racial spells after logging in. The expected spells per race and class are testdata/racials.tsv (from
// _dev/reference/race-class-racials-2026-11-26.tsv).
//
// TestRaces_SampleRaceClass makes one character per race (30, three of them death knights) and is the one in the
// regression set; TestRaces_EveryRaceClass makes all 300 (tag "full"; about 20 minutes). One account per race (the
// realm allows 10 characters per account, one death knight); the characters are deleted at the end.
var (
	classes     = []uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 11}
	racialFixed = map[uint8]bool{13: true, 15: true, 16: true, 19: true, 20: true, 21: true, 24: true, 27: true, 28: true}
)

const (
	classDeathKnight  uint8  = 6
	mapAcherus        uint32 = 609
	createSuccess     byte   = 0x2F
	createLevelNeeded byte   = 0x3B // CHAR_CREATE_LEVEL_REQUIREMENT: a death knight needs a level 55 character
)

var createCodes = map[byte]string{0x2E: "IN_PROGRESS", 0x2F: "SUCCESS", 0x30: "ERROR", 0x31: "FAILED", 0x32: "NAME_IN_USE",
	0x33: "DISABLED", 0x34: "PVP_TEAMS_VIOLATION", 0x35: "SERVER_LIMIT", 0x36: "ACCOUNT_LIMIT", 0x39: "EXPANSION",
	0x3A: "EXPANSION_CLASS", 0x3B: "LEVEL_REQUIREMENT", 0x3C: "UNIQUE_CLASS_LIMIT", 0x3E: "RESTRICTED_RACECLASS"}

func codeName(c byte) string {
	if n, ok := createCodes[c]; ok {
		return n
	}
	return fmt.Sprintf("0x%02X", c)
}

// racials reads testdata/racials.tsv: race -> class -> spell ids.
func racials(t *testing.T) map[uint8]map[uint8][]uint32 {
	t.Helper()
	f, err := os.Open("testdata/racials.tsv")
	if err != nil {
		e2eharness.HarnessFailf(t, "testdata/racials.tsv: %v", err)
	}
	defer f.Close()
	out := map[uint8]map[uint8][]uint32{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		col := strings.Split(line, "\t")
		race, _ := strconv.Atoi(col[0])
		class, _ := strconv.Atoi(col[1])
		var ids []uint32
		for _, s := range strings.Split(col[2], ",") {
			n, _ := strconv.Atoi(s)
			ids = append(ids, uint32(n))
		}
		if out[uint8(race)] == nil {
			out[uint8(race)] = map[uint8][]uint32{}
		}
		out[uint8(race)][uint8(class)] = ids
	}
	return out
}

func letters(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = 'a' + b[i]%26
	}
	return string(b)
}

// charScreen is a world session at the character screen of one account.
type charScreen struct {
	w       *client.WorldClient
	list    chan []client.CharEnumEntry
	created chan byte
}

func openCharScreen(t *testing.T, user string) *charScreen {
	t.Helper()
	auth := client.NewAuthClient(user, e2eharness.DefaultPassword)
	realms, err := auth.Authenticate(e2eharness.AuthAddr)
	if err != nil || len(realms) == 0 {
		e2eharness.HarnessFailf(t, "auth %s: %v (realms=%d)", user, err, len(realms))
	}
	cs := &charScreen{list: make(chan []client.CharEnumEntry, 2), created: make(chan byte, 2)}
	cs.w = client.NewWorldClient(strings.ToUpper(user), append([]byte(nil), auth.SessionKey()...), func(string, ...interface{}) {})
	cs.w.SetLogLevel(client.LogWarn)
	cs.w.OnCharList = func(l []client.CharEnumEntry) {
		select {
		case cs.list <- append([]client.CharEnumEntry(nil), l...):
		default:
		}
	}
	cs.w.OnCharCreateResult = func(d []byte) {
		if len(d) > 0 {
			select {
			case cs.created <- d[0]:
			default:
			}
		}
	}
	if err := cs.w.Connect(realms[0].Address); err != nil {
		e2eharness.HarnessFailf(t, "connect world %s: %v", realms[0].Address, err)
	}
	go func() { _ = cs.w.Run() }()
	if err := cs.w.WaitForSessionPhase(client.PhaseAuthed, 20*time.Second); err != nil {
		e2eharness.HarnessFailf(t, "world auth %s: %v", user, err)
	}
	_ = cs.w.SendReadyForAccountDataTimes()
	_ = cs.w.SendRealmSplit()
	return cs
}

func (cs *charScreen) enum(t *testing.T) []client.CharEnumEntry {
	t.Helper()
	if err := cs.w.RequestCharList(); err != nil {
		e2eharness.HarnessFailf(t, "char enum: %v", err)
	}
	select {
	case l := <-cs.list:
		return l
	case <-time.After(15 * time.Second):
		e2eharness.HarnessFailf(t, "no SMSG_CHAR_ENUM")
	}
	return nil
}

func (cs *charScreen) create(t *testing.T, name string, race, class uint8) byte {
	t.Helper()
	select {
	case <-cs.created:
	default:
	}
	if err := cs.w.CreateCharacter(name, race, class, 0, 0, 0, 0, 0, 0, 0); err != nil {
		e2eharness.HarnessFailf(t, "send create: %v", err)
	}
	select {
	case c := <-cs.created:
		return c
	case <-time.After(20 * time.Second):
		e2eharness.HarnessFailf(t, "no SMSG_CHAR_CREATE for %s (race %d class %d)", name, race, class)
	}
	return 0
}

type made struct {
	race, class uint8
	name        string
	guid        uint64
}

// createAll makes the characters of one race on a new account and returns them (failures are reported, not fatal,
// so one bad pair doesn't hide the others).
func createAll(t *testing.T, authDB, charDB *sql.DB, race uint8, classList []uint8) (string, []made) {
	t.Helper()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	user := fmt.Sprintf("TCR%02d%s", race, strings.ToUpper(hex.EncodeToString(suffix)))
	if err := e2eharness.EnsureAccount(authDB, user, e2eharness.DefaultPassword); err != nil {
		e2eharness.HarnessFailf(t, "account %s: %v", user, err)
	}
	if err := e2eharness.SetGM(authDB, user, 3); err != nil {
		e2eharness.HarnessFailf(t, "account %s GM level: %v", user, err)
	}
	cs := openCharScreen(t, user)
	defer cs.w.Close()
	t.Cleanup(func() { deleteAll(t, user) })

	guidOf := func(name string) (uint32, error) {
		var guid uint32
		err := charDB.QueryRow("SELECT `guid` FROM `characters` WHERE `name` = ?", name).Scan(&guid)
		return guid, err
	}
	var out []made
	var firstGUID uint32
	// the death knight last: it may need a level 55 character on the account
	var ordered []uint8
	for _, c := range classList {
		if c != classDeathKnight {
			ordered = append(ordered, c)
		}
	}
	for _, c := range classList {
		if c == classDeathKnight {
			ordered = append(ordered, c)
		}
	}
	for _, class := range ordered {
		name := "Tc" + letters(8)
		code := cs.create(t, name, race, class)
		if code == createLevelNeeded && class == classDeathKnight {
			// CharacterCreating.MinLevelForHeroicCharacter: a level 55 character on the account (a helper warrior
			// when the death knight is the only one asked for), then again
			if firstGUID == 0 {
				helper := "Tc" + letters(8)
				if c := cs.create(t, helper, race, 1); c == createSuccess {
					firstGUID, _ = guidOf(helper)
				}
			}
			if firstGUID != 0 {
				if _, err := charDB.Exec("UPDATE `characters` SET `level` = 55 WHERE `guid` = ?", firstGUID); err == nil {
					name = "Tc" + letters(8)
					code = cs.create(t, name, race, class)
				}
			}
		}
		if code != createSuccess {
			e2eharness.Assertf(t, "race %d class %d: character create answered %s", race, class, codeName(code))
			continue
		}
		guid, err := guidOf(name) // names are stored as "Tcabcdefgh": letters() is lower case
		if err != nil {
			e2eharness.Assertf(t, "race %d class %d: %s created but not in the characters table: %v", race, class, name, err)
			continue
		}
		if firstGUID == 0 {
			firstGUID = guid
		}
		out = append(out, made{race: race, class: class, name: name, guid: uint64(guid)})
	}
	return user, out
}

func deleteAll(t *testing.T, user string) {
	cs := openCharScreen(t, user)
	defer cs.w.Close()
	for _, c := range cs.enum(t) {
		_ = cs.w.DeleteCharacter(c.GUID)
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(time.Second)
}

// checkStart: the character is on playercreateinfo's map and near its spot, death knights in Acherus, with gear on.
func checkStart(t *testing.T, worldDB, charDB *sql.DB, m made) {
	t.Helper()
	var mapID uint32
	var x, y float32
	if err := charDB.QueryRow("SELECT `map`, `position_x`, `position_y` FROM `characters` WHERE `guid` = ?", tcwire.Low(m.guid)).Scan(&mapID, &x, &y); err != nil {
		e2eharness.Assertf(t, "race %d class %d: characters row: %v", m.race, m.class, err)
		return
	}
	var wantMap uint32
	var wx, wy float32
	if err := worldDB.QueryRow("SELECT `map`, `position_x`, `position_y` FROM `playercreateinfo` WHERE `race` = ? AND `class` = ?", m.race, m.class).Scan(&wantMap, &wx, &wy); err != nil {
		e2eharness.Assertf(t, "race %d class %d: no playercreateinfo row: %v", m.race, m.class, err)
		return
	}
	if m.class == classDeathKnight && mapID != mapAcherus {
		e2eharness.Assertf(t, "race %d death knight starts on map %d, want Acherus (%d)", m.race, mapID, mapAcherus)
	}
	if mapID != wantMap || e2eharness.Distance3D(x, y, 0, wx, wy, 0) > 50 {
		e2eharness.Assertf(t, "race %d class %d starts on map %d (%.0f, %.0f), playercreateinfo says map %d (%.0f, %.0f)", m.race, m.class, mapID, x, y, wantMap, wx, wy)
	}
	var gear int
	if err := charDB.QueryRow("SELECT COUNT(*) FROM `character_inventory` WHERE `guid` = ? AND `bag` = 0 AND `slot` < 19", tcwire.Low(m.guid)).Scan(&gear); err != nil {
		e2eharness.HarnessFailf(t, "character_inventory: %v", err)
	}
	if gear == 0 {
		e2eharness.Assertf(t, "race %d class %d starts with nothing equipped", m.race, m.class)
	}
}

// checkRacials logs the character in and checks it knows every racial spell of its race and class.
func checkRacials(t *testing.T, user string, m made, want []uint32) {
	t.Helper()
	w := tcwire.LoginObserved(t, user, m.guid, m.race, func(uint16, []byte) {})
	e2eharness.FlushWorld(t, w)
	var missing []uint32
	for _, id := range want {
		if !w.KnowsSpell(id) {
			missing = append(missing, id)
		}
	}
	tcwire.Logout(t, w)
	if len(missing) > 0 {
		e2eharness.Assertf(t, "race %d class %d doesn't know racial spells %v (of %v): its racial line isn't taught (SkillRaceClassInfo)", m.race, m.class, missing, want)
	}
}

func runRaces(t *testing.T, pick func(race uint8) []uint8) {
	t.Helper()
	want := racials(t)
	authDB, err := e2eharness.OpenAuthDB()
	if err != nil {
		e2eharness.Preconditionf(t, "auth DB: %v", err)
	}
	defer authDB.Close()
	charDB, err := e2eharness.OpenCharDB()
	if err != nil {
		e2eharness.Preconditionf(t, "characters DB: %v", err)
	}
	defer charDB.Close()
	worldDB, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer worldDB.Close()

	total := 0
	for race := uint8(1); race <= 30; race++ {
		race := race
		t.Run(fmt.Sprintf("race%02d", race), func(t *testing.T) {
			user, chars := createAll(t, authDB, charDB, race, pick(race))
			for _, m := range chars {
				checkStart(t, worldDB, charDB, m)
				if racialFixed[race] {
					checkRacials(t, user, m, want[race][m.class])
				}
			}
			total += len(chars)
			t.Logf("race %d: %d characters made and checked", race, len(chars))
		})
	}
	t.Logf("PASS %d characters: created, start map, gear, racials of races %v", total, keys(racialFixed))
}

func keys(m map[uint8]bool) []uint8 {
	var out []uint8
	for r := uint8(1); r <= 30; r++ {
		if m[r] {
			out = append(out, r)
		}
	}
	return out
}

// One character per race; the classes go round (races 6, 16 and 26 make death knights).
func TestRaces_SampleRaceClass(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "races", "sample", "charcreate"}, Runtime: "med", Category: "temucraft/races"})
	runRaces(t, func(race uint8) []uint8 { return []uint8{classes[(int(race)-1)%len(classes)]} })
}

// All 300 race and class pairs.
func TestRaces_EveryRaceClass(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"long", "temucraft", "races", "full", "charcreate"}, Runtime: "long", Category: "temucraft/races"})
	if os.Getenv("E2E_RACES_FULL") != "1" && !strings.Contains(","+strings.ToLower(os.Getenv("E2E_TAGS"))+",", ",full,") {
		t.Skip("300 characters: set E2E_RACES_FULL=1 (or select it with E2E_TAGS=full)")
	}
	runRaces(t, func(uint8) []uint8 { return classes })
}
