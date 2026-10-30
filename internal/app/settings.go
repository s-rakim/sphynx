package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Settings is everything an admin can change from the Settings and DNS pages.
// Sphynx turns it into Headscale's config.yaml on every change.
type Settings struct {
	// ServerURL is the address devices use to reach the control server.
	ServerURL string `json:"serverUrl"`
	// SetupDone is set once the first-run wizard has finished.
	SetupDone bool `json:"setupDone"`
	// Owner is the Headscale user whose helper node Sphynx uses to look at the
	// tailnet (connection details, public sharing).
	Owner string `json:"owner"`

	TLS        TLSSettings        `json:"tls"`
	DERP       DERPSettings       `json:"derp"`
	DNS        DNSSettings        `json:"dns"`
	Prefixes   PrefixSettings     `json:"prefixes"`
	Nodes      NodeSettings       `json:"nodes"`
	OIDC       OIDCSettings       `json:"oidc"`
	Home       HomeSettings       `json:"home"`
	Resilience ResilienceSettings `json:"resilience"`
	Link       LinkSettings       `json:"link"`
	Share      ShareSettings      `json:"share"`
	WoL        WoLSettings        `json:"wol"`
	Chat       ChatSettings       `json:"chat"`
	Radio      RadioSettings      `json:"radio"`
	Features   FeatureSettings    `json:"features"`
	LogLevel   string             `json:"logLevel"`
}

// HomeSettings controls "Sphynx home", the device Sphynx itself runs on the
// tailnet from inside the container.
type HomeSettings struct {
	// ExitNode lets remote devices send all their internet traffic out
	// through this PC's internet connection.
	ExitNode bool `json:"exitNode"`
	// LANRoutes are home subnets (e.g. 192.168.1.0/24) made reachable to
	// remote devices through Sphynx home.
	LANRoutes []string `json:"lanRoutes"`
	// RelayPort, when non-zero, makes Sphynx home a UDP peer relay: devices
	// that cannot reach each other directly relay through it instead of the
	// slower TCP/443 DERP relays. Publish this UDP port on the router.
	RelayPort int `json:"relayPort"`
	// RelayEndpoints are public ip:port pairs to advertise for the relay
	// (needed because the container sits behind Docker's and the router's NAT).
	RelayEndpoints []string `json:"relayEndpoints"`
}

// ResilienceSettings switch on Tailscale client behaviors that help on weak,
// patchy or heavily filtered connections. They are delivered to devices as
// policy node attributes.
type ResilienceSettings struct {
	// CacheNetmaps: devices keep their peer list on disk and can reconnect
	// to peers after a restart even while the control server is unreachable.
	CacheNetmaps bool `json:"cacheNetmaps"`
	// ClientReachability: devices judge for themselves whether a peer is
	// reachable instead of trusting the server's online flag, so a flaky
	// link to the server doesn't drop working tunnels.
	ClientReachability bool `json:"clientReachability"`
	// ProbeUDPLifetime: devices measure how long their NAT keeps UDP mappings
	// alive (short on cellular) and keep direct paths from silently dying.
	ProbeUDPLifetime bool `json:"probeUdpLifetime"`
	// AutoExitNode: devices may use "auto" exit node selection, which picks
	// the best available exit node and fails over automatically.
	AutoExitNode bool `json:"autoExitNode"`
	// PeerMTU: devices discover the largest packet size each path allows,
	// avoiding stalls on satellite and cellular links with small MTUs.
	PeerMTU bool `json:"peerMtu"`
}

type TLSSettings struct {
	// Mode is "none" (plain HTTP, LAN or behind a reverse proxy),
	// "letsencrypt" (Headscale gets a certificate itself) or "manual".
	Mode          string `json:"mode"`
	Hostname      string `json:"hostname"`
	Email         string `json:"email"`
	ChallengeType string `json:"challengeType"` // HTTP-01 or TLS-ALPN-01
	CertPath      string `json:"certPath"`
	KeyPath       string `json:"keyPath"`
}

type DERPSettings struct {
	// Embedded runs Headscale's own relay + STUN server. Clients only talk to
	// DERP over TLS, so this needs TLS mode letsencrypt or manual.
	Embedded bool   `json:"embedded"`
	IPv4     string `json:"ipv4"`
	IPv6     string `json:"ipv6"`
	// UsePublic adds Tailscale's free, globally distributed relays. They only
	// ever see encrypted WireGuard packets.
	UsePublic bool `json:"usePublic"`
}

type DNSSettings struct {
	MagicDNS         bool                `json:"magicDns"`
	BaseDomain       string              `json:"baseDomain"`
	OverrideLocalDNS bool                `json:"overrideLocalDns"`
	Nameservers      []string            `json:"nameservers"`
	SplitDNS         map[string][]string `json:"splitDns"`
	SearchDomains    []string            `json:"searchDomains"`
}

type PrefixSettings struct {
	V4         string `json:"v4"`
	V6         string `json:"v6"`
	Allocation string `json:"allocation"` // sequential or random
}

type NodeSettings struct {
	// Expiry is how long a login lasts before the device must re-authenticate,
	// e.g. "180d". "0" means never.
	Expiry           string `json:"expiry"`
	EphemeralTimeout string `json:"ephemeralTimeout"`
}

type OIDCSettings struct {
	Enabled        bool     `json:"enabled"`
	Issuer         string   `json:"issuer"`
	ClientID       string   `json:"clientId"`
	ClientSecret   string   `json:"clientSecret,omitempty"`
	Scopes         []string `json:"scopes"`
	AllowedDomains []string `json:"allowedDomains"`
	AllowedUsers   []string `json:"allowedUsers"`
	AllowedGroups  []string `json:"allowedGroups"`
	PKCE           bool     `json:"pkce"`
}

type ShareSettings struct {
	// Enabled turns on the public sharing listener (Sphynx's Funnel stand-in).
	Enabled bool `json:"enabled"`
}

type WoLSettings struct {
	// Broadcasts are extra addresses used when the server itself sends the
	// magic packet (e.g. "192.168.1.255").
	Broadcasts []string `json:"broadcasts"`
}

type FeatureSettings struct {
	Taildrop   bool `json:"taildrop"`
	AutoUpdate bool `json:"autoUpdate"`
}

func DefaultSettings() Settings {
	return Settings{
		ServerURL: "http://localhost:8080",
		TLS:       TLSSettings{Mode: "none", ChallengeType: "HTTP-01"},
		DERP:      DERPSettings{UsePublic: true},
		DNS: DNSSettings{
			MagicDNS:         true,
			BaseDomain:       "sphynx.internal",
			OverrideLocalDNS: true,
			Nameservers:      []string{"1.1.1.1", "1.0.0.1", "2606:4700:4700::1111", "2606:4700:4700::1001"},
			SplitDNS:         map[string][]string{},
			SearchDomains:    []string{},
		},
		Prefixes: PrefixSettings{V4: "100.64.0.0/10", V6: "fd7a:115c:a1e0::/48", Allocation: "sequential"},
		Nodes:    NodeSettings{Expiry: "0", EphemeralTimeout: "30m"},
		OIDC:     OIDCSettings{Scopes: []string{"openid", "profile", "email"}},
		Home:     HomeSettings{ExitNode: true, LANRoutes: []string{}, RelayEndpoints: []string{}},
		Resilience: ResilienceSettings{
			CacheNetmaps:       true,
			ClientReachability: true,
			ProbeUDPLifetime:   true,
			AutoExitNode:       true,
		},
		Link:     LinkSettings{ExitNode: true, Inbox: true, Allow: []LinkClient{}},
		Chat:     ChatSettings{Enabled: true, MaxUploadMB: 2048, Secure: true},
		Radio:    defaultRadioSettings(),
		Features: FeatureSettings{Taildrop: true},
		LogLevel: "info",
	}
}

var (
	hostnameRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	durationRe = regexp.MustCompile(`^(0|([0-9]+(ms|s|m|h|d|w|y))+)$`)
)

// Validate catches mistakes before they reach Headscale, which would
// otherwise refuse to start.
func (s *Settings) Validate() error {
	u, err := url.Parse(s.ServerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("server address must look like http://192.168.1.20:8080 or https://vpn.example.com")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("server address must not contain a path")
	}
	switch s.TLS.Mode {
	case "none":
	case "letsencrypt":
		if !hostnameRe.MatchString(s.TLS.Hostname) {
			return errors.New("Let's Encrypt needs a public hostname, like vpn.example.com")
		}
		if s.TLS.ChallengeType != "HTTP-01" && s.TLS.ChallengeType != "TLS-ALPN-01" {
			return errors.New("challenge type must be HTTP-01 or TLS-ALPN-01")
		}
	case "manual":
		if s.TLS.CertPath == "" || s.TLS.KeyPath == "" {
			return errors.New("manual TLS needs both a certificate path and a key path")
		}
	default:
		return fmt.Errorf("unknown TLS mode %q", s.TLS.Mode)
	}
	if s.TLS.Mode != "none" && u.Scheme != "https" {
		return errors.New("with TLS turned on the server address must start with https://")
	}
	if s.DERP.Embedded && s.TLS.Mode == "none" {
		return errors.New("the built-in relay (DERP) needs TLS: devices only connect to relays over HTTPS")
	}
	if !s.DERP.Embedded && !s.DERP.UsePublic {
		return errors.New("turn on at least one relay option, or devices behind strict NATs will not connect")
	}
	for _, ip := range []string{s.DERP.IPv4, s.DERP.IPv6} {
		if ip != "" {
			if _, err := netip.ParseAddr(ip); err != nil {
				return fmt.Errorf("%q is not an IP address", ip)
			}
		}
	}
	if s.DNS.MagicDNS || s.DNS.BaseDomain != "" {
		if !hostnameRe.MatchString(s.DNS.BaseDomain) || !strings.Contains(s.DNS.BaseDomain, ".") {
			return errors.New("the MagicDNS domain must look like sphynx.internal")
		}
		host := strings.ToLower(u.Hostname())
		bd := strings.ToLower(s.DNS.BaseDomain)
		if host == bd || strings.HasSuffix(host, "."+bd) {
			return errors.New("the MagicDNS domain can't contain the server's own hostname, or devices could lose the server")
		}
	}
	if s.DNS.OverrideLocalDNS && len(s.DNS.Nameservers) == 0 {
		return errors.New("add at least one global nameserver, or turn off \"override local DNS\"")
	}
	for _, ns := range s.DNS.Nameservers {
		if err := validNameserver(ns); err != nil {
			return err
		}
	}
	for dom, nss := range s.DNS.SplitDNS {
		if !hostnameRe.MatchString(dom) {
			return fmt.Errorf("split DNS domain %q is not a valid domain", dom)
		}
		for _, ns := range nss {
			if err := validNameserver(ns); err != nil {
				return err
			}
		}
	}
	for _, p := range []string{s.Prefixes.V4, s.Prefixes.V6} {
		if _, err := netip.ParsePrefix(p); err != nil {
			return fmt.Errorf("%q is not a valid IP range", p)
		}
	}
	if s.Prefixes.Allocation != "sequential" && s.Prefixes.Allocation != "random" {
		return errors.New("IP allocation must be sequential or random")
	}
	if !durationRe.MatchString(s.Nodes.Expiry) {
		return errors.New("key expiry must be 0 (never) or a duration like 90d")
	}
	if !durationRe.MatchString(s.Nodes.EphemeralTimeout) {
		return errors.New("ephemeral timeout must be a duration like 30m")
	}
	if s.OIDC.Enabled && (s.OIDC.Issuer == "" || s.OIDC.ClientID == "") {
		return errors.New("single sign-on needs an issuer URL and a client ID")
	}
	for _, r := range s.Home.LANRoutes {
		p, err := netip.ParsePrefix(r)
		if err != nil {
			return fmt.Errorf("home network %q must look like 192.168.1.0/24", r)
		}
		if p.Bits() == 0 {
			return errors.New("use the exit node switch to share the whole internet, not a 0.0.0.0/0 route")
		}
	}
	if s.Home.RelayPort < 0 || s.Home.RelayPort > 65535 {
		return errors.New("relay port must be between 1 and 65535")
	}
	for _, ep := range s.Home.RelayEndpoints {
		if _, err := netip.ParseAddrPort(ep); err != nil {
			return fmt.Errorf("relay endpoint %q must look like 203.0.113.7:40000", ep)
		}
	}
	for _, b := range s.WoL.Broadcasts {
		if net.ParseIP(b).To4() == nil {
			return fmt.Errorf("%q is not an IPv4 broadcast address", b)
		}
	}
	if err := s.Link.validate(); err != nil {
		return err
	}
	if err := s.Chat.validate(); err != nil {
		return err
	}
	switch s.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log level must be debug, info, warn or error")
	}
	return nil
}

// validNameserver accepts IPs and DoH URLs, like Headscale does.
func validNameserver(ns string) error {
	if _, err := netip.ParseAddr(ns); err == nil {
		return nil
	}
	if u, err := url.Parse(ns); err == nil && u.Scheme == "https" && u.Host != "" {
		return nil
	}
	return fmt.Errorf("nameserver %q must be an IP address or an https:// DNS-over-HTTPS URL", ns)
}

// Secrets never leave the server.
type Secrets struct {
	APIKey       string `json:"apiKey"`
	SessionKey   string `json:"sessionKey"`
	PasswordHash string `json:"passwordHash"`
	PasswordSalt string `json:"passwordSalt"`
	OIDCSecret   string `json:"oidcSecret"`
	// APRSPasscode signs Sphynx in to APRS-IS so it can send.
	APRSPasscode string `json:"aprsPasscode,omitempty"`
	// RadioTOTP is the secret behind the wake codes accepted over radio.
	RadioTOTP string `json:"radioTotp,omitempty"`
}

// jsonFile is a JSON document on disk guarded by a mutex.
type jsonFile[T any] struct {
	mu   sync.Mutex
	path string
	v    T
}

func loadJSON[T any](path string, def T) (*jsonFile[T], error) {
	f := &jsonFile[T]{path: path, v: def}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, f.save()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &f.v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func (f *jsonFile[T]) Get() T {
	f.mu.Lock()
	defer f.mu.Unlock()
	return deepCopy(f.v)
}

// Update applies fn to a copy and saves it if fn returns nil.
func (f *jsonFile[T]) Update(fn func(*T) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := deepCopy(f.v)
	if err := fn(&v); err != nil {
		return err
	}
	old := f.v
	f.v = v
	if err := f.save(); err != nil {
		f.v = old
		return err
	}
	return nil
}

func (f *jsonFile[T]) save() error {
	b, err := json.MarshalIndent(f.v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(f.path, b, 0o600)
}

func deepCopy[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
