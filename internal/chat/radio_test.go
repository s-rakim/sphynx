package chat

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAESKeyWrap(t *testing.T) {
	// RFC 3394 4.6: 256 bits of key data with a 256-bit key.
	kek, _ := hex.DecodeString("000102030405060708090A0B0C0D0E0F101112131415161718191A1B1C1D1E1F")
	key, _ := hex.DecodeString("00112233445566778899AABBCCDDEEFF000102030405060708090A0B0C0D0E0F")
	want, _ := hex.DecodeString("28C9F404C4B810F4CBCCB35CFB87F8263F5786E2D80ED326CBC7F0E71A99F43BFB988B9B7A02DD21")
	got, err := aesKeyWrap(kek, key)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("wrap = %X, %v", got, err)
	}
	back, err := aesKeyUnwrap(kek, got)
	if err != nil || !bytes.Equal(back, key) {
		t.Fatalf("unwrap = %X, %v", back, err)
	}
	got[3] ^= 1
	if _, err := aesKeyUnwrap(kek, got); err == nil {
		t.Fatal("a tampered key unwrapped")
	}
}

func TestSealOpen(t *testing.T) {
	alice, _ := ecdh.P256().GenerateKey(rand.Reader)
	bob, _ := ecdh.P256().GenerateKey(rand.Reader)
	pub, err := parseJWK(publicJWK(alice.PublicKey()))
	if err != nil || !pub.Equal(alice.PublicKey()) {
		t.Fatalf("JWK round trip: %v", err)
	}
	aad := chatAAD("c1", "m1")
	env, err := sealEnvelope("dev-a", alice, map[string]*ecdh.PublicKey{"dev-b": bob.PublicKey()}, aad, map[string]any{"type": "text", "text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	keys := func(id string) (*ecdh.PublicKey, string, error) {
		if id == "dev-a" {
			return alice.PublicKey(), "alice", nil
		}
		return nil, "", errors.New("unknown device")
	}
	c, err := openEnvelope(env, "dev-b", bob, aad, "alice", keys)
	if err != nil || c["text"] != "hi" {
		t.Fatalf("open = %v, %v", c, err)
	}
	// The sender keeps a copy for itself, like the browsers do.
	if c, err := openEnvelope(env, "dev-a", alice, aad, "alice", keys); err != nil || c["text"] != "hi" {
		t.Fatalf("own copy = %v, %v", c, err)
	}
	if _, err := openEnvelope(env, "dev-b", bob, aad, "mallory", keys); err == nil {
		t.Fatal("opened a message relabelled to another sender")
	}
	if _, err := openEnvelope(env, "dev-b", bob, chatAAD("c1", "m2"), "alice", keys); err == nil {
		t.Fatal("opened a message moved to another id")
	}
	if _, err := openEnvelope(env, "dev-c", bob, aad, "alice", keys); err == nil {
		t.Fatal("opened a message not sealed for this device")
	}
}

// chatClient is a device with a browser, talking to the server in dev mode.
type chatClient struct {
	t      *testing.T
	h      http.Handler
	user   string
	token  string
	device string
	key    *ecdh.PrivateKey
}

func (c *chatClient) call(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = "localhost"
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Dev-User", c.user)
	if c.token != "" {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (c *chatClient) must(method, path string, body any) map[string]any {
	c.t.Helper()
	code, out := c.call(method, path, body)
	if code != http.StatusOK {
		c.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func newChatClient(t *testing.T, h http.Handler, user string) *chatClient {
	c := &chatClient{t: t, h: h, user: user}
	c.key, _ = ecdh.P256().GenerateKey(rand.Reader)
	var jwk map[string]any
	_ = json.Unmarshal([]byte(publicJWK(c.key.PublicKey())), &jwk)
	out := c.must("POST", "/api/devices", map[string]any{"name": user + "'s browser", "publicKey": jwk})
	c.token = out["token"].(string)
	c.device = out["device"].(map[string]any)["id"].(string)
	return c
}

// read decrypts a message the way a browser does.
func (c *chatClient) read(srv *Server, chatID string, m map[string]any) string {
	c.t.Helper()
	keys := func(id string) (*ecdh.PublicKey, string, error) {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		var d deviceRow
		if !srv.db.one(scanDevice(&d), "SELECT "+deviceCols+" FROM devices WHERE id = ?", id) {
			return nil, "", errors.New("unknown device")
		}
		k, err := parseJWK(d.PublicKey)
		return k, d.UserID, err
	}
	content, err := openEnvelope(m["payload"].(string), c.device, c.key, chatAAD(chatID, m["id"].(string)), m["senderId"].(string), keys)
	if err != nil {
		c.t.Fatalf("open: %v", err)
	}
	return content["text"].(string)
}

// send seals a text for every browser in the chat and sends it.
func (c *chatClient) send(chatID string, content map[string]any) map[string]any {
	c.t.Helper()
	recipients := map[string]*ecdh.PublicKey{}
	for _, d := range c.must("GET", "/api/chats/"+chatID+"/devices", nil)["devices"].([]any) {
		dv := d.(map[string]any)
		b, _ := json.Marshal(dv["publicKey"])
		k, err := parseJWK(string(b))
		if err != nil {
			c.t.Fatal(err)
		}
		recipients[dv["id"].(string)] = k
	}
	id := newID(16)
	payload, err := sealEnvelope(c.device, c.key, recipients, chatAAD(chatID, id), content)
	if err != nil {
		c.t.Fatal(err)
	}
	return c.must("POST", "/api/chats/"+chatID+"/messages", map[string]any{"id": id, "payload": payload})["message"].(map[string]any)
}

func (c *chatClient) messages(chatID string) []map[string]any {
	var out []map[string]any
	for _, m := range c.must("GET", "/api/chats/"+chatID+"/messages", nil)["messages"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

func TestRadioContacts(t *testing.T) {
	srv, err := New(Config{DataDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	h := srv.Handler(DevResolver{})
	ana := newChatClient(t, h, "ana")
	ben := newChatClient(t, h, "ben")

	var mu sync.Mutex
	var sent []string
	send := func(ctx context.Context, key, text string) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, key+" "+text)
		if strings.Contains(text, "fail me") {
			return errors.New("no acknowledgement from !a1b2c3d4")
		}
		return nil
	}
	const key = "meshtastic:!a1b2c3d4"
	field := RadioUserID(key)
	srv.SetRadio([]RadioContact{{Key: key, Name: "Field radio", Detail: "Meshtastic radio !a1b2c3d4", Link: "meshtastic", MaxText: 60}}, send)
	toAna := []string{"ana@dev"}

	users := ana.must("GET", "/api/users", nil)["users"].([]any)
	var radioUser map[string]any
	for _, u := range users {
		if u.(map[string]any)["id"] == field {
			radioUser = u.(map[string]any)
		}
	}
	if radioUser == nil || radioUser["radio"] != "meshtastic" || radioUser["radioMax"] != 60.0 || radioUser["joined"] != true || radioUser["onNetwork"] != true ||
		radioUser["name"] != "Field radio" || radioUser["device"] != "Meshtastic radio !a1b2c3d4" {
		t.Fatalf("radio contact = %v", radioUser)
	}

	// A message from the field goes to the default device.
	names, err := srv.RadioReceived(key, "hello from the hill", toAna)
	if err != nil || len(names) != 1 || names[0] != "Ana" {
		t.Fatalf("received: %v, %v", names, err)
	}
	chats := ana.must("GET", "/api/chats", nil)["chats"].([]any)
	if len(chats) != 1 {
		t.Fatalf("chats = %v", chats)
	}
	chatID := chats[0].(map[string]any)["id"].(string)
	msgs := ana.messages(chatID)
	if len(msgs) != 1 || msgs[0]["senderId"] != field || ana.read(srv, chatID, msgs[0]) != "hello from the hill" {
		t.Fatalf("messages = %v", msgs)
	}

	// Ana's reply goes out over the radio, and is marked delivered.
	m := ana.send(chatID, map[string]any{"type": "text", "text": "on my way"})
	waitUntil(t, func() bool {
		for _, mem := range ana.must("GET", "/api/chats/"+chatID, nil)["chat"].(map[string]any)["members"].([]any) {
			mm := mem.(map[string]any)
			if mm["userId"] == field && mm["deliveredSeq"].(float64) >= m["seq"].(float64) {
				return true
			}
		}
		return false
	})
	mu.Lock()
	if len(sent) != 1 || sent[0] != key+" Ana: on my way" {
		t.Fatalf("sent = %q", sent)
	}
	mu.Unlock()

	// A failure shows in the chat; a photo's caption goes, with a note.
	ana.send(chatID, map[string]any{"type": "text", "text": "fail me"})
	ana.send(chatID, map[string]any{"type": "media", "kind": "image", "caption": "the view"})
	ana.send(chatID, map[string]any{"type": "location", "lat": -1.28333, "lon": 36.81667, "name": "camp"})
	waitUntil(t, func() bool {
		n := 0
		for _, m := range ana.messages(chatID) {
			if m["kind"] == "system" {
				n++
			}
		}
		return n == 2
	})
	events := map[string]string{}
	for _, m := range ana.messages(chatID) {
		if m["kind"] == "system" {
			var ev map[string]any
			_ = json.Unmarshal([]byte(m["payload"].(string)), &ev)
			events[ev["event"].(string)], _ = ev["error"].(string)
			if ev["event"] == "radio-note" {
				events["radio-note"] = ev["note"].(string)
			}
		}
	}
	if events["radio-failed"] != "no acknowledgement from !a1b2c3d4" || !strings.Contains(events["radio-note"], "photo itself wasn't sent") {
		t.Fatalf("events = %v", events)
	}
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(sent) == 4 })
	mu.Lock()
	if sent[2] != key+" Ana: [photo] the view" || sent[3] != key+" Ana: Location: -1.28333,36.81667 (camp)" {
		t.Fatalf("sent = %q", sent)
	}
	mu.Unlock()

	// "@ben ..." goes to Ben only; afterwards both chats get messages.
	if names, err := srv.RadioReceived(key, "@BEN are you home?", toAna); err != nil || len(names) != 1 || names[0] != "Ben" {
		t.Fatalf("@ben: %v, %v", names, err)
	}
	benChats := ben.must("GET", "/api/chats", nil)["chats"].([]any)
	benChat := benChats[0].(map[string]any)["id"].(string)
	if got := ben.read(srv, benChat, ben.messages(benChat)[0]); got != "are you home?" {
		t.Fatalf("ben got %q", got)
	}
	if names, _ := srv.RadioReceived(key, "both of you", nil); len(names) != 2 {
		t.Fatalf("names = %v", names)
	}

	// Too long for the radio: cut, with a note.
	ana.send(chatID, map[string]any{"type": "text", "text": "Ünïcode " + strings.Repeat("x", 80)})
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(sent) == 5 })
	mu.Lock()
	if want := key + " Ana: Ünïcode " + strings.Repeat("x", 57-len("Ana: Ünïcode ")) + "..."; sent[4] != want {
		t.Fatalf("sent %q, want %q", sent[4], want)
	}
	mu.Unlock()
	last := ana.messages(chatID)
	if p := last[len(last)-1]; p["kind"] != "system" || !strings.Contains(p["payload"].(string), "about 60 characters") {
		t.Fatalf("note = %v", p)
	}

	// Radio contacts can't be in groups.
	if code, _ := ana.call("POST", "/api/chats/group", map[string]any{"name": "Trip", "members": []string{"ben@dev", field}}); code != http.StatusBadRequest {
		t.Fatalf("group with a radio contact: %d", code)
	}
	// Nobody can sign in as the radio contact's bridge.
	if code, _ := (&chatClient{t: t, h: h, user: field, token: "x.y"}).call("GET", "/api/chats", nil); code == http.StatusOK {
		t.Fatal("signed in as a radio contact")
	}
	// Unknown stations don't get in.
	if _, err := srv.RadioReceived("aprs:N0CALL", "hi", toAna); err == nil {
		t.Fatal("a stranger got in")
	}

	// Removed from the settings: shown as removed, and messages don't go.
	srv.SetRadio(nil, send)
	for _, u := range ana.must("GET", "/api/users", nil)["users"].([]any) {
		if uu := u.(map[string]any); uu["id"] == field && (uu["radio"] != "removed" || uu["onNetwork"] != false) {
			t.Fatalf("removed contact = %v", uu)
		}
	}
	ana.send(chatID, map[string]any{"type": "text", "text": "still there?"})
	last = ana.messages(chatID)
	if p := last[len(last)-1]; p["kind"] != "system" || !strings.Contains(p["payload"].(string), "removed from Sphynx") {
		t.Fatalf("last = %v", p)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
