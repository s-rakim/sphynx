package app

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/s-rakim/sphynx/internal/hs"
	"github.com/s-rakim/sphynx/internal/tsnode"
)

const (
	firstSocksPort = 1081
	homeHostname   = "sphynx-home"
)

var exitRoutes = []string{"0.0.0.0/0", "::/0"}

// helper is one of Sphynx's own tailnet nodes. There is one per Headscale
// user, created on demand: the official client only accepts Wake-on-LAN
// requests from devices of the same user. The owner's helper is "Sphynx
// home" and also serves as exit node, subnet router and peer relay.
type helper struct {
	rec    HelperRecord
	node   *tsnode.Node
	cancel context.CancelFunc

	mu       sync.Mutex
	status   *tsnode.Status
	netmap   *tsnode.Netmap
	updated  time.Time
	lastErr  string
	loggedIn bool
	applied  string // fingerprint of the options last applied
}

type helperSet struct {
	a  *App
	mu sync.Mutex
	m  map[string]*helper // by Headscale user ID
	// wake is used to trigger an immediate reconcile.
	wake chan struct{}
}

func newHelperSet(a *App) *helperSet {
	return &helperSet{a: a, m: map[string]*helper{}, wake: make(chan struct{}, 1)}
}

func (hset *helperSet) kick() {
	select {
	case hset.wake <- struct{}{}:
	default:
	}
}

// loop keeps helpers logged in, their routes approved and their status fresh.
func (hset *helperSet) loop(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-hset.wake:
		}
		if ok, _ := hset.a.Ready(); ok {
			hset.reconcile(ctx)
		}
	}
}

// reconcile starts helpers that should exist, logs them in and applies
// their options.
func (hset *helperSet) reconcile(ctx context.Context) {
	a := hset.a
	owner, err := a.ownerUser(ctx)
	if err != nil {
		return
	}
	st := a.state.Get()
	want := map[string]bool{}
	for id := range st.Helpers {
		want[id] = true
	}
	if owner != nil {
		want[owner.ID] = true
	}
	users, err := a.hs.ListUsers(ctx)
	if err != nil {
		return
	}
	byID := map[string]hs.User{}
	for _, u := range users {
		byID[u.ID] = u
	}
	for id := range want {
		u, ok := byID[id]
		if !ok {
			// The user was deleted.
			hset.remove(ctx, id)
			continue
		}
		h, err := hset.ensure(u)
		if err != nil {
			a.Logf("helper for %s: %v", u.Name, err)
			continue
		}
		isHome := owner != nil && owner.ID == id
		if err := hset.sync(ctx, h, u, isHome); err != nil {
			h.mu.Lock()
			h.lastErr = err.Error()
			h.mu.Unlock()
			a.Logf("helper %s: %v", u.Name, err)
		}
	}
	// Managed policy rules depend on Sphynx home's address.
	if err := a.SyncPolicy(ctx); err != nil {
		a.Logf("could not apply access policy: %v", err)
	}
}

// ensure returns the running helper for u, starting it if needed.
func (hset *helperSet) ensure(u hs.User) (*helper, error) {
	hset.mu.Lock()
	defer hset.mu.Unlock()
	if h, ok := hset.m[u.ID]; ok {
		return h, nil
	}
	a := hset.a
	var rec HelperRecord
	err := a.state.Update(func(s *State) error {
		if r, ok := s.Helpers[u.ID]; ok {
			r.UserName = u.Name
			rec = *r
			return nil
		}
		used := map[int]bool{}
		for _, r := range s.Helpers {
			used[r.SocksPort] = true
		}
		port := firstSocksPort
		for used[port] {
			port++
		}
		r := &HelperRecord{UserID: u.ID, UserName: u.Name, SocksPort: port}
		s.Helpers[u.ID] = r
		rec = *r
		return nil
	})
	if err != nil {
		return nil, err
	}
	p := a.cfg.Paths
	wg := 0
	if u.Name == a.settings.Get().Owner {
		wg = a.cfg.HomeWGPort
	}
	node := tsnode.New("u"+u.ID, filepath.Join(p.TailscaleDir(), "user-"+u.ID), p.RunDir,
		p.TailscaledBin, p.TailscaleBin, rec.SocksPort, wg)
	ctx, cancel := context.WithCancel(a.ctx)
	h := &helper{rec: rec, node: node, cancel: cancel}
	hset.m[u.ID] = h
	go node.Run(ctx)
	return h, nil
}

func (hset *helperSet) remove(ctx context.Context, userID string) {
	hset.mu.Lock()
	h := hset.m[userID]
	delete(hset.m, userID)
	hset.mu.Unlock()
	if h != nil {
		h.cancel()
	}
	_ = hset.a.state.Update(func(s *State) error {
		delete(s.Helpers, userID)
		return nil
	})
}

// setRelayEndpoints tells Sphynx home's peer relay which public address
// and port to announce (needed behind Docker's and the router's NAT).
func (hset *helperSet) setRelayEndpoints(ctx context.Context, h *helper, eps []string) {
	if _, err := h.node.Exec(ctx, "set", "--relay-server-static-endpoints="+strings.Join(eps, ",")); err != nil {
		hset.a.Logf("relay endpoints: %v", err)
	}
}

// sync logs a helper in when needed and applies its options.
func (hset *helperSet) sync(ctx context.Context, h *helper, u hs.User, isHome bool) error {
	a := hset.a
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := h.node.WaitReady(rctx); err != nil {
		return err
	}
	st, err := h.node.Status(rctx)
	if err != nil {
		return err
	}
	s := a.settings.Get()
	opts := tsnode.UpOptions{
		LoginServer: headscaleURL,
		Hostname:    "sphynx-" + strings.ToLower(u.Name),
	}
	if isHome {
		opts.Hostname = homeHostname
		opts.AdvertiseExit = s.Home.ExitNode
		opts.AdvertiseRoutes = s.Home.LANRoutes
		opts.RelayServerPort = s.Home.RelayPort
	}
	if isHome && s.Home.ExitNode {
		// The default route must be advertised together with the routes.
		opts.AdvertiseRoutes = append(slices.Clone(opts.AdvertiseRoutes), exitRoutes...)
		opts.AdvertiseExit = false
	}
	fingerprint := fmt.Sprintf("%+v %v", opts, s.Home.RelayEndpoints)

	switch st.BackendState {
	case "NeedsLogin", "NoState", "Stopped":
		key, err := a.hs.CreatePreAuthKey(ctx, hs.CreateKeyRequest{
			User:       u.ID,
			Expiration: time.Now().Add(10 * time.Minute),
		})
		if err != nil {
			return fmt.Errorf("create login key: %w", err)
		}
		opts.AuthKey = key.Key
		uctx, ucancel := context.WithTimeout(ctx, 2*time.Minute)
		err = h.node.Up(uctx, opts)
		ucancel()
		if err != nil {
			return err
		}
		a.Logf("Sphynx node %s joined the tailnet as %s", opts.Hostname, u.Name)
		if isHome {
			hset.setRelayEndpoints(rctx, h, s.Home.RelayEndpoints)
		}
		h.mu.Lock()
		h.applied = fingerprint
		h.mu.Unlock()
		if st, err = h.node.Status(rctx); err != nil {
			return err
		}
	case "Running", "Starting":
		h.mu.Lock()
		changed := h.applied != fingerprint
		h.mu.Unlock()
		if changed {
			if err := h.node.Set(rctx, opts); err != nil {
				return err
			}
			if isHome {
				hset.setRelayEndpoints(rctx, h, s.Home.RelayEndpoints)
			}
			h.mu.Lock()
			h.applied = fingerprint
			h.mu.Unlock()
		}
	case "NeedsMachineAuth":
		// Not used by Headscale; fall through to status refresh.
	}

	// Map the helper to its Headscale node.
	nodeID := ""
	if st.Self != nil {
		nodes, err := a.hs.ListNodes(ctx)
		if err == nil {
			for _, n := range nodes {
				if overlaps(n.IPAddresses, st.Self.TailscaleIPs) {
					nodeID = n.ID
					if n.Expiry != nil && !n.Expiry.IsZero() {
						_ = a.hs.ExpireNode(ctx, n.ID, true)
					}
					if isHome {
						a.approveHomeRoutes(ctx, n, opts.AdvertiseRoutes)
					}
				}
			}
		}
	}
	if nodeID != "" && nodeID != h.rec.NodeID {
		h.rec.NodeID = nodeID
		_ = a.state.Update(func(s *State) error {
			if r := s.Helpers[u.ID]; r != nil {
				r.NodeID = nodeID
			}
			return nil
		})
	}
	var nm *tsnode.Netmap
	if isHome {
		nm, _ = h.node.Netmap(rctx)
	}
	h.mu.Lock()
	h.status, h.netmap, h.updated, h.lastErr = st, nm, time.Now(), ""
	h.loggedIn = st.BackendState == "Running"
	h.mu.Unlock()
	return nil
}

// approveHomeRoutes approves exactly what Sphynx home advertises, so the
// admin never has to approve Sphynx's own routes.
func (a *App) approveHomeRoutes(ctx context.Context, n hs.Node, routes []string) {
	want := slices.Clone(routes)
	slices.Sort(want)
	have := slices.Clone(n.ApprovedRoutes)
	slices.Sort(have)
	if slices.Equal(want, have) {
		return
	}
	if err := a.hs.SetApprovedRoutes(ctx, n.ID, want); err != nil {
		a.Logf("approve Sphynx home routes: %v", err)
	}
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// forUser returns the running, logged-in helper of a user, starting one if
// necessary and waiting briefly for it to join.
func (hset *helperSet) forUser(ctx context.Context, u hs.User) (*helper, error) {
	h, err := hset.ensure(u)
	if err != nil {
		return nil, err
	}
	owner, _ := hset.a.ownerUser(ctx)
	isHome := owner != nil && owner.ID == u.ID
	for i := 0; ; i++ {
		h.mu.Lock()
		ok := h.loggedIn
		h.mu.Unlock()
		if ok {
			return h, nil
		}
		if err := hset.sync(ctx, h, u, isHome); err != nil && i >= 2 {
			return nil, fmt.Errorf("Sphynx's helper device for %s is not connected yet: %w", u.Name, err)
		}
		if i >= 2 {
			return nil, fmt.Errorf("Sphynx's helper device for %s is not connected yet", u.Name)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (hset *helperSet) home() *helper {
	owner := hset.a.settings.Get().Owner
	hset.mu.Lock()
	defer hset.mu.Unlock()
	for _, h := range hset.m {
		if h.rec.UserName == owner {
			return h
		}
	}
	return nil
}

// nodes returns the tailscaled of every helper, Sphynx home first.
func (hset *helperSet) nodes() []*tsnode.Node {
	home := hset.home()
	hset.mu.Lock()
	defer hset.mu.Unlock()
	var out []*tsnode.Node
	if home != nil {
		out = append(out, home.node)
	}
	for _, h := range hset.m {
		if h != home {
			out = append(out, h.node)
		}
	}
	return out
}

func (hset *helperSet) homeNodeID() string {
	if h := hset.home(); h != nil {
		return h.rec.NodeID
	}
	return ""
}

// helperNodeIDs returns the Headscale node IDs that are Sphynx helpers.
func (hset *helperSet) helperNodeIDs() map[string]bool {
	ids := map[string]bool{}
	for _, r := range hset.a.state.Get().Helpers {
		if r.NodeID != "" {
			ids[r.NodeID] = true
		}
	}
	return ids
}

// snapshot returns the cached status and netmap of Sphynx home.
func (hset *helperSet) snapshot() (*tsnode.Status, *tsnode.Netmap, time.Time) {
	h := hset.home()
	if h == nil {
		return nil, nil, time.Time{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status, h.netmap, h.updated
}

// refreshHome re-reads Sphynx home's view of the tailnet if it is stale.
func (hset *helperSet) refreshHome(ctx context.Context, maxAge time.Duration) {
	h := hset.home()
	if h == nil {
		return
	}
	h.mu.Lock()
	fresh := time.Since(h.updated) < maxAge
	h.mu.Unlock()
	if fresh {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := h.node.Status(rctx)
	if err != nil {
		return
	}
	nm, _ := h.node.Netmap(rctx)
	h.mu.Lock()
	h.status, h.netmap, h.updated = st, nm, time.Now()
	h.mu.Unlock()
}

// HelperInfo is shown on the Settings page.
type HelperInfo struct {
	User      string    `json:"user"`
	Hostname  string    `json:"hostname"`
	NodeID    string    `json:"nodeId"`
	State     string    `json:"state"`
	IPs       []string  `json:"ips"`
	Home      bool      `json:"home"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Running   bool      `json:"running"`
}

func (hset *helperSet) info() []HelperInfo {
	owner := hset.a.settings.Get().Owner
	hset.mu.Lock()
	defer hset.mu.Unlock()
	var out []HelperInfo
	for _, h := range hset.m {
		h.mu.Lock()
		hi := HelperInfo{
			User: h.rec.UserName, NodeID: h.rec.NodeID, Home: h.rec.UserName == owner,
			Error: h.lastErr, UpdatedAt: h.updated, Running: h.node.Proc.Status().Running,
		}
		if h.status != nil {
			hi.State = h.status.BackendState
			if h.status.Self != nil {
				hi.Hostname = h.status.Self.HostName
				hi.IPs = h.status.Self.TailscaleIPs
			}
		}
		h.mu.Unlock()
		out = append(out, hi)
	}
	slices.SortFunc(out, func(x, y HelperInfo) int { return strings.Compare(x.User, y.User) })
	return out
}

// wakeVia asks relay (an awake device) to broadcast a magic packet. The
// request is sent from the helper owned by the same user as the relay.
func (a *App) wakeVia(ctx context.Context, relay hs.Node, mac string) (*tsnode.WoLResult, error) {
	if len(relay.Tags) > 0 {
		return nil, errors.New("tagged devices can't relay Wake-on-LAN: the Tailscale client only accepts wake requests from its own user's devices")
	}
	if relay.User == nil {
		return nil, errors.New("relay device has no owner")
	}
	if !relay.Online {
		return nil, fmt.Errorf("%s is offline, so it can't send the wake packet", displayName(relay))
	}
	h, err := a.helpers.forUser(ctx, *relay.User)
	if err != nil {
		return nil, err
	}
	var url string
	for i := 0; i < 10 && url == ""; i++ {
		st, err := h.node.Status(ctx)
		if err != nil {
			return nil, err
		}
		if p := st.FindPeer(relay.IPAddresses); p != nil {
			url = pickPeerAPI(p.PeerAPIURL)
		}
		if url == "" {
			// A newly started helper may not have received the peer list yet.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if url == "" {
		return nil, fmt.Errorf("%s doesn't offer the Tailscale peer API to Sphynx (is it reachable under the access policy?)", displayName(relay))
	}
	res, err := h.node.WakeOnLAN(ctx, url, mac)
	if err != nil && !strings.HasPrefix(err.Error(), "relay device answered") {
		// Most likely Sphynx and the relay hadn't talked yet and the
		// request beat path discovery. Ping, then try once more.
		host := strings.TrimPrefix(url, "http://")
		if ap, perr := netip.ParseAddrPort(host); perr == nil {
			_ = h.node.Ping(ctx, ap.Addr().String())
		}
		res, err = h.node.WakeOnLAN(ctx, url, mac)
	}
	return res, err
}

func pickPeerAPI(urls []string) string {
	for _, u := range urls {
		host := strings.TrimPrefix(u, "http://")
		if ap, err := netip.ParseAddrPort(host); err == nil && ap.Addr().Is4() {
			return u
		}
	}
	if len(urls) > 0 {
		return urls[0]
	}
	return ""
}

func displayName(n hs.Node) string {
	if n.GivenName != "" {
		return n.GivenName
	}
	return n.Name
}

// resetApplied restarts every helper, e.g. after Sphynx home moved to
// another user (its WireGuard port and routes move with it).
func (hset *helperSet) resetApplied() {
	hset.mu.Lock()
	old := hset.m
	hset.m = map[string]*helper{}
	hset.mu.Unlock()
	for _, h := range old {
		h.cancel()
	}
	deadline := time.Now().Add(20 * time.Second)
	for _, h := range old {
		for h.node.Proc.Status().Running && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
	}
	hset.kick()
}
