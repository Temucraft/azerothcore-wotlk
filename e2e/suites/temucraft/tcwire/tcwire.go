//go:build e2e

// Package tcwire holds the small protocol helpers the Temucraft suites share: reading system messages, notifications
// and addon whispers, gossip menus, item use, mail, and the database checks (character settings, RBAC).
//
// The harness has no gossip, addon or mail helpers, so these build the 3.3.5a packets by hand and read the replies
// through AddPacketHook (race-safe, many subscribers). Every waiter is Arm -> Send -> Wait.
package tcwire

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azerothcore/AzerothGhost/client"
	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
)

// 3.3.5a opcodes the harness doesn't name.
const (
	CmsgUseItem         uint16 = 0x0AB
	CmsgGossipHello     uint16 = 0x17B
	CmsgGossipSelectOpt uint16 = 0x17C
	SmsgGossipMessage   uint16 = 0x17D
	SmsgGossipComplete  uint16 = 0x17E
	SmsgListInventory   uint16 = 0x19F
	SmsgShowBank        uint16 = 0x1B8
	SmsgNotification    uint16 = 0x1CB
	SmsgClearCooldown   uint16 = 0x1DE
	CmsgSendMail        uint16 = 0x238
	SmsgSendMailResult  uint16 = 0x239
	SmsgShowMailbox     uint16 = 0x297
	SmsgTradeStatus     uint16 = 0x120
	ChatMsgSystem       uint8  = 0x00
	ChatMsgSay          uint8  = 0x01
	ChatMsgWhisper      uint8  = 0x07
	LangAddon           uint32 = 0xFFFFFFFF
	chatMsgChannel      uint8  = 0x11
	chatMsgAchievement  uint8  = 0x30
	chatMsgGuildAchieve uint8  = 0x31
	DefaultReplyTimeout        = 5 * time.Second
)

// Chat is one SMSG_MESSAGECHAT.
type Chat struct {
	Type     uint8
	Lang     uint32
	Sender   uint64
	Receiver uint64
	Text     string
}

func isMonsterType(t uint8) bool {
	switch t {
	case 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x29, 0x2A, 0x32: // monster say/party/yell/whisper/emote, raid boss emote/whisper, battlenet
		return true
	}
	return false
}

func readCString(r *bytes.Reader) string {
	var b []byte
	for {
		c, err := r.ReadByte()
		if err != nil || c == 0 {
			return string(b)
		}
		b = append(b, c)
	}
}

func readSized(r *bytes.Reader) string {
	var n uint32
	if binary.Read(r, binary.LittleEndian, &n) != nil || n == 0 || n > 8192 {
		return ""
	}
	b := make([]byte, n)
	if _, err := r.Read(b); err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\x00")
}

// ParseChat decodes an SMSG_MESSAGECHAT body (AzerothCore's ChatHandler::BuildChatPacket layout).
func ParseChat(data []byte) (Chat, bool) {
	var c Chat
	if len(data) < 17 {
		return c, false
	}
	r := bytes.NewReader(data)
	binary.Read(r, binary.LittleEndian, &c.Type)
	binary.Read(r, binary.LittleEndian, &c.Lang)
	binary.Read(r, binary.LittleEndian, &c.Sender)
	var flags uint32
	binary.Read(r, binary.LittleEndian, &flags)
	if isMonsterType(c.Type) {
		_ = readSized(r) // sender name
		binary.Read(r, binary.LittleEndian, &c.Receiver)
		if c.Receiver != 0 && c.Receiver>>48 != 0 { // a named, non-player receiver
			_ = readSized(r)
		}
	} else {
		if c.Type == chatMsgChannel {
			_ = readCString(r)
		}
		binary.Read(r, binary.LittleEndian, &c.Receiver)
	}
	c.Text = readSized(r)
	return c, true
}

var colourCodes = regexp.MustCompile(`\|c[0-9a-fA-F]{8}|\|r`)

// Plain drops the |cAARRGGBB ... |r colour codes.
func Plain(s string) string { return colourCodes.ReplaceAllString(s, "") }

// Collector keeps every system message, notification and addon whisper a bot receives while it is armed.
type Collector struct {
	mu     sync.Mutex
	system []string
	notes  []string
	addon  []string // "PREFIX\tbody" of LANG_ADDON whispers
	says   []Chat
	cancel func()
}

// Collect arms a collector on the bot's world session; it is cancelled at test end.
func Collect(t *testing.T, w *client.WorldClient) *Collector {
	t.Helper()
	c := &Collector{}
	c.cancel = w.AddPacketHook(func(op uint16, data []byte) {
		switch op {
		case client.SmsgMessageChat:
			m, ok := ParseChat(data)
			if !ok {
				return
			}
			c.mu.Lock()
			switch {
			case m.Lang == LangAddon:
				c.addon = append(c.addon, m.Text)
			case m.Type == ChatMsgSystem:
				c.system = append(c.system, Plain(m.Text))
			case m.Type == ChatMsgSay:
				c.says = append(c.says, m)
			}
			c.mu.Unlock()
		case SmsgNotification:
			c.mu.Lock()
			c.notes = append(c.notes, Plain(readCString(bytes.NewReader(data))))
			c.mu.Unlock()
		}
	})
	t.Cleanup(c.cancel)
	return c
}

// Mark is the position to read new messages from (system, notifications, addon).
type Mark struct{ sys, note, addon, say int }

// Mark returns the current end of every list.
func (c *Collector) Mark() Mark {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Mark{len(c.system), len(c.notes), len(c.addon), len(c.says)}
}

// SystemSince returns the system messages after m.
func (c *Collector) SystemSince(m Mark) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.system[min(m.sys, len(c.system)):]...)
}

// NotesSince returns the notifications (SMSG_NOTIFICATION) after m.
func (c *Collector) NotesSince(m Mark) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.notes[min(m.note, len(c.notes)):]...)
}

// AddonSince returns the addon messages with this prefix after m, prefix and tab removed.
func (c *Collector) AddonSince(m Mark, prefix string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, s := range c.addon[min(m.addon, len(c.addon)):] {
		if strings.HasPrefix(s, prefix+"\t") {
			out = append(out, s[len(prefix)+1:])
		}
	}
	return out
}

// SaysSince returns the SAY messages after m.
func (c *Collector) SaysSince(m Mark) []Chat {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Chat(nil), c.says[min(m.say, len(c.says)):]...)
}

func waitFor(timeout time.Duration, get func() (string, bool)) (string, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if s, ok := get(); ok {
			return s, true
		}
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func findIn(list []string, substr string) (string, bool) {
	sub := strings.ToLower(substr)
	for _, s := range list {
		if strings.Contains(strings.ToLower(s), sub) {
			return s, true
		}
	}
	return "", false
}

// WaitSystem waits for a system message after m that contains substr (case-insensitive).
func (c *Collector) WaitSystem(m Mark, substr string, timeout time.Duration) (string, bool) {
	return waitFor(timeout, func() (string, bool) { return findIn(c.SystemSince(m), substr) })
}

// WaitNote waits for a notification after m that contains substr.
func (c *Collector) WaitNote(m Mark, substr string, timeout time.Duration) (string, bool) {
	return waitFor(timeout, func() (string, bool) { return findIn(c.NotesSince(m), substr) })
}

// WaitAddon waits for an addon message (prefix removed) after m that starts with start.
func (c *Collector) WaitAddon(m Mark, prefix, start string, timeout time.Duration) (string, bool) {
	return waitFor(timeout, func() (string, bool) {
		for _, s := range c.AddonSince(m, prefix) {
			if strings.HasPrefix(s, start) {
				return s, true
			}
		}
		return "", false
	})
}

// Command sends a chat command and waits for a system reply containing want.
func Command(t *testing.T, w *client.WorldClient, c *Collector, cmd, want string) string {
	t.Helper()
	m := c.Mark()
	e2eharness.MustGM(t, w, cmd)
	got, ok := c.WaitSystem(m, want, DefaultReplyTimeout)
	if !ok {
		e2eharness.Assertf(t, "%q: no system reply containing %q (got %q)", cmd, want, c.SystemSince(m))
	}
	return got
}

// ---------------------------------------------------------------- raw packets

// ArmPacket waits for the next packet with this opcode that match accepts (nil = any).
func ArmPacket(w *client.WorldClient, opcode uint16, match func([]byte) bool) (wait func(time.Duration) ([]byte, bool), cancel func()) {
	ch := make(chan []byte, 8)
	cancel = w.AddPacketHook(func(op uint16, data []byte) {
		if op != opcode || (match != nil && !match(data)) {
			return
		}
		select {
		case ch <- append([]byte(nil), data...):
		default:
		}
	})
	wait = func(timeout time.Duration) ([]byte, bool) {
		select {
		case d := <-ch:
			return d, true
		case <-time.After(timeout):
			return nil, false
		}
	}
	return wait, cancel
}

// GUIDIs matches a packet that starts with this 64-bit guid.
func GUIDIs(guid uint64) func([]byte) bool {
	return func(d []byte) bool { return len(d) >= 8 && binary.LittleEndian.Uint64(d) == guid }
}

func send(t *testing.T, w *client.WorldClient, opcode uint16, buf *bytes.Buffer) {
	t.Helper()
	if err := w.SendPacketRaw(opcode, buf.Bytes()); err != nil {
		e2eharness.HarnessFailf(t, "send opcode 0x%03X: %v", opcode, err)
	}
}

// ---------------------------------------------------------------- gossip

// GossipOption is one line of a gossip menu.
type GossipOption struct {
	Index   uint32
	Icon    uint8
	Coded   bool
	Money   uint32
	Text    string
	BoxText string
}

// GossipMenu is an SMSG_GOSSIP_MESSAGE.
type GossipMenu struct {
	GUID    uint64
	MenuID  uint32
	TextID  uint32
	Options []GossipOption
}

// Find returns the first option whose text contains substr (case-insensitive, colours removed).
func (g GossipMenu) Find(substr string) (GossipOption, bool) {
	sub := strings.ToLower(substr)
	for _, o := range g.Options {
		if strings.Contains(strings.ToLower(Plain(o.Text)), sub) {
			return o, true
		}
	}
	return GossipOption{}, false
}

// Count returns how many options contain substr.
func (g GossipMenu) Count(substr string) int {
	n := 0
	sub := strings.ToLower(substr)
	for _, o := range g.Options {
		if strings.Contains(strings.ToLower(Plain(o.Text)), sub) {
			n++
		}
	}
	return n
}

// Texts lists the option texts (for logs).
func (g GossipMenu) Texts() []string {
	out := make([]string, 0, len(g.Options))
	for _, o := range g.Options {
		out = append(out, Plain(o.Text))
	}
	return out
}

// ParseGossip decodes an SMSG_GOSSIP_MESSAGE.
func ParseGossip(data []byte) (GossipMenu, bool) {
	var g GossipMenu
	r := bytes.NewReader(data)
	if binary.Read(r, binary.LittleEndian, &g.GUID) != nil {
		return g, false
	}
	var count uint32
	binary.Read(r, binary.LittleEndian, &g.MenuID)
	binary.Read(r, binary.LittleEndian, &g.TextID)
	if binary.Read(r, binary.LittleEndian, &count) != nil || count > 64 {
		return g, false
	}
	for i := uint32(0); i < count; i++ {
		var o GossipOption
		var coded uint8
		binary.Read(r, binary.LittleEndian, &o.Index)
		binary.Read(r, binary.LittleEndian, &o.Icon)
		binary.Read(r, binary.LittleEndian, &coded)
		binary.Read(r, binary.LittleEndian, &o.Money)
		o.Coded = coded != 0
		o.Text = readCString(r)
		o.BoxText = readCString(r)
		g.Options = append(g.Options, o)
	}
	return g, true
}

// ArmGossip waits for the next gossip menu from this unit.
func ArmGossip(w *client.WorldClient, guid uint64) (wait func(time.Duration) (GossipMenu, bool), cancel func()) {
	raw, cancel := ArmPacket(w, SmsgGossipMessage, GUIDIs(guid))
	return func(timeout time.Duration) (GossipMenu, bool) {
		d, ok := raw(timeout)
		if !ok {
			return GossipMenu{}, false
		}
		return ParseGossip(d)
	}, cancel
}

// GossipHello talks to a unit and returns its menu.
func GossipHello(t *testing.T, w *client.WorldClient, guid uint64) GossipMenu {
	t.Helper()
	wait, cancel := ArmGossip(w, guid)
	defer cancel()
	SendGossipHello(t, w, guid)
	g, ok := wait(DefaultReplyTimeout)
	if !ok {
		e2eharness.Assertf(t, "no gossip menu from %#x after CMSG_GOSSIP_HELLO", guid)
	}
	return g
}

// SendGossipHello sends CMSG_GOSSIP_HELLO without waiting.
func SendGossipHello(t *testing.T, w *client.WorldClient, guid uint64) {
	t.Helper()
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, guid)
	send(t, w, CmsgGossipHello, buf)
}

// GossipSelect picks a gossip option (no box code).
func GossipSelect(t *testing.T, w *client.WorldClient, menu GossipMenu, option GossipOption) {
	t.Helper()
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, menu.GUID)
	binary.Write(buf, binary.LittleEndian, menu.MenuID)
	binary.Write(buf, binary.LittleEndian, option.Index)
	if option.Coded {
		buf.WriteByte(0)
	}
	send(t, w, CmsgGossipSelectOpt, buf)
}

// ---------------------------------------------------------------- items, addon, mail

// ItemLowGUID returns the low guid of the item in a backpack slot (bag 255 in the push result is bag 0 in the
// characters DB). Saves first.
func ItemLowGUID(t *testing.T, b *e2eharness.ScenarioBot, bag, slot uint8) uint32 {
	t.Helper()
	b.Save(t)
	dbBag := uint32(0)
	if bag != 255 {
		e2eharness.Preconditionf(t, "ItemLowGUID: item went to bag %d, expected the backpack", bag)
	}
	var low uint32
	err := b.CharDB.QueryRow("SELECT `item` FROM `character_inventory` WHERE `guid` = ? AND `bag` = ? AND `slot` = ?",
		uint32(b.GUID&0xFFFFFFFF), dbBag, slot).Scan(&low)
	if err != nil {
		e2eharness.HarnessFailf(t, "item in backpack slot %d: %v", slot, err)
	}
	return low
}

// UseItem sends CMSG_USE_ITEM for a backpack item on yourself.
func UseItem(t *testing.T, w *client.WorldClient, bag, slot uint8, itemGUID uint64, spellID uint32) {
	t.Helper()
	buf := new(bytes.Buffer)
	buf.WriteByte(bag)
	buf.WriteByte(slot)
	buf.WriteByte(1) // cast count
	binary.Write(buf, binary.LittleEndian, spellID)
	binary.Write(buf, binary.LittleEndian, itemGUID)
	binary.Write(buf, binary.LittleEndian, uint32(0)) // glyph index
	buf.WriteByte(0)                                  // cast flags
	binary.Write(buf, binary.LittleEndian, uint32(0)) // target mask: self
	send(t, w, CmsgUseItem, buf)
}

// ItemSpell returns the first on-use spell of an item template (0 if none).
func ItemSpell(t *testing.T, worldDB *sql.DB, entry uint32) uint32 {
	t.Helper()
	var spell uint32
	if err := worldDB.QueryRow("SELECT `spellid_1` FROM `item_template` WHERE `entry` = ?", entry).Scan(&spell); err != nil {
		e2eharness.Preconditionf(t, "item_template %d: %v (is the module's SQL applied?)", entry, err)
	}
	return spell
}

// SendAddon whispers an addon message (LANG_ADDON) to a player name, the way an addon's SendAddonMessage does.
func SendAddon(t *testing.T, w *client.WorldClient, target, prefix, body string) {
	t.Helper()
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, uint32(ChatMsgWhisper))
	binary.Write(buf, binary.LittleEndian, LangAddon)
	buf.WriteString(target)
	buf.WriteByte(0)
	buf.WriteString(prefix + "\t" + body)
	buf.WriteByte(0)
	send(t, w, client.CmsgMessageChat, buf)
}

// SendMail sends CMSG_SEND_MAIL with no items. A game master may use their own guid as the mailbox.
func SendMail(t *testing.T, w *client.WorldClient, mailbox uint64, receiver, subject, body string, money uint32) {
	t.Helper()
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, mailbox)
	for _, s := range []string{receiver, subject, body} {
		buf.WriteString(s)
		buf.WriteByte(0)
	}
	binary.Write(buf, binary.LittleEndian, uint32(41)) // stationery
	binary.Write(buf, binary.LittleEndian, uint32(0))
	buf.WriteByte(0) // items
	binary.Write(buf, binary.LittleEndian, money)
	binary.Write(buf, binary.LittleEndian, uint32(0)) // COD
	binary.Write(buf, binary.LittleEndian, uint64(0))
	buf.WriteByte(0)
	send(t, w, CmsgSendMail, buf)
}

// ---------------------------------------------------------------- database

// Low is the low part of a player guid (the characters DB key).
func Low(guid uint64) uint32 { return uint32(guid & 0xFFFFFFFF) }

// CharSetting reads mod_temucraft_character_setting (ok=false when there is no row).
func CharSetting(t *testing.T, charDB *sql.DB, guid uint64, setting string) (int64, bool) {
	t.Helper()
	var v int64
	err := charDB.QueryRow("SELECT `value` FROM `mod_temucraft_character_setting` WHERE `guid` = ? AND `setting` = ?", Low(guid), setting).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, false
	}
	if err != nil {
		e2eharness.HarnessFailf(t, "mod_temucraft_character_setting %s: %v", setting, err)
	}
	return v, true
}

// AccountID looks up an account id by name.
func AccountID(t *testing.T, authDB *sql.DB, user string) uint32 {
	t.Helper()
	var id uint32
	if err := authDB.QueryRow("SELECT `id` FROM `account` WHERE `username` = ?", strings.ToUpper(user)).Scan(&id); err != nil {
		e2eharness.HarnessFailf(t, "account %s: %v", user, err)
	}
	return id
}

// DenyPermission denies one RBAC permission to an account on every realm (taken back at test end). The session
// loads its permissions at login, so Relog after this.
func DenyPermission(t *testing.T, authDB *sql.DB, accountID, perm uint32) {
	t.Helper()
	if _, err := authDB.Exec("REPLACE INTO `rbac_account_permissions` (`accountId`, `permissionId`, `granted`, `realmId`) VALUES (?, ?, 0, -1)", accountID, perm); err != nil {
		e2eharness.HarnessFailf(t, "rbac deny %d to %d: %v", perm, accountID, err)
	}
	t.Cleanup(func() {
		authDB.Exec("DELETE FROM `rbac_account_permissions` WHERE `accountId` = ? AND `permissionId` = ?", accountID, perm)
	})
}
