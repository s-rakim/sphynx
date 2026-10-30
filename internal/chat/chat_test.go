package chat

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestDeviceUserID(t *testing.T) {
	a := DeviceUserID("7", "mkey:abc")
	if !regexp.MustCompile(`^d[a-z2-7]{15}$`).MatchString(a) {
		t.Fatalf("id %q", a)
	}
	if a != DeviceUserID("7", "mkey:abc") {
		t.Error("ids must be stable")
	}
	if a == DeviceUserID("7", "mkey:abd") || a == DeviceUserID("8", "mkey:abc") {
		t.Error("different devices share an id")
	}
}

func TestHostName(t *testing.T) {
	for in, want := range map[string]string{
		"sphynx-home":            "sphynx-home",
		"Sphynx-Home:80":         "sphynx-home",
		"100.64.0.1:3001":        "100.64.0.1",
		"[fd7a:115c:a1e0::1]:80": "fd7a:115c:a1e0::1",
		"chat.example.com.":      "chat.example.com",
		"[broken":                "",
	} {
		if got := hostName(in); got != want {
			t.Errorf("hostName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsEmoji(t *testing.T) {
	for _, e := range []string{"👍", "❤️", "👨‍👩‍👧", "🇰🇪", "1️⃣"} {
		if !isEmoji(e) {
			t.Errorf("%q should be an emoji", e)
		}
	}
	for _, e := range []string{"", "a", "ok", "<b>", "👍 nice"} {
		if isEmoji(e) {
			t.Errorf("%q is not an emoji", e)
		}
	}
}

func TestParseDevDirectory(t *testing.T) {
	d := ParseDevDirectory("phone:Android:Ana, laptop:Windows ,BAD NAME,tv")
	if len(d) != 3 {
		t.Fatalf("got %d devices: %+v", len(d), d)
	}
	if d[0].ID != "phone@dev" || d[0].Name != "Phone" || d[0].OS != "Android" || d[0].Owner != "Ana" {
		t.Errorf("phone %+v", d[0])
	}
	if d[1].OS != "Windows" || d[1].Owner != "" || d[2].OS != "dev" {
		t.Errorf("laptop %+v, tv %+v", d[1], d[2])
	}
}

// The app shell and the API only open for devices the resolver knows, on
// host names it accepts.
func TestHandlerChecksHostAndIdentity(t *testing.T) {
	srv, err := New(Config{DataDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	h := srv.Handler(DevResolver{})
	get := func(host, user, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = host
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set("Accept", "text/html")
		if user != "" {
			r.Header.Set("X-Dev-User", user)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := get("evil.example", "ana", "/"); w.Code != http.StatusMisdirectedRequest {
		t.Errorf("foreign host: %d", w.Code)
	}
	if w := get("localhost", "", "/"); w.Code != http.StatusFound {
		t.Errorf("no identity in dev mode should go to the login page: %d", w.Code)
	}
	w := get("localhost", "ana", "/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<title>Sphynx Chat</title>") {
		t.Errorf("app shell: %d", w.Code)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self' ws://localhost wss://localhost") {
		t.Errorf("CSP %q", csp)
	}
	if w := get("localhost", "ana", "/vendor/webcrypto-fallback.js"); w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Errorf("crypto fallback: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get("localhost", "ana", "/.hidden"); w.Code != http.StatusNotFound {
		t.Errorf("dot files: %d", w.Code)
	}
}

func TestBackup(t *testing.T) {
	srv, err := New(Config{DataDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.mu.Lock()
	srv.upsertUser(&Identity{ID: "ana@dev", Name: "Ana"})
	srv.mu.Unlock()
	var b strings.Builder
	if err := srv.Backup(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "SQLite format 3\x00") || !strings.Contains(b.String(), "ana@dev") {
		t.Errorf("backup is not the chat database (%d bytes)", b.Len())
	}
}
