package app

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/s-rakim/sphynx/internal/hs"
	"github.com/s-rakim/sphynx/internal/tsnode"
)

// Device is a Headscale node plus what Sphynx home can see of it.
type Device struct {
	hs.Node
	Helper bool `json:"helper"`
	Home   bool `json:"home"`

	OS            string   `json:"os,omitempty"`
	OSVersion     string   `json:"osVersion,omitempty"`
	ClientVersion string   `json:"clientVersion,omitempty"`
	DeviceModel   string   `json:"deviceModel,omitempty"`
	Hostname      string   `json:"hostname,omitempty"`
	DNSName       string   `json:"dnsName,omitempty"`
	WoLMACs       []string `json:"wolMacs,omitempty"`
	ShieldsUp     bool     `json:"shieldsUp,omitempty"`

	// Path between this device and Sphynx home.
	Link          string     `json:"link,omitempty"` // direct, relay, peer-relay, idle
	LinkAddr      string     `json:"linkAddr,omitempty"`
	RelayRegion   string     `json:"relayRegion,omitempty"`
	LastHandshake *time.Time `json:"lastHandshake,omitempty"`
	RxBytes       int64      `json:"rxBytes,omitempty"`
	TxBytes       int64      `json:"txBytes,omitempty"`
	UDPWorks      *bool      `json:"udpWorks,omitempty"`

	ExitNodeAdvertised bool     `json:"exitNodeAdvertised"`
	ExitNodeApproved   bool     `json:"exitNodeApproved"`
	SubnetsAdvertised  []string `json:"subnetsAdvertised"`
	SubnetsApproved    []string `json:"subnetsApproved"`
	StrictFirewall     bool     `json:"strictFirewall"`
	PeerRelay          bool     `json:"peerRelay"`
}

func isExitRoute(r string) bool { return r == "0.0.0.0/0" || r == "::/0" }

func (a *App) devices(ctx context.Context) ([]Device, error) {
	nodes, err := a.hs.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	a.helpers.refreshHome(ctx, 10*time.Second)
	st, nm, _ := a.helpers.snapshot()
	helperIDs := a.helpers.helperNodeIDs()
	homeID := a.helpers.homeNodeID()
	opts := a.state.Get().Devices

	byIP := map[string]*tsnode.NetmapNode{}
	if nm != nil {
		all := append([]*tsnode.NetmapNode{nm.SelfNode}, nm.Peers...)
		for _, p := range all {
			if p == nil {
				continue
			}
			for _, addr := range p.Addresses {
				if pf, err := netip.ParsePrefix(addr); err == nil {
					byIP[pf.Addr().String()] = p
				}
			}
		}
	}

	out := make([]Device, 0, len(nodes))
	for _, n := range nodes {
		d := Device{Node: n, Helper: helperIDs[n.ID], Home: n.ID == homeID && homeID != ""}
		for _, r := range n.AvailableRoutes {
			if isExitRoute(r) {
				d.ExitNodeAdvertised = true
			} else {
				d.SubnetsAdvertised = append(d.SubnetsAdvertised, r)
			}
		}
		for _, r := range n.ApprovedRoutes {
			if isExitRoute(r) {
				d.ExitNodeApproved = true
			} else {
				d.SubnetsApproved = append(d.SubnetsApproved, r)
			}
		}
		if d.SubnetsAdvertised == nil {
			d.SubnetsAdvertised = []string{}
		}
		if d.SubnetsApproved == nil {
			d.SubnetsApproved = []string{}
		}
		if o := opts[n.ID]; o != nil {
			d.StrictFirewall = o.StrictFirewall
			d.PeerRelay = o.PeerRelay && !d.Helper
		}
		for _, ip := range n.IPAddresses {
			if p := byIP[ip]; p != nil {
				hi := p.Hostinfo
				d.OS, d.OSVersion, d.ClientVersion = hi.OS, hi.OSVersion, shortVersion(hi.IPNVersion)
				d.DeviceModel, d.Hostname, d.WoLMACs, d.ShieldsUp = hi.DeviceModel, hi.Hostname, hi.WoLMACs, hi.ShieldsUp
				if hi.Distro != "" {
					d.OSVersion = strings.TrimSpace(hi.Distro + " " + hi.DistroVersion)
				}
				if hi.NetInfo != nil {
					d.UDPWorks = hi.NetInfo.WorkingUDP
				}
				d.DNSName = strings.TrimSuffix(p.Name, ".")
				break
			}
		}
		if st != nil {
			if p := st.FindPeer(n.IPAddresses); p != nil {
				d.RxBytes, d.TxBytes = p.RxBytes, p.TxBytes
				d.RelayRegion = p.Relay
				if !p.LastHandshake.IsZero() {
					t := p.LastHandshake
					d.LastHandshake = &t
				}
				switch {
				case p.CurAddr != "":
					d.Link, d.LinkAddr = "direct", p.CurAddr
				case p.PeerRelay != "":
					d.Link, d.LinkAddr = "peer-relay", p.PeerRelay
				case p.Active || (!p.LastHandshake.IsZero() && time.Since(p.LastHandshake) < 3*time.Minute):
					d.Link = "relay"
				default:
					d.Link = "idle"
				}
				if d.OS == "" {
					d.OS = p.OS
				}
				if d.DNSName == "" {
					d.DNSName = strings.TrimSuffix(p.DNSName, ".")
				}
			}
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(x, y Device) int {
		if x.Helper != y.Helper {
			if x.Helper {
				return 1
			}
			return -1
		}
		return strings.Compare(strings.ToLower(displayName(x.Node)), strings.ToLower(displayName(y.Node)))
	})
	return out, nil
}

// shortVersion turns "1.104.4-t27047635f-gabc" into "1.104.4".
func shortVersion(v string) string {
	v, _, _ = strings.Cut(v, "-")
	return v
}

func (a *App) apiNodes(w http.ResponseWriter, r *http.Request) (any, error) {
	return a.devices(r.Context())
}

func (a *App) findDevice(ctx context.Context, id string) (*Device, error) {
	ds, err := a.devices(ctx)
	if err != nil {
		return nil, err
	}
	for i := range ds {
		if ds[i].ID == id {
			return &ds[i], nil
		}
	}
	return nil, notFound("no device with that ID")
}

func (a *App) apiNode(w http.ResponseWriter, r *http.Request) (any, error) {
	return a.findDevice(r.Context(), r.PathValue("id"))
}

var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (a *App) apiNodeRename(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ Name string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if !dnsLabelRe.MatchString(name) {
		return nil, badRequest("device names can use lowercase letters, numbers and dashes (they become DNS names)")
	}
	return nil, a.hs.RenameNode(r.Context(), r.PathValue("id"), name)
}

var tagRe = regexp.MustCompile(`^tag:[a-zA-Z0-9][a-zA-Z0-9-]*$`)

func (a *App) apiNodeTags(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ Tags []string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	var tags []string
	for _, t := range req.Tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "tag:") {
			t = "tag:" + t
		}
		if !tagRe.MatchString(t) {
			return nil, badRequest("tags look like tag:server (letters, numbers and dashes)")
		}
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	return nil, a.hs.SetTags(r.Context(), r.PathValue("id"), tags)
}

// apiNodeRoutes sets which advertised subnets and exit node are approved.
func (a *App) apiNodeRoutes(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Subnets  []string `json:"subnets"`
		ExitNode bool     `json:"exitNode"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	var routes []string
	for _, s := range req.Subnets {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, badRequest(s + " is not a valid subnet")
		}
		if isExitRoute(p.String()) {
			continue
		}
		routes = append(routes, p.Masked().String())
	}
	if req.ExitNode {
		routes = append(routes, exitRoutes...)
	}
	if err := a.hs.SetApprovedRoutes(r.Context(), r.PathValue("id"), routes); err != nil {
		return nil, err
	}
	return a.findDevice(r.Context(), r.PathValue("id"))
}

func (a *App) apiNodeExpire(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		DisableExpiry bool `json:"disableExpiry"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return nil, a.hs.ExpireNode(r.Context(), r.PathValue("id"), req.DisableExpiry)
}

// apiNodeOptions changes Sphynx's per-device options. Options left out of
// the request keep their value.
func (a *App) apiNodeOptions(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		StrictFirewall *bool `json:"strictFirewall"`
		PeerRelay      *bool `json:"peerRelay"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	if _, err := a.hs.GetNode(r.Context(), id); err != nil {
		return nil, err
	}
	if req.PeerRelay != nil && *req.PeerRelay && a.helpers.helperNodeIDs()[id] {
		return nil, badRequest("this is one of Sphynx's own devices; Sphynx home's peer relay is under Settings")
	}
	if err := a.state.Update(func(s *State) error {
		if s.Devices == nil {
			s.Devices = map[string]*DeviceOptions{}
		}
		var o DeviceOptions
		if cur := s.Devices[id]; cur != nil {
			o = *cur
		}
		if req.StrictFirewall != nil {
			o.StrictFirewall = *req.StrictFirewall
		}
		if req.PeerRelay != nil {
			o.PeerRelay = *req.PeerRelay
		}
		s.Devices[id] = &o
		return nil
	}); err != nil {
		return nil, err
	}
	if err := a.SyncPolicy(r.Context()); err != nil {
		return nil, err
	}
	return a.findDevice(r.Context(), id)
}

func (a *App) apiNodeDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	id := r.PathValue("id")
	if a.helpers.helperNodeIDs()[id] {
		return nil, badRequest("this is one of Sphynx's own devices; it is managed automatically")
	}
	if err := a.hs.DeleteNode(r.Context(), id); err != nil {
		return nil, err
	}
	_ = a.state.Update(func(s *State) error {
		delete(s.Devices, id)
		return nil
	})
	return nil, a.SyncPolicy(r.Context())
}

var authIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{6,128}$`)

// apiRegister approves a device that ran `tailscale up` and was shown a
// registration link, or approves/rejects another pending auth request.
func (a *App) apiRegister(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Link   string `json:"link"`
		User   string `json:"user"`
		Action string `json:"action"` // register (default), approve, reject
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	authID := strings.TrimSpace(req.Link)
	if u, err := url.Parse(authID); err == nil && u.Path != "" && strings.Contains(authID, "/") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		authID = parts[len(parts)-1]
	}
	if !authIDRe.MatchString(authID) {
		return nil, badRequest("paste the full link the device showed, like http://server:8080/register/AbCd1234")
	}
	ctx := r.Context()
	switch req.Action {
	case "approve":
		return nil, a.hs.AuthApprove(ctx, authID)
	case "reject":
		return nil, a.hs.AuthReject(ctx, authID)
	}
	if req.User == "" {
		return nil, badRequest("choose who the device belongs to")
	}
	n, err := a.hs.AuthRegister(ctx, req.User, authID)
	if err != nil {
		return nil, err
	}
	a.Logf("registered device %s for %s", n.Name, req.User)
	return n, nil
}
