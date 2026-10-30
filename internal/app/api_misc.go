package app

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/s-rakim/sphynx/internal/hs"
	"github.com/s-rakim/sphynx/internal/wol"
)

// Overview

func (a *App) apiOverview(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx := r.Context()
	ready, why := a.Ready()
	s := a.settings.Get()
	out := map[string]any{
		"version":    a.cfg.Version,
		"ready":      ready,
		"readyError": why,
		"serverUrl":  s.ServerURL,
		"tlsMode":    s.TLS.Mode,
		"owner":      s.Owner,
		"magicDns":   s.DNS.MagicDNS,
		"baseDomain": s.DNS.BaseDomain,
		"adminPort":  a.adminPort(),
		"headscale":  a.hsProc.Status(),
		"helpers":    a.helpers.info(),
		"home":       s.Home,
		"resilience": s.Resilience,
		"tlsError":   a.front.tlsErr,
	}
	if !ready {
		return out, nil
	}
	devs, err := a.devices(ctx)
	if err != nil {
		return nil, err
	}
	users, err := a.hs.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	total, online, exits, pending := 0, 0, 0, 0
	for _, d := range devs {
		if d.Helper {
			continue
		}
		total++
		if d.Online {
			online++
		}
		if d.ExitNodeApproved {
			exits++
		}
		if (d.ExitNodeAdvertised && !d.ExitNodeApproved) || len(d.SubnetsAdvertised) > len(d.SubnetsApproved) {
			pending++
		}
	}
	out["devices"] = total
	out["online"] = online
	out["users"] = len(users)
	out["exitNodes"] = exits
	out["pendingRoutes"] = pending
	out["homeExitReady"] = false
	for _, d := range devs {
		if d.Home {
			out["homeExitReady"] = d.ExitNodeApproved && d.Online
			out["homeDevice"] = d
		}
	}
	return out, nil
}

// Users

func (a *App) apiUsers(w http.ResponseWriter, r *http.Request) (any, error) {
	users, err := a.hs.ListUsers(r.Context())
	if err != nil {
		return nil, err
	}
	nodes, err := a.hs.ListNodes(r.Context())
	if err != nil {
		return nil, err
	}
	helperIDs := a.helpers.helperNodeIDs()
	type userView struct {
		hs.User
		Devices int  `json:"devices"`
		Owner   bool `json:"owner"`
	}
	owner := a.settings.Get().Owner
	out := []userView{}
	for _, u := range users {
		v := userView{User: u, Owner: u.Name == owner}
		for _, n := range nodes {
			if n.User != nil && n.User.ID == u.ID && !helperIDs[n.ID] {
				v.Devices++
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *App) apiUserCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if err := validUserName(name); err != nil {
		return nil, err
	}
	return a.hs.CreateUser(r.Context(), name, strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.Email))
}

func (a *App) apiUserRename(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ Name string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if err := validUserName(name); err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	users, err := a.hs.ListUsers(r.Context())
	if err != nil {
		return nil, err
	}
	var old string
	for _, u := range users {
		if u.ID == id {
			old = u.Name
		}
	}
	if err := a.hs.RenameUser(r.Context(), id, name); err != nil {
		return nil, err
	}
	if old != "" && old == a.settings.Get().Owner {
		_ = a.settings.Update(func(s *Settings) error {
			s.Owner = name
			return nil
		})
	}
	return nil, nil
}

// apiUserDelete removes a user. Their Sphynx helper is removed first;
// Headscale refuses while the user still owns other devices.
func (a *App) apiUserDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx := r.Context()
	id := r.PathValue("id")
	users, err := a.hs.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	var name string
	for _, u := range users {
		if u.ID == id {
			name = u.Name
		}
	}
	if name == "" {
		return nil, notFound("no such user")
	}
	if name == a.settings.Get().Owner {
		return nil, badRequest("this user owns Sphynx home; pick another owner in Settings first")
	}
	nodes, err := a.hs.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	helperIDs := a.helpers.helperNodeIDs()
	var others []string
	for _, n := range nodes {
		if n.User == nil || n.User.ID != id {
			continue
		}
		if helperIDs[n.ID] {
			continue
		}
		others = append(others, displayName(n))
	}
	if len(others) > 0 {
		return nil, badRequest("remove this user's devices first: " + strings.Join(others, ", "))
	}
	a.helpers.remove(ctx, id)
	for _, n := range nodes {
		if n.User != nil && n.User.ID == id && helperIDs[n.ID] {
			_ = a.hs.DeleteNode(ctx, n.ID)
		}
	}
	return nil, a.hs.DeleteUser(ctx, id)
}

// Pre-auth keys

func (a *App) apiKeys(w http.ResponseWriter, r *http.Request) (any, error) {
	keys, err := a.hs.ListPreAuthKeys(r.Context())
	if err != nil {
		return nil, err
	}
	// Hide the short-lived keys Sphynx makes for its own helper devices.
	helperUsers := map[string]bool{}
	for _, h := range a.state.Get().Helpers {
		helperUsers[h.UserID] = true
	}
	out := []hs.PreAuthKey{}
	for _, k := range keys {
		if k.Used && !k.Reusable && k.User != nil && helperUsers[k.User.ID] && k.Expiration.Sub(k.CreatedAt) <= 11*time.Minute {
			continue
		}
		out = append(out, k)
	}
	slices.SortFunc(out, func(x, y hs.PreAuthKey) int { return y.CreatedAt.Compare(x.CreatedAt) })
	return out, nil
}

func (a *App) apiKeyCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		User      string   `json:"user"` // user ID; empty for a tags-only key
		Reusable  bool     `json:"reusable"`
		Ephemeral bool     `json:"ephemeral"`
		Hours     float64  `json:"hours"`
		Tags      []string `json:"tags"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.Hours <= 0 {
		req.Hours = 24
	}
	if req.Hours > 24*365*10 {
		return nil, badRequest("keys can last at most 10 years")
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
			return nil, badRequest("tags look like tag:server")
		}
		tags = append(tags, t)
	}
	if req.User == "" && len(tags) == 0 {
		return nil, badRequest("choose a user, or add tags for a shared/server device")
	}
	exp := time.Now().Add(time.Duration(req.Hours * float64(time.Hour)))
	return a.hs.CreatePreAuthKey(r.Context(), hs.CreateKeyRequest{
		User: req.User, Reusable: req.Reusable, Ephemeral: req.Ephemeral, Expiration: exp, ACLTags: tags,
	})
}

func (a *App) apiKeyExpire(w http.ResponseWriter, r *http.Request) (any, error) {
	return nil, a.hs.ExpirePreAuthKey(r.Context(), r.PathValue("id"))
}

func (a *App) apiKeyDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	return nil, a.hs.DeletePreAuthKey(r.Context(), r.PathValue("id"))
}

// API keys (for scripts and other tools)

func (a *App) apiAPIKeys(w http.ResponseWriter, r *http.Request) (any, error) {
	keys, err := a.hs.ListAPIKeys(r.Context())
	if err != nil {
		return nil, err
	}
	own := a.hs.Key()
	type keyView struct {
		hs.APIKey
		Sphynx bool `json:"sphynx"`
	}
	out := []keyView{}
	for _, k := range keys {
		out = append(out, keyView{APIKey: k, Sphynx: k.Prefix != "" && strings.HasPrefix(own, k.Prefix)})
	}
	return out, nil
}

func (a *App) apiAPIKeyCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Days int `json:"days"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.Days <= 0 {
		req.Days = 90
	}
	key, err := a.hs.CreateAPIKey(r.Context(), time.Now().AddDate(0, 0, req.Days))
	if err != nil {
		return nil, err
	}
	return map[string]string{"key": key}, nil
}

func (a *App) guardOwnAPIKey(prefix string) error {
	if prefix != "" && strings.HasPrefix(a.hs.Key(), prefix) {
		return badRequest("that key is the one Sphynx itself uses")
	}
	return nil
}

func (a *App) apiAPIKeyExpire(w http.ResponseWriter, r *http.Request) (any, error) {
	if err := a.guardOwnAPIKey(r.PathValue("prefix")); err != nil {
		return nil, err
	}
	return nil, a.hs.ExpireAPIKey(r.Context(), r.PathValue("prefix"))
}

func (a *App) apiAPIKeyDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	if err := a.guardOwnAPIKey(r.PathValue("prefix")); err != nil {
		return nil, err
	}
	return nil, a.hs.DeleteAPIKey(r.Context(), r.PathValue("prefix"))
}

// Policy

func (a *App) apiPolicy(w http.ResponseWriter, r *http.Request) (any, error) {
	st := a.state.Get()
	base := st.BasePolicy
	if base == "" {
		base = DefaultPolicy
	}
	rules := ManagedRules(a.policyInputs(r.Context()))
	eff, err := EffectivePolicy(base, rules)
	if err != nil {
		eff = ""
	}
	out := map[string]any{"policy": base, "managed": rules, "effective": eff, "default": DefaultPolicy}
	if p, err := a.hs.GetPolicy(r.Context()); err == nil {
		out["updatedAt"] = p.UpdatedAt
	}
	return out, nil
}

func (a *App) checkPolicy(r *http.Request, base string) (string, error) {
	eff, err := EffectivePolicy(base, ManagedRules(a.policyInputs(r.Context())))
	if err != nil {
		return "", badRequest(err.Error())
	}
	if err := a.hs.CheckPolicy(r.Context(), eff); err != nil {
		var ae *hs.APIError
		if errors.As(err, &ae) {
			return "", badRequest(ae.Message)
		}
		return "", err
	}
	return eff, nil
}

func (a *App) apiPolicyCheck(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ Policy string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if _, err := a.checkPolicy(r, req.Policy); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) apiPolicySave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ Policy string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if _, err := a.checkPolicy(r, req.Policy); err != nil {
		return nil, err
	}
	old := a.state.Get().BasePolicy
	if err := a.state.Update(func(s *State) error {
		s.BasePolicy = req.Policy
		return nil
	}); err != nil {
		return nil, err
	}
	if err := a.SyncPolicy(r.Context()); err != nil {
		_ = a.state.Update(func(s *State) error {
			s.BasePolicy = old
			return nil
		})
		return nil, err
	}
	a.Logf("access policy updated")
	return a.apiPolicy(w, r)
}

// DNS

type extraRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	// Managed marks records Sphynx adds itself (Headscale ignores the field).
	Managed bool `json:"sphynx,omitempty"`
}

// readExtraRecords returns the DNS records the admin made, without the
// ones Sphynx manages.
func (a *App) readExtraRecords() ([]extraRecord, error) {
	b, err := os.ReadFile(a.cfg.Paths.ExtraRecords())
	if errors.Is(err, os.ErrNotExist) {
		return []extraRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []extraRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, err
	}
	out := []extraRecord{}
	for _, r := range recs {
		if !r.Managed {
			out = append(out, r)
		}
	}
	return out, nil
}

// writeExtraRecords saves the admin's records plus Sphynx's own. Headscale
// watches the file and applies records without a restart.
func (a *App) writeExtraRecords(user []extraRecord) error {
	all := append(append([]extraRecord{}, user...), a.managedRecords()...)
	b, _ := json.MarshalIndent(all, "", "  ")
	if cur, err := os.ReadFile(a.cfg.Paths.ExtraRecords()); err == nil && string(cur) == string(b) {
		return nil
	}
	return writeFileAtomic(a.cfg.Paths.ExtraRecords(), b, 0o644)
}

// syncManagedRecords rewrites the records file after Sphynx's own records
// changed.
func (a *App) syncManagedRecords() {
	recs, err := a.readExtraRecords()
	if err == nil {
		err = a.writeExtraRecords(recs)
	}
	if err != nil {
		a.Logf("could not update DNS records: %v", err)
	}
}

func (a *App) apiDNS(w http.ResponseWriter, r *http.Request) (any, error) {
	recs, err := a.readExtraRecords()
	if err != nil {
		return nil, err
	}
	return map[string]any{"dns": a.settings.Get().DNS, "records": recs}, nil
}

var recordNameRe = regexp.MustCompile(`^(?i)[a-z0-9*]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

func (a *App) apiDNSSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		DNS     DNSSettings   `json:"dns"`
		Records []extraRecord `json:"records"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	for i, rec := range req.Records {
		rec.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rec.Name), "."))
		rec.Type = strings.ToUpper(strings.TrimSpace(rec.Type))
		rec.Value = strings.TrimSpace(rec.Value)
		if !recordNameRe.MatchString(rec.Name) {
			return nil, badRequest(fmt.Sprintf("%q is not a full DNS name (e.g. nas.sphynx.internal)", rec.Name))
		}
		ip := net.ParseIP(rec.Value)
		switch {
		case rec.Type == "A" && (ip == nil || ip.To4() == nil):
			return nil, badRequest(rec.Name + ": an A record needs an IPv4 address")
		case rec.Type == "AAAA" && (ip == nil || ip.To4() != nil):
			return nil, badRequest(rec.Name + ": an AAAA record needs an IPv6 address")
		case rec.Type != "A" && rec.Type != "AAAA":
			return nil, badRequest("only A and AAAA records are supported")
		}
		req.Records[i] = rec
	}
	if req.Records == nil {
		req.Records = []extraRecord{}
	}
	s := a.settings.Get()
	changed := !jsonEqual(s.DNS, req.DNS)
	if changed {
		s.DNS = req.DNS
		if err := s.Validate(); err != nil {
			return nil, badRequest(err.Error())
		}
	}
	for i := range req.Records {
		req.Records[i].Managed = false
	}
	if err := a.writeExtraRecords(req.Records); err != nil {
		return nil, err
	}
	if changed {
		if err := a.settings.Update(func(cur *Settings) error {
			cur.DNS = req.DNS
			return nil
		}); err != nil {
			return nil, err
		}
		if err := a.RestartHeadscale(); err != nil {
			return nil, err
		}
	}
	return map[string]any{"restarted": changed}, nil
}

func jsonEqual(x, y any) bool {
	bx, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return string(bx) == string(by)
}

// Settings

func (a *App) apiSettings(w http.ResponseWriter, r *http.Request) (any, error) {
	s := a.settings.Get()
	s.OIDC.ClientSecret = ""
	return map[string]any{
		"settings":      s,
		"oidcSecretSet": a.secrets.Get().OIDCSecret != "",
		"helpers":       a.helpers.info(),
		"processes":     []any{a.hsProc.Status()},
		"ports": map[string]any{
			"admin": a.cfg.AdminAddr, "http": a.cfg.HTTPAddr, "https": a.cfg.TLSAddr,
			"acme": a.cfg.ACMEAddr, "homeWireGuard": a.cfg.HomeWGPort,
		},
	}, nil
}

func (a *App) apiSettingsSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req Settings
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	old := a.settings.Get()
	// Fields that are not edited on the Settings page.
	req.SetupDone = old.SetupDone
	req.DNS = old.DNS
	req.Link = old.Link
	req.Chat = old.Chat
	req.Radio = old.Radio
	req.ServerURL = strings.TrimRight(strings.TrimSpace(req.ServerURL), "/")
	if req.Home.LANRoutes == nil {
		req.Home.LANRoutes = []string{}
	}
	for i, rt := range req.Home.LANRoutes {
		req.Home.LANRoutes[i] = strings.TrimSpace(rt)
	}
	if req.Home.RelayEndpoints == nil {
		req.Home.RelayEndpoints = []string{}
	}
	secret := req.OIDC.ClientSecret
	req.OIDC.ClientSecret = ""
	if err := req.Validate(); err != nil {
		return nil, badRequest(err.Error())
	}
	if req.Owner != old.Owner {
		users, err := a.hs.ListUsers(r.Context())
		if err != nil {
			return nil, err
		}
		found := false
		for _, u := range users {
			found = found || u.Name == req.Owner
		}
		if !found {
			return nil, badRequest("Sphynx home's owner must be an existing user")
		}
	}
	if secret != "" {
		if err := a.secrets.Update(func(s *Secrets) error {
			s.OIDCSecret = secret
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if err := a.settings.Update(func(cur *Settings) error {
		*cur = req
		return nil
	}); err != nil {
		return nil, err
	}
	a.Logf("settings updated")

	// Decide what needs restarting.
	oldCfg, _ := old.HeadscaleConfig(a.cfg.Paths, "")
	newCfg, _ := req.HeadscaleConfig(a.cfg.Paths, "")
	restarted := false
	if string(oldCfg) != string(newCfg) || secret != "" {
		if err := a.RestartHeadscale(); err != nil {
			return nil, err
		}
		restarted = true
	}
	if !jsonEqual(old.TLS, req.TLS) {
		a.front.Reload()
	}
	if req.Owner != old.Owner {
		// The previous home helper becomes a plain helper; reset both.
		a.helpers.resetApplied()
	}
	if !restarted {
		go a.helpers.reconcile(a.ctx)
	}
	return map[string]any{"restarted": restarted}, nil
}

func (a *App) apiHeadscaleRestart(w http.ResponseWriter, r *http.Request) (any, error) {
	return nil, a.RestartHeadscale()
}

// Wake-on-LAN

func (a *App) apiWoL(w http.ResponseWriter, r *http.Request) (any, error) {
	t := a.state.Get().WakeTargets
	if t == nil {
		t = []*WakeTarget{}
	}
	return t, nil
}

func (a *App) apiWoLSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req WakeTarget
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, badRequest("give the computer a name")
	}
	mac, err := wol.ParseMAC(req.MAC)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	req.MAC = mac.String()
	switch req.Method {
	case "relay":
		if req.RelayNodeID == "" {
			return nil, badRequest("pick an always-on device on the same network to send the wake signal")
		}
		n, err := a.hs.GetNode(r.Context(), req.RelayNodeID)
		if err != nil {
			return nil, err
		}
		if len(n.Tags) > 0 {
			return nil, badRequest("tagged devices can't relay Wake-on-LAN; pick a personal device")
		}
	case "server":
	default:
		return nil, badRequest("method must be relay or server")
	}
	var saved WakeTarget
	err = a.state.Update(func(s *State) error {
		for _, t := range s.WakeTargets {
			if req.ID != "" && t.ID == req.ID {
				t.Name, t.MAC, t.NodeID, t.Method, t.RelayNodeID = req.Name, req.MAC, req.NodeID, req.Method, req.RelayNodeID
				saved = *t
				return nil
			}
		}
		if req.ID != "" {
			return notFound("no such wake target")
		}
		req.ID = randomHex(6)
		req.LastWake, req.LastResult = time.Time{}, ""
		t := req
		s.WakeTargets = append(s.WakeTargets, &t)
		saved = t
		return nil
	})
	return saved, err
}

func (a *App) apiWoLDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	id := r.PathValue("id")
	return nil, a.state.Update(func(s *State) error {
		s.WakeTargets = slices.DeleteFunc(s.WakeTargets, func(t *WakeTarget) bool { return t.ID == id })
		return nil
	})
}

func (a *App) apiWoLWake(w http.ResponseWriter, r *http.Request) (any, error) {
	var target *WakeTarget
	for _, t := range a.state.Get().WakeTargets {
		if t.ID == r.PathValue("id") {
			target = t
		}
	}
	if target == nil {
		return nil, notFound("no such wake target")
	}
	result, err := a.wakeAndRecord(r.Context(), target)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	return map[string]string{"result": result}, nil
}

// wakeAndRecord wakes a computer and notes how it went.
func (a *App) wakeAndRecord(ctx context.Context, target *WakeTarget) (string, error) {
	id := target.ID
	result, err := a.Wake(ctx, target)
	_ = a.state.Update(func(s *State) error {
		for _, t := range s.WakeTargets {
			if t.ID == id {
				t.LastWake = time.Now()
				if err != nil {
					t.LastResult = "failed: " + err.Error()
				} else {
					t.LastResult = result
				}
			}
		}
		return nil
	})
	if err != nil {
		a.Logf("wake %s (%s) failed: %v", target.Name, target.MAC, err)
		return "", err
	}
	a.Logf("wake %s (%s): %s", target.Name, target.MAC, result)
	return result, nil
}

// Wake sends the magic packet for t and describes what happened.
func (a *App) Wake(ctx context.Context, t *WakeTarget) (string, error) {
	mac, err := wol.ParseMAC(t.MAC)
	if err != nil {
		return "", err
	}
	if t.Method == "server" {
		sent, err := wol.Broadcast(mac, append([]string{"255.255.255.255"}, a.settings.Get().WoL.Broadcasts...))
		if err != nil {
			return "", err
		}
		return "sent from the Sphynx server to " + strings.Join(sent, ", "), nil
	}
	relay, err := a.hs.GetNode(ctx, t.RelayNodeID)
	if err != nil {
		return "", fmt.Errorf("relay device: %w", err)
	}
	res, err := a.wakeVia(ctx, *relay, mac.String())
	if err != nil {
		return "", err
	}
	if len(res.SentTo) == 0 {
		return "", fmt.Errorf("%s could not send the packet: %s", displayName(*relay), strings.Join(res.Errors, "; "))
	}
	return fmt.Sprintf("%s broadcast the wake packet on %s", displayName(*relay), strings.Join(res.SentTo, ", ")), nil
}

// Public shares

var shareNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

func (a *App) apiShares(w http.ResponseWriter, r *http.Request) (any, error) {
	sh := a.state.Get().Shares
	if sh == nil {
		sh = []*Share{}
	}
	return map[string]any{"shares": sh, "enabled": a.settings.Get().Share.Enabled, "serverUrl": a.settings.Get().ServerURL}, nil
}

func (a *App) apiShareSettings(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := a.settings.Update(func(s *Settings) error {
		s.Share.Enabled = req.Enabled
		return nil
	}); err != nil {
		return nil, err
	}
	if req.Enabled {
		a.Logf("public shares turned on")
	} else {
		a.Logf("public shares turned off")
	}
	return a.apiShares(w, r)
}

func (a *App) apiShareSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req Share
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	if !shareNameRe.MatchString(req.Name) {
		return nil, badRequest("share names use lowercase letters, numbers and dashes")
	}
	if req.Port < 1 || req.Port > 65535 {
		return nil, badRequest("enter the port the service listens on, like 8096")
	}
	if _, err := a.hs.GetNode(r.Context(), req.NodeID); err != nil {
		return nil, badRequest("pick the device that runs the service")
	}
	var saved Share
	err := a.state.Update(func(s *State) error {
		for _, x := range s.Shares {
			if x.Name == req.Name && x.ID != req.ID {
				return badRequest("another share already uses that name")
			}
		}
		for _, x := range s.Shares {
			if req.ID != "" && x.ID == req.ID {
				x.Name, x.NodeID, x.Port, x.HTTPS, x.Enabled = req.Name, req.NodeID, req.Port, req.HTTPS, req.Enabled
				saved = *x
				return nil
			}
		}
		req.ID = randomHex(6)
		req.Created = time.Now()
		x := req
		s.Shares = append(s.Shares, &x)
		saved = x
		return nil
	})
	return saved, err
}

func (a *App) apiShareDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	id := r.PathValue("id")
	return nil, a.state.Update(func(s *State) error {
		s.Shares = slices.DeleteFunc(s.Shares, func(x *Share) bool { return x.ID == id })
		return nil
	})
}

// Logs and backup

func (a *App) apiLogs(w http.ResponseWriter, r *http.Request) (any, error) {
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 || n > 2000 {
		n = 400
	}
	src := r.URL.Query().Get("source")
	switch src {
	case "", "sphynx":
		return a.logs.Lines(n), nil
	case "headscale":
		return a.hsProc.Logs.Lines(n), nil
	case "link":
		return a.link.logs(n), nil
	case "home":
		if h := a.helpers.home(); h != nil {
			return h.node.Proc.Logs.Lines(n), nil
		}
		return []string{}, nil
	}
	return nil, badRequest("unknown log source")
}

// serveBackup streams a zip of the data directory (database, keys,
// settings). Restore by stopping Sphynx and unpacking it into the volume.
func (a *App) serveBackup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="sphynx-backup-%s.zip"`, time.Now().Format("2006-01-02")))
	zw := zip.NewWriter(w)
	root := a.cfg.Paths.DataDir
	chatDir := filepath.Join(root, "chat")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if path == chatDir && d != nil && d.IsDir() {
			return fs.SkipDir // added below, as a consistent copy
		}
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSocket != 0 || strings.HasSuffix(path, ".sock") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		zf, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, _ = io.Copy(zf, f)
		return nil
	})
	if a.chat != nil {
		if zf, err := zw.Create("chat/chat.db"); err == nil {
			if err := a.chat.srv.Backup(zf); err != nil {
				a.Logf("backup: chat database: %v", err)
			}
		}
	}
	_ = zw.Close()
}
