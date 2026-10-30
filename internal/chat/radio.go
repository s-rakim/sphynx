package chat

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Radio contacts are people in the field who reach Sphynx over long-range
// radio (Sphynx's radio mode). Each radio station is a contact of its own,
// like a device, but it has no browser: Sphynx holds the keys of its one
// "browser", the radio bridge. The bridge encrypts what the station sends
// for the devices it talks to, and reads what devices send the station to
// pass it on over the radio. Radio carries text in the clear, so chats with
// a radio contact are not end-to-end encrypted, and the app says so.

// RadioContact is a radio station shown as a contact.
type RadioContact struct {
	Key    string // stable id, like "meshtastic:!a1b2c3d4" or "aprs:5Z4ABC-7"
	Name   string
	Detail string // shown under the name, like "Meshtastic radio !a1b2c3d4"
	Link   string // "meshtastic" or "aprs"
	// MaxText is how many bytes of text a message can take over the
	// radio, the sender's name included; longer ones are cut.
	MaxText int
}

// RadioSend passes a message for a radio contact on over the radio. It
// returns once the station has acknowledged it, or with why it couldn't.
type RadioSend func(ctx context.Context, key, text string) error

// RadioUserID is the chat account id of a radio station.
func RadioUserID(key string) string {
	sum := sha256.Sum256([]byte("sphynx-chat/radio|" + key))
	return "r" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))[:15]
}

type radioState struct {
	contacts map[string]RadioContact // by account id
	send     RadioSend
}

// maxRadioText caps what Sphynx passes on over the radio in one go; the
// radio link splits it into messages it can carry.
const maxRadioText = 1000

// SetRadio sets the radio contacts and how messages for them are sent.
func (s *Server) SetRadio(contacts []RadioContact, send RadioSend) {
	s.locked(func() {
		old := s.radio.contacts
		s.radio = radioState{contacts: map[string]RadioContact{}, send: send}
		for _, c := range contacts {
			id := RadioUserID(c.Key)
			s.radio.contacts[id] = c
			s.ensureRadioUser(id, c)
		}
		for id := range old {
			if _, ok := s.radio.contacts[id]; !ok {
				if u := s.getUser(id); u != nil {
					s.broadcastUser(u) // now shown as removed
				}
			}
		}
	})
}

func (s *Server) ensureRadioUser(id string, c RadioContact) {
	name := truncate(strings.TrimSpace(c.Name), 60)
	if name == "" {
		name = c.Detail
	}
	changed := false
	if u := s.getUser(id); u == nil {
		s.db.run(`INSERT INTO users (id, name, device_name, about, created_at) VALUES (?, ?, ?, '', ?)`, id, name, c.Detail, now())
		changed = true
	} else if u.Name != name || u.DeviceName != c.Detail {
		s.db.run("UPDATE users SET name = ?, device_name = ? WHERE id = ?", name, c.Detail, id)
		changed = true
	}
	if !s.isRadioUser(id) {
		priv, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		deviceID := newID(16)
		s.db.inTx(func() {
			// The token is a hash of a secret nobody keeps: no browser can
			// ever sign in as the bridge.
			s.db.run(`INSERT INTO devices (id, user_id, name, public_key, token_hash, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				deviceID, id, "Sphynx radio bridge", publicJWK(priv.PublicKey()), hashToken(newID(32)), now())
			s.db.run("INSERT INTO radio_keys (user_id, device_id, private_key) VALUES (?, ?, ?)", id, deviceID, b64u(priv.Bytes()))
		})
		changed = true
	}
	if changed {
		s.broadcastUser(s.getUser(id))
	}
}

func (s *Server) isRadioUser(id string) bool {
	return s.db.exists("SELECT 1 FROM radio_keys WHERE user_id = ?", id)
}

// radioLink is what userView shows for a radio account: its link, or
// "removed" once it is no longer a radio contact, and how long a message
// to it can be, the sender's name included.
func (s *Server) radioLink(id string) (link string, maxText int) {
	if c, ok := s.radio.contacts[id]; ok {
		return c.Link, c.MaxText
	}
	if s.isRadioUser(id) {
		return "removed", 0
	}
	return "", 0
}

func (s *Server) radioKey(userID string) (string, *ecdh.PrivateKey) {
	var deviceID, key string
	if !s.db.one(func(r scanner) error { return r.Scan(&deviceID, &key) },
		"SELECT device_id, private_key FROM radio_keys WHERE user_id = ?", userID) {
		return "", nil
	}
	raw, err := unb64u(key)
	if err != nil {
		return "", nil
	}
	priv, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return "", nil
	}
	return deviceID, priv
}

// RadioReceived passes a message from a radio contact into the chat: to
// the device named after an "@" at its start ("@laptop on my way"), or
// else to every device that has a chat with the contact, plus deliverTo
// (device accounts, see DeviceUserID). It returns the names of the
// devices that got it.
func (s *Server) RadioReceived(key, text string, deliverTo []string) (names []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err = guard(func() { names = s.radioReceived(key, text, deliverTo) })
	return names, err
}

func (s *Server) radioReceived(key, text string, deliverTo []string) []string {
	id := RadioUserID(key)
	if _, ok := s.radio.contacts[id]; !ok {
		fail(404, "not a radio contact")
	}
	text = truncate(strings.TrimSpace(text), maxRadioText)
	if text == "" {
		fail(400, "empty message")
	}
	var targets []string
	if to, rest, ok := s.radioAddressee(text); ok {
		targets, text = []string{to}, rest
	} else {
		targets = s.db.strings(`SELECT m2.user_id FROM chats c JOIN members m1 ON m1.chat_id = c.id JOIN members m2 ON m2.chat_id = c.id
			WHERE c.type = 'direct' AND m1.user_id = ? AND m2.user_id != ?`, id, id)
		targets = append(targets, deliverTo...)
	}
	s.db.run("UPDATE users SET last_seen = ? WHERE id = ?", now(), id)
	var names []string
	for _, t := range uniq(targets) {
		if t == id || s.isRadioUser(t) || !s.ensureUser(t) || s.isBlocked(t, id) {
			continue
		}
		// Without a browser nothing could read it.
		if !s.db.exists("SELECT 1 FROM devices WHERE user_id = ? AND revoked_at IS NULL", t) {
			continue
		}
		chat := s.directChat(id, t)
		s.radioPost(chat, id, map[string]any{"type": "text", "text": text})
		if u := s.getUser(t); u != nil {
			names = append(names, u.Name)
		}
	}
	if u := s.getUser(id); u != nil {
		s.broadcastUser(u)
	}
	if len(names) == 0 {
		fail(409, "none of the devices it could go to has opened Sphynx Chat yet")
	}
	return names
}

// radioAddressee reads "@name message", a message for one device: an
// exact name, or the start of exactly one device's name.
func (s *Server) radioAddressee(text string) (string, string, bool) {
	if !strings.HasPrefix(text, "@") {
		return "", "", false
	}
	word, rest, _ := strings.Cut(text[1:], " ")
	want, rest := squash(word), strings.TrimSpace(rest)
	if want == "" || rest == "" {
		return "", "", false
	}
	names := map[string][]string{} // account id -> names
	for _, d := range s.directorySnapshot() {
		names[d.ID] = append(names[d.ID], d.Name)
	}
	s.db.all(func(r scanner) error {
		var id, name, device string
		if err := r.Scan(&id, &name, &device); err != nil {
			return err
		}
		names[id] = append(names[id], name, device)
		return nil
	}, "SELECT id, name, device_name FROM users WHERE id NOT IN (SELECT user_id FROM radio_keys)")
	var exact, prefix []string
	for id, list := range names {
		if !s.onNetwork(id) {
			continue
		}
		for _, n := range list {
			switch sq := squash(n); {
			case sq == "":
			case sq == want:
				exact = append(exact, id)
			case strings.HasPrefix(sq, want):
				prefix = append(prefix, id)
			}
		}
	}
	exact, prefix = uniq(exact), uniq(prefix)
	switch {
	case len(exact) == 1:
		return exact[0], rest, true
	case len(exact) == 0 && len(prefix) == 1:
		return prefix[0], rest, true
	}
	return "", "", false
}

// squash lower-cases a name and keeps only letters and digits, so
// "@malcolmsphone" finds "Malcolm's phone".
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// directChat returns the one-to-one chat of two accounts, creating it.
func (s *Server) directChat(creator, other string) *chatRow {
	pair := []string{creator, other}
	if pair[1] < pair[0] {
		pair[0], pair[1] = pair[1], pair[0]
	}
	key := pair[0] + "|" + pair[1]
	var found chatRow
	if s.db.one(scanChat(&found), "SELECT "+chatCols+" FROM chats WHERE direct_key = ?", key) {
		return &found
	}
	chatID := newID(16)
	s.db.inTx(func() {
		s.db.run("INSERT INTO chats (id, type, created_by, created_at, direct_key) VALUES (?, 'direct', ?, ?, ?)", chatID, creator, now(), key)
		for _, uid := range uniq([]string{creator, other}) {
			s.db.run("INSERT INTO members (chat_id, user_id, role, joined_at) VALUES (?, ?, 'member', ?)", chatID, uid, now())
		}
	})
	return s.getChat(chatID)
}

// radioPost writes a message from a radio contact into a chat, encrypted
// for every browser in it.
func (s *Server) radioPost(chat *chatRow, from string, content map[string]any) {
	deviceID, priv := s.radioKey(from)
	if priv == nil {
		failf(500, "radio contact %s has no key", from)
	}
	recipients := map[string]*ecdh.PublicKey{}
	for _, d := range s.devices(`SELECT `+prefixed("d.", deviceCols)+` FROM devices d JOIN members m ON m.user_id = d.user_id
		WHERE m.chat_id = ? AND m.left_at IS NULL AND d.revoked_at IS NULL`, chat.ID) {
		if k, err := parseJWK(string(d.PublicKey)); err == nil {
			recipients[d.ID] = k
		}
	}
	id := newID(16)
	payload, err := sealEnvelope(deviceID, priv, recipients, chatAAD(chat.ID, id), content)
	if err != nil {
		panic(err)
	}
	m := s.insertMessage(chat, newMessage{id: id, senderID: from, senderDevice: &deviceID, kind: "msg", payload: &payload})
	s.broadcastMessage(m, "message", sendOpts{})
	s.broadcastChat(chat.ID)
}

func chatAAD(chatID, messageID string) string {
	return "tailchat/v1|chat:" + chatID + "|msg:" + messageID
}

// radioOutgoing passes a device's message to a radio contact on over the
// radio, in the background. Delivery shows as the usual tick; a failure
// as a note in the chat.
func (s *Server) radioOutgoing(chat *chatRow, m *messageRow, sender *userRow) {
	radioID := ""
	for _, uid := range s.activeMemberIDs(chat.ID) {
		if uid != sender.ID && s.isRadioUser(uid) {
			radioID = uid
		}
	}
	if radioID == "" {
		return
	}
	c, ok := s.radio.contacts[radioID]
	if !ok || s.radio.send == nil {
		s.systemMessage(chat, radioID, map[string]any{"event": "radio-failed", "error": "this radio contact was removed from Sphynx's radio settings"})
		return
	}
	text, note := s.radioText(chat, m, radioID)
	if note != "" {
		s.systemMessage(chat, radioID, map[string]any{"event": "radio-note", "note": note})
	}
	if text == "" {
		return
	}
	text = truncate(truncate(sender.Name, 20)+": "+text, maxRadioText)
	if c.MaxText > 0 && len(text) > c.MaxText {
		text = cutBytes(text, c.MaxText-3) + "..."
		s.systemMessage(chat, radioID, map[string]any{"event": "radio-note",
			"note": fmt.Sprintf("Messages over this radio can be about %d characters long: the end of this one was cut off.", c.MaxText)})
	}
	send, chatID, seq := s.radio.send, chat.ID, m.Seq
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 20*time.Minute)
		defer cancel()
		err := send(ctx, c.Key, text)
		s.locked(func() {
			chat := s.getChat(chatID)
			if chat == nil {
				return
			}
			if err != nil {
				s.systemMessage(chat, radioID, map[string]any{"event": "radio-failed", "error": err.Error()})
				return
			}
			s.radioDelivered(chat, radioID, seq)
		})
	}()
}

// cutBytes cuts s to at most n bytes, between characters.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// radioText reads a message for the radio. note explains anything that
// can't go over the radio.
func (s *Server) radioText(chat *chatRow, m *messageRow, radioID string) (text, note string) {
	deviceID, priv := s.radioKey(radioID)
	if priv == nil || m.Payload == nil {
		return "", "Sphynx couldn't pass this message on over the radio."
	}
	content, err := openEnvelope(*m.Payload, deviceID, priv, chatAAD(chat.ID, m.ID), m.SenderID,
		func(id string) (*ecdh.PublicKey, string, error) {
			var d deviceRow
			if !s.db.one(scanDevice(&d), "SELECT "+deviceCols+" FROM devices WHERE id = ?", id) {
				return nil, "", errors.New("unknown device")
			}
			k, err := parseJWK(d.PublicKey)
			return k, d.UserID, err
		})
	if err != nil {
		return "", "Sphynx couldn't read this message to pass it on over the radio (" + err.Error() + "). Reload the chat and send it again."
	}
	str := func(k string) string { v, _ := content[k].(string); return strings.TrimSpace(v) }
	switch content["type"] {
	case "text":
		return str("text"), ""
	case "media":
		kind := map[string]string{"image": "photo", "video": "video", "gif": "GIF", "audio": "audio", "voice": "voice message",
			"sticker": "sticker", "document": "document"}[str("kind")]
		if kind == "" {
			kind = "file"
		}
		note = "Only text goes over the radio: the " + kind + " itself wasn't sent."
		if c := str("caption"); c != "" {
			return "[" + kind + "] " + c, note
		}
		return "", note
	case "location":
		lat, ok1 := content["lat"].(float64)
		lon, ok2 := content["lon"].(float64)
		if !ok1 || !ok2 {
			return "", "Only text goes over the radio."
		}
		loc := fmt.Sprintf("Location: %s,%s", strconv.FormatFloat(lat, 'f', 5, 64), strconv.FormatFloat(lon, 'f', 5, 64))
		if n := str("name"); n != "" {
			loc += " (" + n + ")"
		}
		return loc, ""
	case "poll":
		return "", "Polls don't go over the radio."
	}
	return "", "Only text goes over the radio: this message wasn't sent."
}

// radioDelivered marks messages up to seq as delivered to the radio
// contact (two ticks; radio has no read receipts).
func (s *Server) radioDelivered(chat *chatRow, radioID string, seq int64) {
	s.db.run("UPDATE members SET delivered_seq = MAX(delivered_seq, ?) WHERE chat_id = ? AND user_id = ?", seq, chat.ID, radioID)
	fresh := s.getMember(chat.ID, radioID)
	if fresh == nil {
		return
	}
	for _, uid := range s.everMemberIDs(chat.ID) {
		s.hub.sendToUser(uid, map[string]any{"type": "receipt", "chatId": chat.ID, "member": s.memberView(fresh, chat, uid)}, sendOpts{})
	}
}

// noRadio refuses radio contacts where only one-to-one chats make sense.
func (s *Server) noRadio(userID string) {
	if s.isRadioUser(userID) {
		fail(400, "Radio contacts can only have one-to-one chats")
	}
}
