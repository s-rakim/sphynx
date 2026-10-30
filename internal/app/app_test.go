package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDefaultSettingsValid(t *testing.T) {
	s := DefaultSettings()
	if err := s.Validate(); err != nil {
		t.Fatalf("default settings: %v", err)
	}
}

func TestSettingsValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		edit    func(*Settings)
		wantErr string
	}{
		"server URL with path":   {func(s *Settings) { s.ServerURL = "https://vpn.example.com/x" }, "path"},
		"server URL not http":    {func(s *Settings) { s.ServerURL = "vpn.example.com" }, "server address"},
		"relay without TLS":      {func(s *Settings) { s.DERP.Embedded = true }, "needs TLS"},
		"no relays at all":       {func(s *Settings) { s.DERP.UsePublic = false }, ""},
		"LAN route is a default": {func(s *Settings) { s.Home.LANRoutes = []string{"0.0.0.0/0"} }, ""},
		"bad expiry":             {func(s *Settings) { s.Nodes.Expiry = "ninety days" }, ""},
		"letsencrypt needs host": {func(s *Settings) { s.TLS.Mode = "letsencrypt"; s.ServerURL = "https://vpn.example.com" }, "hostname"},
	} {
		s := DefaultSettings()
		tc.edit(&s)
		err := s.Validate()
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error %q doesn't mention %q", name, err, tc.wantErr)
		}
	}

	s := DefaultSettings()
	s.ServerURL = "https://vpn.example.com"
	s.TLS.Mode = "letsencrypt"
	s.TLS.Hostname = "vpn.example.com"
	s.DERP.Embedded = true
	s.Home.LANRoutes = []string{"192.168.1.0/24"}
	if err := s.Validate(); err != nil {
		t.Errorf("internet setup: %v", err)
	}
}

func TestHeadscaleConfig(t *testing.T) {
	s := DefaultSettings()
	p := Paths{DataDir: "/data", RunDir: "/run/sphynx"}
	b, err := s.HeadscaleConfig(p, "")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		ServerURL  string `json:"server_url"`
		ListenAddr string `json:"listen_addr"`
		DERP       struct {
			URLs   []string `json:"urls"`
			Paths  []string `json:"paths"`
			Server struct {
				Enabled bool `json:"enabled"`
			} `json:"server"`
		} `json:"derp"`
		Policy struct {
			Mode string `json:"mode"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("config is not JSON: %v", err)
	}
	if cfg.ServerURL != s.ServerURL || cfg.ListenAddr != headscaleAddr {
		t.Errorf("server_url %q listen_addr %q", cfg.ServerURL, cfg.ListenAddr)
	}
	if len(cfg.DERP.URLs) != 0 {
		t.Errorf("Headscale must not download relay maps itself, got urls %v", cfg.DERP.URLs)
	}
	if len(cfg.DERP.Paths) != 1 || cfg.DERP.Paths[0] != p.PublicDERPMap() {
		t.Errorf("derp paths = %v", cfg.DERP.Paths)
	}
	if cfg.DERP.Server.Enabled {
		t.Error("built-in relay should be off without TLS")
	}
	if cfg.Policy.Mode != "database" {
		t.Errorf("policy mode = %q", cfg.Policy.Mode)
	}
	if strings.Contains(string(b), "oidc") {
		t.Error("OIDC section present while OIDC is off")
	}
}

func TestEffectivePolicy(t *testing.T) {
	// No managed rules: the admin's text (with comments) is kept as is.
	got, err := EffectivePolicy(DefaultPolicy, nil)
	if err != nil || got != DefaultPolicy {
		t.Fatalf("EffectivePolicy without rules changed the text (err %v)", err)
	}

	rules := ManagedRules(PolicyInputs{
		Resilience: DefaultSettings().Resilience,
		StrictIPs:  []string{"100.64.0.7", "fd7a:115c:a1e0::7"},
		HomeIPs:    []string{"100.64.0.1"},
		HomeRelay:  true,
	})
	if len(rules) != 3 {
		t.Fatalf("got %d managed rules, want 3", len(rules))
	}
	out, err := EffectivePolicy(DefaultPolicy, rules)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Grants    []map[string]any `json:"grants"`
		NodeAttrs []struct {
			Target []string `json:"target"`
			Attr   []string `json:"attr"`
		} `json:"nodeAttrs"`
		SSH []any `json:"ssh"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("effective policy is not JSON: %v", err)
	}
	if len(doc.Grants) != 3 || len(doc.SSH) != 1 {
		t.Errorf("grants %d ssh %d, want 3 and 1", len(doc.Grants), len(doc.SSH))
	}
	var strict bool
	for _, a := range doc.NodeAttrs {
		if len(a.Attr) == 1 && a.Attr[0] == "only-tcp-443" {
			strict = strings.Join(a.Target, ",") == "100.64.0.7/32,fd7a:115c:a1e0::7/128"
		}
	}
	if !strict {
		t.Errorf("strict firewall rule missing or wrong: %+v", doc.NodeAttrs)
	}

	if _, err := EffectivePolicy(`{"grants": {"not": "a list"}}`, rules); err == nil {
		t.Error("grants that aren't a list should be rejected")
	}
	if _, err := EffectivePolicy(`{"grants": [`, nil); err == nil {
		t.Error("broken HuJSON should be rejected")
	}
}

func TestDERPMapYAML(t *testing.T) {
	y, err := derpMapYAML(fallbackDERPMap)
	if err != nil {
		t.Fatal(err)
	}
	s := string(y)
	for _, want := range []string{"\"regions\":\n", "\n  1:\n", "    \"regionid\": 1\n", "    \"nodes\":\n", "      - \"canport80\": true\n", "        \"name\": \"1c\"\n", "        \"hostname\": \"derp1"} {
		if !strings.Contains(s, want) {
			t.Errorf("relay map YAML lacks %q", want)
		}
	}
	if strings.ContainsAny(s, "\t") {
		t.Error("YAML must not contain tabs")
	}
	if _, err := derpMapYAML([]byte(`{"Regions": {}}`)); err == nil {
		t.Error("an empty relay map should be rejected")
	}
}

func TestDropCookie(t *testing.T) {
	h := http.Header{}
	h.Add("Cookie", "a=1; sphynx_session=secret; b=2")
	h.Add("Cookie", "sphynx_session=other")
	dropCookie(h, sessionCookie)
	if got := h.Get("Cookie"); got != "a=1; b=2" || len(h.Values("Cookie")) != 1 {
		t.Errorf("Cookie = %q", h.Values("Cookie"))
	}
	h = http.Header{}
	h.Set("Cookie", "sphynx_session=x")
	dropCookie(h, sessionCookie)
	if _, ok := h["Cookie"]; ok {
		t.Error("empty Cookie header left behind")
	}
}

func TestKeepHeadscaleLine(t *testing.T) {
	noisy := `2026-10-29T19:39:56Z INF http request bytes=12 elapsed=70 method=GET path=/api/v1/user proto=HTTP/1.1 remote=127.0.0.1:49502 status=200`
	if keepHeadscaleLine(noisy) {
		t.Error("Sphynx's own successful API calls should be dropped")
	}
	for _, keep := range []string{
		strings.Replace(noisy, "status=200", "status=500", 1),
		`2026-10-29T19:39:55Z INF listening and serving HTTP on: 127.0.0.1:18080`,
		`2026-10-29T19:40:01Z INF http request method=POST path=/ts2021 remote=203.0.113.9:5000 status=101`,
	} {
		if !keepHeadscaleLine(keep) {
			t.Errorf("dropped %q", keep)
		}
	}
}

func TestLoginLimiter(t *testing.T) {
	l := &loginLimiter{failures: map[string][]time.Time{}}
	for i := 0; i < 3; i++ {
		if !l.allowed("1.2.3.4", 3) {
			t.Fatalf("blocked after %d failures", i)
		}
		l.fail("1.2.3.4")
	}
	if l.allowed("1.2.3.4", 3) {
		t.Error("not blocked after 3 failures")
	}
	if !l.allowed("5.6.7.8", 3) {
		t.Error("other address blocked")
	}
	l.reset("1.2.3.4")
	if !l.allowed("1.2.3.4", 3) {
		t.Error("still blocked after reset")
	}
}

func TestLinkSettings(t *testing.T) {
	l := LinkSettings{ExitNode: true, Admin: true, Allow: []LinkClient{{Key: "nodekey:" + strings.Repeat("ab", 32)}}}
	if err := l.validate(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.spec("3000"), ","); got != "exit-node,3000" {
		t.Errorf("spec = %s", got)
	}
	l.Allow[0].Key = "nodekey:123"
	if l.validate() == nil {
		t.Error("short key accepted")
	}
}

func TestLastComplaint(t *testing.T) {
	lines := []string{
		"00:36:17 2026/10/30 00:36:17 Expand: fetching DERPMap: no such host",
		"sphynx: tailcat exited (exit status 1), restarting in 1s",
	}
	if got := lastComplaint(lines); got != "Expand: fetching DERPMap: no such host" {
		t.Errorf("lastComplaint = %q", got)
	}
	if got := lastComplaint([]string{"sphynx: starting"}); got != "" {
		t.Errorf("lastComplaint of Sphynx's own lines = %q", got)
	}
}

func TestSigninPages(t *testing.T) {
	for _, tc := range []struct {
		d    signinData
		want []string
		not  []string
	}{
		{signinData{Owner: "malcolm"}, []string{"Add this device", "Device belongs to", "Approve device"}, []string{"Confirm it"}},
		{signinData{Done: true, Device: "pixel"}, []string{"Device added", "<strong>pixel</strong>"}, []string{"Allowed"}},
		{signinData{Check: true, Insecure: true}, []string{"Confirm it's you", "Tailscale SSH", ">Allow<", "HTTPS is off"}, []string{"Device belongs to", "Approve device"}},
		{signinData{Check: true, Error: "That isn't the Sphynx admin password."}, []string{`role="alert"`}, nil},
		{signinData{Check: true, Done: true}, []string{"Allowed", "go back to the device"}, []string{"<form"}},
	} {
		var b strings.Builder
		if err := signinTmpl.Execute(&b, tc.d); err != nil {
			t.Fatal(err)
		}
		for _, s := range tc.want {
			if !strings.Contains(b.String(), s) {
				t.Errorf("%+v: page lacks %q", tc.d, s)
			}
		}
		for _, s := range tc.not {
			if strings.Contains(b.String(), s) {
				t.Errorf("%+v: page has %q", tc.d, s)
			}
		}
	}
}
