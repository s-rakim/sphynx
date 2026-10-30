// Package tsnode runs official tailscaled instances in userspace-networking
// mode inside the Sphynx container. Sphynx uses them as its own presence on
// the tailnet: to relay Wake-on-LAN packets, to read live connection details,
// to proxy public shares, and as the built-in "home" exit node.
package tsnode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/s-rakim/sphynx/internal/proc"
)

// Node is one tailscaled process plus the CLI used to drive it.
type Node struct {
	Name      string // stable identifier, e.g. "u1"
	Dir       string // state directory
	Socket    string
	SocksPort int
	WGPort    int // 0 picks a random UDP port
	CLI       string
	Proc      *proc.Process

	localOnce sync.Once
	local     *http.Client
}

func New(name, stateDir, runDir, tailscaled, cli string, socksPort, wgPort int) *Node {
	n := &Node{
		Name:      name,
		Dir:       stateDir,
		Socket:    filepath.Join(runDir, "tailscaled-"+name+".sock"),
		SocksPort: socksPort,
		WGPort:    wgPort,
		CLI:       cli,
	}
	n.Proc = proc.New("tailscaled-"+name, func() []string {
		return []string{
			tailscaled,
			"--tun=userspace-networking",
			"--statedir=" + n.Dir,
			"--socket=" + n.Socket,
			"--socks5-server=127.0.0.1:" + strconv.Itoa(n.SocksPort),
			"--port=" + strconv.Itoa(n.WGPort),
			// Never upload logs to Tailscale Inc.
			"--no-logs-no-support",
		}
	}, []string{"TS_NO_LOGS_NO_SUPPORT=true"})
	return n
}

// Run keeps tailscaled alive until ctx is done.
func (n *Node) Run(ctx context.Context) {
	_ = os.MkdirAll(n.Dir, 0o700)
	n.Proc.Run(ctx)
}

// Exec runs the tailscale CLI against this node.
func (n *Node) Exec(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"--socket=" + n.Socket}, args...)
	cmd := exec.CommandContext(ctx, n.CLI, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return stdout.Bytes(), fmt.Errorf("tailscale %s: %v: %s", args[0], err, msg)
	}
	return stdout.Bytes(), nil
}

// WaitReady waits for tailscaled's local socket to accept connections.
func (n *Node) WaitReady(ctx context.Context) error {
	for {
		c, err := net.Dial("unix", n.Socket)
		if err == nil {
			c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("tailscaled %s did not start: %w", n.Name, ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// Status is the subset of `tailscale status --json` Sphynx uses.
type Status struct {
	Version        string
	BackendState   string
	AuthURL        string
	Self           *PeerStatus
	Peer           map[string]*PeerStatus
	Health         []string
	CurrentTailnet *struct {
		Name           string
		MagicDNSSuffix string
	}
}

type PeerStatus struct {
	ID             string
	PublicKey      string
	HostName       string
	DNSName        string
	OS             string
	UserID         int64
	TailscaleIPs   []string
	Tags           []string
	PrimaryRoutes  []string
	Relay          string // home DERP region code
	PeerRelay      string // UDP peer relay in use, if any
	CurAddr        string // direct ip:port, empty when relayed
	RxBytes        int64
	TxBytes        int64
	Created        time.Time
	LastWrite      time.Time
	LastSeen       time.Time
	LastHandshake  time.Time
	Online         bool
	ExitNode       bool
	ExitNodeOption bool
	Active         bool
	PeerAPIURL     []string
	InNetworkMap   bool
	InMagicSock    bool
	InEngine       bool
	KeyExpiry      *time.Time
}

func (n *Node) Status(ctx context.Context) (*Status, error) {
	out, err := n.Exec(ctx, "status", "--json")
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var st Status
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, fmt.Errorf("parse status: %w", err)
	}
	return &st, nil
}

// UpOptions are passed to `tailscale up`.
type UpOptions struct {
	LoginServer     string
	AuthKey         string
	Hostname        string
	AdvertiseExit   bool
	AdvertiseRoutes []string
	AdvertiseTags   []string
	RelayServerPort int // 0 disables the peer relay server
}

func (o UpOptions) flags() []string {
	f := []string{
		"--hostname=" + o.Hostname,
		"--accept-dns=false",
		"--accept-routes=false",
		"--advertise-exit-node=" + strconv.FormatBool(o.AdvertiseExit),
		"--advertise-routes=" + strings.Join(o.AdvertiseRoutes, ","),
	}
	return f
}

// Up logs the node in (or re-applies settings when already logged in).
func (n *Node) Up(ctx context.Context, o UpOptions) error {
	args := []string{"up", "--reset", "--timeout=90s", "--login-server=" + o.LoginServer}
	if o.AuthKey != "" {
		args = append(args, "--auth-key="+o.AuthKey)
	}
	if len(o.AdvertiseTags) > 0 {
		args = append(args, "--advertise-tags="+strings.Join(o.AdvertiseTags, ","))
	}
	args = append(args, o.flags()...)
	_, err := n.Exec(ctx, args...)
	if err != nil {
		return err
	}
	return n.SetRelayServerPort(ctx, o.RelayServerPort)
}

// Set changes advertised routes without logging in again.
func (n *Node) Set(ctx context.Context, o UpOptions) error {
	args := append([]string{"set"}, o.flags()...)
	if _, err := n.Exec(ctx, args...); err != nil {
		return err
	}
	return n.SetRelayServerPort(ctx, o.RelayServerPort)
}

// SetRelayServerPort turns this node into a UDP peer relay for other devices
// (port > 0) or turns that off (port 0).
func (n *Node) SetRelayServerPort(ctx context.Context, port int) error {
	v := ""
	if port > 0 {
		v = strconv.Itoa(port)
	}
	_, err := n.Exec(ctx, "set", "--relay-server-port="+v)
	return err
}

// Ping sends Tailscale's own path-discovery pings to ip. A successful pong
// means the encrypted path is up, so a following request won't time out
// while the two devices are still finding each other.
func (n *Node) Ping(ctx context.Context, ip string) error {
	_, err := n.Exec(ctx, "ping", "--c=3", "--timeout=3s", "--until-direct=false", ip)
	return err
}

func (n *Node) Logout(ctx context.Context) error {
	_, err := n.Exec(ctx, "logout")
	return err
}

// Netmap returns `tailscale debug netmap`, which includes each peer's
// Hostinfo (OS version, client version, Wake-on-LAN MACs, ...).
func (n *Node) Netmap(ctx context.Context) (*Netmap, error) {
	out, err := n.Exec(ctx, "debug", "netmap")
	if err != nil {
		return nil, err
	}
	var nm Netmap
	if err := json.Unmarshal(out, &nm); err != nil {
		return nil, fmt.Errorf("parse netmap: %w", err)
	}
	return &nm, nil
}

type Netmap struct {
	SelfNode *NetmapNode
	Peers    []*NetmapNode
}

type NetmapNode struct {
	ID        int64
	StableID  string
	Name      string
	Addresses []string
	Hostinfo  Hostinfo
	CapMap    map[string]json.RawMessage
	Online    *bool
	LastSeen  *time.Time
	Endpoints []string
	HomeDERP  int
}

type Hostinfo struct {
	IPNVersion    string
	OS            string
	OSVersion     string
	Distro        string
	DistroVersion string
	DeviceModel   string
	Hostname      string
	Package       string
	ShieldsUp     bool
	WoLMACs       []string
	RoutableIPs   []string
	RequestTags   []string
	Userspace     bool
	Machine       string
	NetInfo       *struct {
		PreferredDERP         int
		WorkingUDP            *bool
		HaveIPv6              bool
		MappingVariesByDestIP *bool
		LinkType              string
	}
}

// HTTPClient makes requests through this node's SOCKS5 proxy, i.e. from its
// tailnet address.
func (n *Node) HTTPClient(timeout time.Duration) *http.Client {
	proxy := &url.URL{Scheme: "socks5", Host: "127.0.0.1:" + strconv.Itoa(n.SocksPort)}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyURL(proxy),
			MaxIdleConns:        16,
			IdleConnTimeout:     60 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
}

// ErrNotPeer means the node is not visible to this helper (offline, never
// connected, or hidden by the access policy).
var ErrNotPeer = errors.New("device is not visible from Sphynx's helper node")

// FindPeer returns the peer owning any of the given tailnet IPs.
func (st *Status) FindPeer(ips []string) *PeerStatus {
	want := map[string]bool{}
	for _, ip := range ips {
		want[ip] = true
	}
	for _, p := range st.Peer {
		for _, ip := range p.TailscaleIPs {
			if want[ip] {
				return p
			}
		}
	}
	return nil
}

// WoLResult is what a peer returns from its PeerAPI Wake-on-LAN handler.
type WoLResult struct {
	SentTo []string
	Errors []string
}

// WakeOnLAN asks the peer at peerAPIURL to broadcast a magic packet for mac
// on all of its LAN interfaces. The official client only allows this when
// the caller and the peer belong to the same user and neither is tagged.
func (n *Node) WakeOnLAN(ctx context.Context, peerAPIURL, mac string) (*WoLResult, error) {
	form := url.Values{"mac": {mac}}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(peerAPIURL, "/")+"/v0/wol", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := n.HTTPClient(20 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("relay device answered %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
	}
	var res WoLResult
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		return nil, fmt.Errorf("unexpected answer from relay device: %w", err)
	}
	return &res, nil
}

// WhoIs is the subset of tailscaled's LocalAPI whois answer Sphynx uses.
type WhoIs struct {
	Node *struct {
		ID       int64
		StableID string
		Name     string // MagicDNS name, with a trailing dot
		User     int64
		Machine  string
		Tags     []string
		Hostinfo struct {
			OS       string
			Hostname string
		}
	}
	UserProfile *struct {
		ID          int64
		LoginName   string
		DisplayName string
	}
}

// ErrNoMatch means tailscaled doesn't know the connection.
var ErrNoMatch = errors.New("not a connection from the tailnet")

// WhoIs asks this node which tailnet device is on the other end of a
// connection. In userspace-networking mode tailscaled hands connections
// from the tailnet to local services from a loopback address, and
// remembers which device each one came from; addr is that loopback
// address as the local service sees it.
func (n *Node) WhoIs(ctx context.Context, addr string) (*WhoIs, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://local-tailscaled.sock/localapi/v0/whois?proto=tcp&addr="+url.QueryEscape(addr), nil)
	if err != nil {
		return nil, err
	}
	resp, err := n.localClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoMatch
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tailscaled whois: %s", resp.Status)
	}
	var w WhoIs
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return nil, err
	}
	if w.Node == nil {
		return nil, ErrNoMatch
	}
	return &w, nil
}

// localClient talks to tailscaled's LocalAPI over its unix socket.
func (n *Node) localClient() *http.Client {
	n.localOnce.Do(func() {
		n.local = &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", n.Socket)
				},
				MaxIdleConns:    4,
				IdleConnTimeout: 30 * time.Second,
			},
		}
	})
	return n.local
}
