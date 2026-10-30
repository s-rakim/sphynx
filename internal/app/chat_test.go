package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/s-rakim/sphynx/internal/chat"
	"github.com/s-rakim/sphynx/internal/hs"
)

func testChat(edit func(*Settings)) *chatService {
	s := DefaultSettings()
	if edit != nil {
		edit(&s)
	}
	a := &App{settings: &jsonFile[Settings]{v: s}}
	return &chatService{a: a}
}

func internetTLS(s *Settings) {
	s.ServerURL = "https://vpn.example.com"
	s.TLS.Mode = "letsencrypt"
	s.TLS.Hostname = "vpn.example.com"
}

func TestChatSettingsValidate(t *testing.T) {
	if err := DefaultSettings().Chat.validate(); err != nil {
		t.Fatalf("default chat settings: %v", err)
	}
	for name, c := range map[string]ChatSettings{
		"no uploads":        {Enabled: true, MaxUploadMB: 0},
		"huge uploads":      {Enabled: true, MaxUploadMB: 20000},
		"short secure host": {Enabled: true, MaxUploadMB: 10, SecureHost: "chat"},
		"bad secure host":   {Enabled: true, MaxUploadMB: 10, SecureHost: "chat example.com"},
	} {
		if c.validate() == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := (ChatSettings{MaxUploadMB: 10, SecureHost: "chat.example.com"}).validate(); err != nil {
		t.Errorf("full host name: %v", err)
	}
}

func TestChatSecureHost(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*Settings)
		want string
	}{
		"plain http":   {nil, ""},
		"lets encrypt": {internetTLS, "chat.vpn.example.com"},
		"own host":     {func(s *Settings) { internetTLS(s); s.Chat.SecureHost = "Talk.Example.com" }, "talk.example.com"},
		"secure off":   {func(s *Settings) { internetTLS(s); s.Chat.Secure = false }, ""},
		"chat off":     {func(s *Settings) { internetTLS(s); s.Chat.Enabled = false }, ""},
		"from server URL": {func(s *Settings) {
			s.ServerURL = "https://home.example.org:8443"
			s.TLS.Mode = "manual"
		}, "chat.home.example.org"},
		"IP address": {func(s *Settings) {
			s.ServerURL = "https://203.0.113.7"
			s.TLS.Mode = "manual"
		}, ""},
	} {
		if got := testChat(tc.edit).secureHost(); got != tc.want {
			t.Errorf("%s: secure host %q, want %q", name, got, tc.want)
		}
	}
}

func TestChatAllowedHost(t *testing.T) {
	c := testChat(func(s *Settings) {
		internetTLS(s)
		s.DNS.BaseDomain = "sphynx.internal"
	})
	tn := chatTailnet{c}
	for host, want := range map[string]bool{
		"sphynx-home":             true,
		"100.64.0.1":              true,
		"fd7a:115c:a1e0::1":       true,
		"localhost":               true,
		"127.0.0.1":               true,
		"laptop.sphynx.internal":  true,
		"chat.vpn.example.com":    true,
		"vpn.example.com":         false,
		"evil.example":            false,
		"192.168.1.5":             false,
		"sphynx.internal.evil.io": false,
	} {
		if got := tn.AllowedHost(host); got != want {
			t.Errorf("AllowedHost(%q) = %v, want %v", host, got, want)
		}
	}
	local := chatLocal{c}
	if !local.AllowedHost("localhost") || local.AllowedHost("sphynx-home") {
		t.Error("the PC's own chat only answers to localhost")
	}
}

func TestChatIdentity(t *testing.T) {
	n := hs.Node{ID: "7", MachineKey: "mkey:abc", Name: "pixel-7", GivenName: "malcolms-phone", User: &hs.User{Name: "malcolm", DisplayName: "Malcolm"}}
	id := chatIdentity(n, "android")
	if id.ID != chat.DeviceUserID("7", "mkey:abc") || id.Name != "malcolms-phone" || id.Owner != "Malcolm" || id.OS != "android" || id.NodeID != "7" {
		t.Errorf("identity %+v", id)
	}
	if other := chatIdentity(hs.Node{ID: "7", MachineKey: "mkey:def"}, ""); other.ID == id.ID {
		t.Error("a node id handed out again must not inherit the old device's chats")
	}
	if tagged := chatIdentity(hs.Node{ID: "8", Tags: []string{"tag:tv"}, User: &hs.User{Name: "malcolm"}}, ""); tagged.Owner != "tag:tv" {
		t.Errorf("tagged device owner %q", tagged.Owner)
	}
}

func TestChatPolicyGrant(t *testing.T) {
	in := PolicyInputs{Chat: true, HomeIPs: []string{"100.64.0.1", "fd7a:115c:a1e0::1"}}
	var grant *ManagedRule
	for _, r := range ManagedRules(in) {
		if r.Section == "grants" && strings.Contains(r.Description, "Chat") {
			grant = &r
		}
	}
	if grant == nil {
		t.Fatal("no chat grant")
	}
	b, _ := json.Marshal(grant.JSON)
	if got := string(b); !strings.Contains(got, `"dst":["100.64.0.1/32","fd7a:115c:a1e0::1/128"]`) || !strings.Contains(got, `"ip":["tcp:80","tcp:443"]`) {
		t.Errorf("chat grant %s", got)
	}
	in.Chat = false
	for _, r := range ManagedRules(in) {
		if strings.Contains(r.Description, "Chat") {
			t.Error("chat grant while the chat is off")
		}
	}
}
