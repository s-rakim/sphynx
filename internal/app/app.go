// Package app is the Sphynx server: it runs Headscale, Sphynx's own tailnet
// helper nodes, the public front server and the admin web UI.
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/s-rakim/sphynx/internal/hs"
	"github.com/s-rakim/sphynx/internal/proc"
)

// Config is fixed at startup (from flags and environment).
type Config struct {
	Paths   Paths
	Version string
	// AdminAddr serves the web UI, e.g. ":3000".
	AdminAddr string
	// HTTPAddr serves the control protocol over plain HTTP, e.g. ":8080".
	HTTPAddr string
	// TLSAddr and ACMEAddr are used when TLS is on.
	TLSAddr  string
	ACMEAddr string
	// HomeWGPort is the WireGuard UDP port of Sphynx home (publish it).
	HomeWGPort int
	// ChatLocalAddr serves Sphynx Chat to the PC itself, e.g. ":3001".
	ChatLocalAddr string
}

type App struct {
	cfg      Config
	settings *jsonFile[Settings]
	secrets  *jsonFile[Secrets]
	state    *jsonFile[State]

	hs     *hs.Client
	hsProc *proc.Process
	logs   *proc.Ring

	ctx     context.Context
	helpers *helperSet
	front   *front
	link    *link
	chat    *chatService
	radio   *radioService
	limiter *loginLimiter
	// signinLimiter guards the public device sign-in page.
	signinLimiter *loginLimiter

	readyMu  sync.Mutex
	ready    bool // Headscale API reachable with a valid key
	readyErr string

	policyMu sync.Mutex
}

func New(cfg Config) (*App, error) {
	p := cfg.Paths
	for _, d := range []string{p.HeadscaleDir(), p.SphynxDir(), p.TailscaleDir(), p.RunDir, p.ACMECache()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	a := &App{cfg: cfg, logs: proc.NewRing(2000), limiter: &loginLimiter{failures: map[string][]time.Time{}}, signinLimiter: &loginLimiter{failures: map[string][]time.Time{}}}
	var err error
	if a.settings, err = loadJSON(filepath.Join(p.SphynxDir(), "settings.json"), DefaultSettings()); err != nil {
		return nil, err
	}
	if a.secrets, err = loadJSON(filepath.Join(p.SphynxDir(), "secrets.json"), Secrets{}); err != nil {
		return nil, err
	}
	if a.state, err = loadJSON(filepath.Join(p.SphynxDir(), "state.json"), defaultState()); err != nil {
		return nil, err
	}
	if err := a.secrets.Update(func(s *Secrets) error {
		if s.SessionKey == "" {
			s.SessionKey = randomHex(32)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	a.applyEnv()
	a.hs = hs.New(headscaleURL, nil)
	a.hs.SetKey(a.secrets.Get().APIKey)
	a.hsProc = proc.New("headscale", func() []string {
		return []string{p.HeadscaleBin, "serve", "--config", p.HeadscaleConfig()}
	}, nil)
	a.hsProc.Keep = keepHeadscaleLine
	a.helpers = newHelperSet(a)
	a.front = newFront(a)
	a.link = &link{a: a}
	if a.chat, err = newChatService(a); err != nil {
		return nil, err
	}
	a.radio = newRadioService(a)
	return a, nil
}

// keepHeadscaleLine drops the access-log line Headscale writes for every
// successful API call Sphynx makes, which would otherwise bury everything
// else in the log.
func keepHeadscaleLine(line string) bool {
	return !(strings.Contains(line, "http request") &&
		strings.Contains(line, "path=/api/v1/") &&
		strings.Contains(line, "remote=127.0.0.1") &&
		strings.Contains(line, "status=2"))
}

// adminPort is the port of the admin UI, used by Sphynx Link.
func (a *App) adminPort() string {
	_, port, err := net.SplitHostPort(a.cfg.AdminAddr)
	if err != nil || port == "" {
		return "3000"
	}
	return port
}

// Logf logs to stderr and the in-memory Sphynx log shown in the UI.
func (a *App) Logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Print(msg)
	a.logs.Add(time.Now().Format("15:04:05 ") + msg)
}

// applyEnv seeds settings from environment variables. Env values only fill
// in things the admin has not set up yet, so the UI stays the source of truth.
func (a *App) applyEnv() {
	if pw := os.Getenv("SPHYNX_ADMIN_PASSWORD"); pw != "" && !a.PasswordSet() {
		if err := a.SetPassword(pw); err != nil {
			a.Logf("SPHYNX_ADMIN_PASSWORD ignored: %v", err)
		}
	}
	_ = a.settings.Update(func(s *Settings) error {
		if s.SetupDone {
			return nil
		}
		if v := os.Getenv("SPHYNX_SERVER_URL"); v != "" {
			s.ServerURL = strings.TrimRight(v, "/")
		}
		if v := os.Getenv("SPHYNX_BASE_DOMAIN"); v != "" {
			s.DNS.BaseDomain = v
		}
		return nil
	})
}

// Run starts everything and blocks until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	a.ctx = ctx
	if err := a.writeHeadscaleFiles(); err != nil {
		return err
	}
	a.updateHosts()
	go a.hsProc.Run(ctx)
	go a.refreshPublicDERPMap(ctx)
	go a.bootstrap(ctx)
	go a.helpers.loop(ctx)
	go a.link.apply(ctx)

	go a.chat.srv.Run(ctx)
	go a.chat.watchSecure(ctx)
	go a.radio.apply(ctx)

	errc := make(chan error, 3)
	go func() { errc <- a.serveAdmin(ctx) }()
	go func() { errc <- a.front.run(ctx) }()
	go func() { errc <- a.serveChatLocal(ctx) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

// writeHeadscaleFiles renders config.yaml and makes sure the DNS records
// file exists (Headscale watches it and reloads records on change).
func (a *App) writeHeadscaleFiles() error {
	s := a.settings.Get()
	if s.DERP.UsePublic {
		if err := a.ensurePublicDERPMap(); err != nil {
			return err
		}
	}
	cfg, err := s.HeadscaleConfig(a.cfg.Paths, a.secrets.Get().OIDCSecret)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(a.cfg.Paths.HeadscaleConfig(), cfg, 0o600); err != nil {
		return err
	}
	if _, err := os.Stat(a.cfg.Paths.ExtraRecords()); errors.Is(err, os.ErrNotExist) {
		return writeFileAtomic(a.cfg.Paths.ExtraRecords(), []byte("[]\n"), 0o644)
	}
	return nil
}

// updateHosts points the public hostname at this container, so Sphynx's
// own nodes reach the server and its relay locally instead of hairpinning
// through the router.
func (a *App) updateHosts() {
	u, err := url.Parse(a.settings.Get().ServerURL)
	if err != nil {
		return
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil || host == "localhost" || host == "" {
		return
	}
	const marker = "# sphynx"
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if !strings.HasSuffix(l, marker) {
			lines = append(lines, l)
		}
	}
	lines = append(lines, "127.0.0.1 "+host+" "+marker)
	if err := os.WriteFile("/etc/hosts", []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		a.Logf("could not update /etc/hosts: %v", err)
	}
}

// RestartHeadscale rewrites the config and restarts Headscale.
func (a *App) RestartHeadscale() error {
	if err := a.writeHeadscaleFiles(); err != nil {
		return err
	}
	a.updateHosts()
	a.setReady(false, "restarting Headscale")
	a.hsProc.Restart()
	go a.bootstrap(a.ctx)
	return nil
}

func (a *App) setReady(ok bool, why string) {
	a.readyMu.Lock()
	a.ready, a.readyErr = ok, why
	a.readyMu.Unlock()
}

func (a *App) Ready() (bool, string) {
	a.readyMu.Lock()
	defer a.readyMu.Unlock()
	return a.ready, a.readyErr
}

// bootstrap waits for Headscale, makes sure Sphynx has a working API key,
// pushes the access policy and starts the helper nodes.
func (a *App) bootstrap(ctx context.Context) {
	deadline := time.Now().Add(3 * time.Minute)
	for ctx.Err() == nil {
		err := a.ensureAPIKey(ctx)
		if err == nil {
			break
		}
		a.setReady(false, err.Error())
		if time.Now().After(deadline) {
			a.Logf("Headscale is still not answering: %v", err)
			deadline = time.Now().Add(3 * time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	if ctx.Err() != nil {
		return
	}
	a.setReady(true, "")
	a.Logf("Headscale is up")
	if err := a.SyncPolicy(ctx); err != nil {
		a.Logf("could not apply access policy: %v", err)
	}
	a.helpers.reconcile(ctx)
}

func (a *App) ensureAPIKey(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.hs.Health(cctx); err != nil {
		if !errors.Is(err, hs.ErrUnauthorized) {
			return fmt.Errorf("waiting for Headscale: %w", err)
		}
	}
	if a.hs.Key() != "" {
		_, err := a.hs.ListUsers(cctx)
		if err == nil {
			return nil
		}
		if !errors.Is(err, hs.ErrUnauthorized) {
			return err
		}
		a.Logf("stored API key was rejected, creating a new one")
	}
	key, err := a.headscaleCLI(cctx, "apikeys", "create", "--expiration", "87600h")
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if i := strings.LastIndex(key, "\n"); i >= 0 {
		key = strings.TrimSpace(key[i+1:])
	}
	a.hs.SetKey(key)
	if _, err := a.hs.ListUsers(cctx); err != nil {
		return fmt.Errorf("new API key does not work: %w", err)
	}
	return a.secrets.Update(func(s *Secrets) error {
		s.APIKey = key
		return nil
	})
}

// headscaleCLI runs the headscale CLI, which talks to the server over its
// unix socket and needs no API key.
func (a *App) headscaleCLI(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"--config", a.cfg.Paths.HeadscaleConfig()}, args...)
	cmd := exec.CommandContext(ctx, a.cfg.Paths.HeadscaleBin, full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("headscale %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// SyncPolicy pushes base policy + managed rules to Headscale.
func (a *App) SyncPolicy(ctx context.Context) error {
	a.policyMu.Lock()
	defer a.policyMu.Unlock()
	st := a.state.Get()
	if st.BasePolicy == "" {
		// Adopt a policy someone set directly in Headscale, otherwise start
		// from the default.
		base := DefaultPolicy
		if p, err := a.hs.GetPolicy(ctx); err == nil && strings.TrimSpace(p.Policy) != "" {
			base = p.Policy
		}
		if err := a.state.Update(func(s *State) error {
			s.BasePolicy = base
			return nil
		}); err != nil {
			return err
		}
		st.BasePolicy = base
	}
	eff, err := EffectivePolicy(st.BasePolicy, ManagedRules(a.policyInputs(ctx)))
	if err != nil {
		return err
	}
	cur, err := a.hs.GetPolicy(ctx)
	if err == nil && cur.Policy == eff {
		return nil
	}
	_, err = a.hs.SetPolicy(ctx, eff)
	return err
}

func (a *App) policyInputs(ctx context.Context) PolicyInputs {
	s := a.settings.Get()
	st := a.state.Get()
	in := PolicyInputs{Resilience: s.Resilience, HomeRelay: s.Home.RelayPort > 0, Chat: s.Chat.Enabled, AdminPort: a.adminPort()}
	nodes, err := a.hs.ListNodes(ctx)
	if err != nil {
		return in
	}
	home := a.helpers.homeNodeID()
	for _, n := range nodes {
		if o := st.Devices[n.ID]; o != nil {
			if o.StrictFirewall {
				in.StrictIPs = append(in.StrictIPs, n.IPAddresses...)
			}
			if o.PeerRelay && n.ID != home {
				in.RelayIPs = append(in.RelayIPs, n.IPAddresses...)
			}
		}
		if n.ID == home && home != "" {
			in.HomeIPs = append(in.HomeIPs, n.IPAddresses...)
		}
	}
	slices.Sort(in.StrictIPs)
	slices.Sort(in.RelayIPs)
	return in
}

// ownerUser returns the Headscale user Sphynx home belongs to.
func (a *App) ownerUser(ctx context.Context) (*hs.User, error) {
	users, err := a.hs.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	owner := a.settings.Get().Owner
	for i := range users {
		if users[i].Name == owner {
			return &users[i], nil
		}
	}
	return nil, nil
}
