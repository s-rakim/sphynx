package app

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/s-rakim/sphynx/internal/chat"
	"github.com/s-rakim/sphynx/internal/hs"
	"github.com/s-rakim/sphynx/internal/proc"
	"github.com/s-rakim/sphynx/internal/radio"
	"github.com/s-rakim/sphynx/internal/radio/radiotest"
)

func TestTOTP(t *testing.T) {
	// RFC 6238 appendix B (SHA-1), last six digits.
	key := []byte("12345678901234567890")
	for step, want := range map[int64]string{59 / 30: "287082", 1111111109 / 30: "081804", 1234567890 / 30: "005924", 2000000000 / 30: "279037"} {
		if got := totpCode(key, step); got != want {
			t.Errorf("step %d: %s, want %s", step, got, want)
		}
	}
	secret := b32.EncodeToString(key)
	at := time.Unix(1234567890, 0)
	if step, ok := totpCheck(secret, "005924", at); !ok || step != 1234567890/30 {
		t.Fatal("current code refused")
	}
	if _, ok := totpCheck(secret, "005924", at.Add(30*time.Second)); !ok {
		t.Fatal("code from the previous step refused")
	}
	if _, ok := totpCheck(secret, "005924", at.Add(90*time.Second)); ok {
		t.Fatal("old code accepted")
	}
	if _, ok := totpCheck(secret, "5924", at); ok {
		t.Fatal("short code accepted")
	}
	if len(newTOTPSecret()) != 32 || !strings.HasPrefix(totpURI("ABC"), "otpauth://totp/Sphynx:radio%20wake?secret=ABC&issuer=Sphynx") {
		t.Fatal("secret or URI")
	}
	qr, err := qrDataURL(totpURI(newTOTPSecret()))
	if err != nil || !strings.HasPrefix(qr, "data:image/svg+xml;base64,") {
		t.Fatalf("qr: %v", err)
	}
	svg, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(qr, "data:image/svg+xml;base64,"))
	if !strings.HasPrefix(string(svg), "<svg") || strings.Count(string(svg), "h1v1") < 200 {
		t.Fatalf("qr svg: %.100s", svg)
	}
}

func TestRadioSettings(t *testing.T) {
	ok := defaultRadioSettings()
	ok.Enabled = true
	ok.Meshtastic = MeshtasticSettings{Enabled: true, Address: "192.168.1.50"}
	ok.APRS.Enabled, ok.APRS.Call = true, " 5z4abc-10 "
	ok.Contacts = []RadioContact{
		{Name: "My T-Echo", Link: "meshtastic", Address: "A1B2C3D4", Commands: true, Chat: true},
		{Name: "Handheld", Link: "aprs", Address: "5z4abc-7"},
	}
	if err := ok.normalize(); err != nil {
		t.Fatal(err)
	}
	if ok.APRS.Call != "5Z4ABC-10" || ok.Contacts[0].Address != "!a1b2c3d4" || ok.Contacts[1].Address != "5Z4ABC-7" || ok.Contacts[0].ID == "" {
		t.Fatalf("normalized: %+v", ok)
	}
	for _, addr := range []string{"/dev/ttyUSB0", "meshtastic.local", "10.0.0.5:4403", "[fd00::5]:4403"} {
		s := defaultRadioSettings()
		s.Meshtastic = MeshtasticSettings{Enabled: true, Address: addr}
		if err := s.normalize(); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	bad := map[string]func(*RadioSettings){
		"no radio address":  func(s *RadioSettings) { s.Meshtastic.Enabled = true },
		"bad radio address": func(s *RadioSettings) { s.Meshtastic = MeshtasticSettings{Enabled: true, Address: "http://x/"} },
		"no callsign":       func(s *RadioSettings) { s.APRS.Enabled = true },
		"no APRS link":      func(s *RadioSettings) { s.APRS = APRSSettings{Enabled: true, Call: "N0CALL"} },
		"bad path":          func(s *RadioSettings) { s.APRS.Path = []string{"WIDE1-99"} },
		"bad node id":       func(s *RadioSettings) { s.Contacts = []RadioContact{{Name: "x", Link: "meshtastic", Address: "!zz"}} },
		"no name":           func(s *RadioSettings) { s.Contacts = []RadioContact{{Link: "aprs", Address: "N0CALL"}} },
		"bad link":          func(s *RadioSettings) { s.Contacts = []RadioContact{{Name: "x", Link: "cb", Address: "N0CALL"}} },
		"twice": func(s *RadioSettings) {
			s.Contacts = []RadioContact{{Name: "a", Link: "aprs", Address: "N0CALL"}, {Name: "b", Link: "aprs", Address: "n0call"}}
		},
	}
	for name, edit := range bad {
		s := defaultRadioSettings()
		edit(&s)
		if s.normalize() == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// testRadioApp is just enough of Sphynx to run radio commands: Headscale
// is unreachable, so device lookups fail.
func testRadioApp(t *testing.T, edit func(*Settings, *Secrets, *State)) *App {
	s, sec, st := DefaultSettings(), Secrets{}, defaultState()
	edit(&s, &sec, &st)
	srv, err := chat.New(chat.Config{DataDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	a := &App{
		settings: &jsonFile[Settings]{path: filepath.Join(dir, "settings.json"), v: s},
		secrets:  &jsonFile[Secrets]{path: filepath.Join(dir, "secrets.json"), v: sec},
		state:    &jsonFile[State]{path: filepath.Join(dir, "state.json"), v: st},
		logs:     proc.NewRing(100),
		hs:       hs.New("http://127.0.0.1:1", nil),
		ctx:      ctx,
	}
	a.chat = &chatService{a: a, srv: srv, dir: &chatDirectory{a: a}}
	a.helpers = newHelperSet(a)
	a.radio = newRadioService(a)
	return a
}

func TestRadioCommands(t *testing.T) {
	secret := newTOTPSecret()
	a := testRadioApp(t, func(s *Settings, sec *Secrets, st *State) {
		sec.RadioTOTP = secret
		st.WakeTargets = []*WakeTarget{{ID: "1", Name: "Gaming PC", MAC: "3c:7c:3f:12:34:56", Method: "relay", RelayNodeID: "9"}}
	})
	r := a.radio
	for text, want := range map[string]radioCommand{
		"help":                   {name: "help"},
		" /PING ":                {name: "ping"},
		"status":                 {name: "status"},
		"wake gaming pc 123456":  {name: "wake", arg: "gaming pc", code: "123456"},
		"Wake GamingPC":          {name: "wake", arg: "GamingPC"},
		"wake desktop 654321":    {name: "wake", arg: "desktop", code: "654321"},
		"help me with the tent":  {},
		"status update: arrived": {}, // no such device (and no Headscale here)
		"wake up, we're leaving": {},
		"see you at 7":           {},
	} {
		got, ok := r.parseCommand(text)
		if ok != (want.name != "") || got != want {
			t.Errorf("parseCommand(%q) = %+v, %v; want %+v", text, got, ok, want)
		}
	}

	c := RadioContact{Name: "Field", Link: "aprs", Address: "5Z4ABC-7", Commands: true}
	if s := r.run(a.ctx, c, radio.Message{}, radioCommand{name: "help"}); !strings.Contains(s, "wake <computer> <code>") || strings.Contains(s, "Sphynx Chat") {
		t.Errorf("help = %q", s)
	}
	if s := r.wake(c, radioCommand{name: "wake", arg: "desktop", code: "123456"}); !strings.Contains(s, "no computer called desktop. Computers: Gaming PC") {
		t.Errorf("unknown computer: %q", s)
	}
	if s := r.wake(c, radioCommand{name: "wake", arg: "gaming pc"}); !strings.Contains(s, "add the 6-digit code") {
		t.Errorf("no code: %q", s)
	}

	// A right code works once; guessing locks waking.
	key, _ := b32.DecodeString(secret)
	code := totpCode(key, time.Now().Unix()/30)
	if err := r.checkCode(code); err != nil {
		t.Fatalf("right code: %v", err)
	}
	if err := r.checkCode(code); err == nil {
		t.Fatal("a used code worked again")
	}
	for range 4 {
		_ = r.checkCode("000000")
	}
	next := totpCode(key, time.Now().Unix()/30+1)
	if err := r.checkCode(next); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("after 5 wrong codes: %v", err)
	}
}

func TestRadioOverMeshtastic(t *testing.T) {
	fake, err := radiotest.NewMeshtastic("127.0.0.1:0", 0x0badcafe, "Home")
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()
	fake.AddNode(radiotest.MeshNode{Num: 0xa1b2c3d4, LongName: "Field"})
	fake.AddNode(radiotest.MeshNode{Num: 0x55555555, LongName: "Stranger"})
	a := testRadioApp(t, func(s *Settings, sec *Secrets, st *State) {
		s.Radio.Enabled = true
		s.Radio.Meshtastic = MeshtasticSettings{Enabled: true, Address: fake.Addr()}
		s.Radio.Contacts = []RadioContact{{ID: "c1", Name: "Field", Link: "meshtastic", Address: "!a1b2c3d4", Commands: true}}
	})
	a.radio.apply(a.ctx)
	waitFor(t, func() bool { return fake.Connected() && a.radio.mesh.Status()[0].Connected })

	_ = fake.Inject(radiotest.MeshIn{From: 0xa1b2c3d4, Text: "ping", SNR: 6})
	_ = fake.Inject(radiotest.MeshIn{From: 0x55555555, Text: "status"})
	_ = fake.Inject(radiotest.MeshIn{From: 0xa1b2c3d4, Text: "hello?"})
	waitFor(t, func() bool { return len(fake.Sent()) == 2 })
	var texts []string
	for _, s := range fake.Sent() {
		if s.To != 0xa1b2c3d4 {
			t.Fatalf("sent to %x", s.To)
		}
		texts = append(texts, s.Text)
	}
	slices.Sort(texts)
	if texts[0] != "Sphynx: pong (heard you directly: SNR 6.0 dB, RSSI -80 dBm)" || texts[1] != "Sphynx: unknown command. Send help for the list." {
		t.Fatalf("replies = %q", texts)
	}
	a.radio.mu.Lock()
	heard := a.radio.heard
	a.radio.mu.Unlock()
	if len(heard) != 1 || heard[0].From != "!55555555" || heard[0].Name != "Stranger" {
		t.Fatalf("heard = %+v", heard)
	}
	waitFor(t, func() bool {
		a.radio.mu.Lock()
		defer a.radio.mu.Unlock()
		return len(a.radio.log) == 4 // ping, its reply, hello?, its reply
	})

	// Turning radio mode off disconnects the radio.
	_ = a.settings.Update(func(s *Settings) error { s.Radio.Enabled = false; return nil })
	a.radio.apply(a.ctx)
	waitFor(t, func() bool { return !fake.Connected() })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
