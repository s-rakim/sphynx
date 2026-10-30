package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/s-rakim/sphynx/internal/chat"
	"github.com/s-rakim/sphynx/internal/hs"
)

// Sphynx Chat (internal/chat) is a WhatsApp-style messenger in which every
// device on the tailnet is its own contact. Devices open it through Sphynx
// home: at http://sphynx-home/ on the tailnet, and at https://chat.<domain>
// when Sphynx has a Let's Encrypt certificate (browsers only allow calls,
// voice notes and notifications on HTTPS). The PC running Sphynx can open
// it at http://localhost:3001 as Sphynx home itself.

// ChatSettings are on the Settings page.
type ChatSettings struct {
	Enabled     bool `json:"enabled"`
	MaxUploadMB int  `json:"maxUploadMb"`
	// Secure serves the chat over HTTPS at SecureHost (default
	// chat.<server hostname>) when TLS is on.
	Secure     bool   `json:"secure"`
	SecureHost string `json:"secureHost"`
}

func (c ChatSettings) validate() error {
	if c.MaxUploadMB < 1 || c.MaxUploadMB > 16384 {
		return errors.New("the chat's file size limit must be between 1 MB and 16384 MB")
	}
	if c.SecureHost != "" && (!hostnameRe.MatchString(c.SecureHost) || !strings.Contains(c.SecureHost, ".")) {
		return errors.New("the secure chat address must be a full host name, like chat.example.com")
	}
	return nil
}

// chatService ties the chat server into Sphynx.
type chatService struct {
	a   *App
	srv *chat.Server
	dir *chatDirectory

	tailnet http.Handler // devices on the tailnet
	local   http.Handler // this PC, signed in to the admin UI

	mu        sync.Mutex
	secureOK  string // host whose certificate is ready
	secureErr string
	kick      chan struct{}
}

func newChatService(a *App) (*chatService, error) {
	s := a.settings.Get().Chat
	c := &chatService{a: a, kick: make(chan struct{}, 1)}
	c.dir = &chatDirectory{a: a}
	srv, err := chat.New(chat.Config{
		DataDir:        filepath.Join(a.cfg.Paths.DataDir, "chat"),
		MaxUploadBytes: int64(s.MaxUploadMB) << 20,
		Logf:           a.Logf,
		Network:        c.network,
	}, c.dir)
	if err != nil {
		return nil, err
	}
	c.srv = srv
	c.tailnet = srv.Handler(chatTailnet{c})
	c.local = srv.Handler(chatLocal{c})
	return c, nil
}

func (c *chatService) enabled() bool { return c.a.settings.Get().Chat.Enabled }

func (c *chatService) network() chat.NetworkInfo {
	n := chat.NetworkInfo{Address: "http://" + homeHostname + "/"}
	if host := c.secureReady(); host != "" {
		n.SecureAddress = "https://" + host + "/"
	}
	return n
}

// secureHost is the HTTPS chat address Sphynx should serve, if any.
func (c *chatService) secureHost() string {
	s := c.a.settings.Get()
	if !s.Chat.Enabled || !s.Chat.Secure || s.TLS.Mode == "none" {
		return ""
	}
	if s.Chat.SecureHost != "" {
		return strings.ToLower(s.Chat.SecureHost)
	}
	host := s.TLS.Hostname
	if host == "" {
		if u, err := url.Parse(s.ServerURL); err == nil {
			host = u.Hostname()
		}
	}
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	return "chat." + strings.ToLower(host)
}

// secureReady returns the HTTPS chat host once its certificate works.
func (c *chatService) secureReady() string {
	want := c.secureHost()
	c.mu.Lock()
	defer c.mu.Unlock()
	if want != "" && c.secureOK == want {
		return want
	}
	return ""
}

// ---- admin API ----

func (a *App) apiChat(w http.ResponseWriter, r *http.Request) (any, error) {
	s := a.settings.Get()
	c := a.chat
	want := c.secureHost()
	c.mu.Lock()
	ready, errMsg := c.secureOK == want && want != "", c.secureErr
	c.mu.Unlock()
	addrs := []string{"http://" + homeHostname + "/"}
	for _, ip := range a.homeIPs() {
		if !strings.Contains(ip, ":") {
			addrs = append(addrs, "http://"+ip+"/")
		}
	}
	if ready {
		addrs = append([]string{"https://" + want + "/"}, addrs...)
	}
	localPort := ""
	if _, p, err := net.SplitHostPort(a.cfg.ChatLocalAddr); err == nil {
		localPort = p
	}
	return map[string]any{
		"settings":    s.Chat,
		"tlsOn":       s.TLS.Mode != "none",
		"secureHost":  want,
		"secureReady": ready,
		"secureError": errMsg,
		"addresses":   addrs,
		"localPort":   localPort,
		"homeOnline":  a.helpers.homeNodeID() != "",
	}, nil
}

func (a *App) apiChatSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req ChatSettings
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	req.SecureHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(req.SecureHost), "."))
	if err := req.validate(); err != nil {
		return nil, badRequest(err.Error())
	}
	old := a.settings.Get().Chat
	if err := a.settings.Update(func(s *Settings) error {
		s.Chat = req
		return nil
	}); err != nil {
		return nil, err
	}
	a.Logf("chat settings updated")
	a.chat.srv.SetMaxUploadBytes(int64(req.MaxUploadMB) << 20)
	if old.Enabled && !req.Enabled {
		a.chat.srv.Disconnect()
	}
	if old.Enabled != req.Enabled || old.Secure != req.Secure || old.SecureHost != req.SecureHost {
		// The HTTPS listener only asks Let's Encrypt for names it knows.
		a.front.Reload()
		a.chat.recheck()
	}
	if old.Enabled != req.Enabled {
		if err := a.SyncPolicy(r.Context()); err != nil {
			return nil, err
		}
	}
	return a.apiChat(w, r)
}

// ---- tailnet identity ----

// chatTailnet identifies devices that open the chat over the tailnet.
// tailscaled (in userspace-networking mode) hands each connection from the
// tailnet to Sphynx from a loopback address and can say which device it
// came from; nothing else can connect from loopback inside the container.
type chatTailnet struct{ c *chatService }

func (chatTailnet) Mode() string { return "tailnet" }

func (t chatTailnet) AllowedHost(host string) bool {
	if host == "localhost" || chat.IsLoopback(host) {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return tailnetIP(ip)
	}
	// MagicDNS short names, like sphynx-home, can't be a web page's domain.
	if !strings.Contains(host, ".") {
		return true
	}
	s := t.c.a.settings.Get()
	if bd := strings.ToLower(s.DNS.BaseDomain); bd != "" && strings.HasSuffix(host, "."+bd) {
		return true
	}
	return host == t.c.secureHost()
}

var (
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

func tailnetIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return cgnat.Contains(ip) || tailnetV6.Contains(ip)
}

type connKey struct{}

// connIdentity caches who is behind one TCP connection: the device on the
// other end of a connection never changes, while a loopback port number
// is reused by later connections.
type connIdentity struct {
	mu sync.Mutex
	id *chat.Identity
}

// chatConnContext gives every connection its own identity cache.
func chatConnContext(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, &connIdentity{})
}

func (t chatTailnet) Identify(r *http.Request) (*chat.Identity, error) {
	if !chat.IsLoopback(r.RemoteAddr) {
		return nil, &chat.Error{Status: http.StatusForbidden,
			Message: "Sphynx Chat only opens on devices connected to your Sphynx network. Connect this device with the Tailscale app, turn on \"Use Tailscale DNS\", and try again."}
	}
	ci, _ := r.Context().Value(connKey{}).(*connIdentity)
	if ci != nil {
		ci.mu.Lock()
		defer ci.mu.Unlock()
		if ci.id != nil {
			return ci.id, nil
		}
	}
	id, err := t.c.identifyConn(r.Context(), r.RemoteAddr)
	if err != nil {
		return nil, err
	}
	if ci != nil {
		ci.id = id
	}
	return id, nil
}

func (c *chatService) identifyConn(ctx context.Context, remote string) (*chat.Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	nodes := c.a.helpers.nodes()
	for _, n := range nodes {
		w, err := n.WhoIs(ctx, remote)
		if err != nil {
			continue
		}
		nodeID := strconv.FormatInt(w.Node.ID, 10)
		id, ok := c.dir.byNode(ctx, nodeID)
		if !ok {
			return nil, &chat.Error{Status: http.StatusServiceUnavailable, Message: "Sphynx doesn't know this device yet. Try again in a moment."}
		}
		if id.OS == "" {
			id.OS = w.Node.Hostinfo.OS
		}
		return &id, nil
	}
	return nil, &chat.Error{Status: http.StatusForbidden,
		Message: "Sphynx couldn't tell which of your devices this is. Open the chat from a device connected to Sphynx with the Tailscale app."}
}

// ---- this PC ----

// chatLocal lets the PC that runs Sphynx use the chat as Sphynx home,
// once signed in to the admin UI (browsers send the admin session cookie
// to every port of localhost).
type chatLocal struct{ c *chatService }

func (chatLocal) Mode() string { return "local" }

func (chatLocal) AllowedHost(host string) bool {
	return host == "localhost" || chat.IsLoopback(host)
}

func (l chatLocal) Identify(r *http.Request) (*chat.Identity, error) {
	a := l.c.a
	if !a.authenticated(r) {
		host := "localhost"
		if h, _, err := net.SplitHostPort(r.Host); err == nil && h != "" {
			host = h
		}
		return nil, &chat.Error{Status: http.StatusUnauthorized,
			Message:  "On this PC, Sphynx Chat runs as Sphynx home. Sign in to the Sphynx admin page first, then come back.",
			Link:     "http://" + net.JoinHostPort(host, a.adminPort()) + "/",
			LinkText: "Open the Sphynx admin page"}
	}
	homeID := a.helpers.homeNodeID()
	if homeID == "" {
		return nil, &chat.Error{Status: http.StatusServiceUnavailable, Message: "Sphynx home isn't connected yet. Try again in a minute."}
	}
	id, ok := l.c.dir.byNode(r.Context(), homeID)
	if !ok {
		return nil, &chat.Error{Status: http.StatusServiceUnavailable, Message: "Sphynx home isn't connected yet. Try again in a minute."}
	}
	return &id, nil
}

// ---- directory ----

// chatDirectory lists the tailnet's devices as chat contacts: every
// device except Sphynx's own helper nodes (Sphynx home stays, as this PC).
type chatDirectory struct {
	a    *App
	mu   sync.Mutex
	list []chat.Identity
	at   time.Time
}

func (d *chatDirectory) Devices(ctx context.Context) ([]chat.Identity, error) {
	devs, err := d.a.devices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]chat.Identity, 0, len(devs))
	for _, dv := range devs {
		if dv.Helper && !dv.Home {
			continue
		}
		out = append(out, chatIdentity(dv.Node, dv.OS))
	}
	d.mu.Lock()
	d.list, d.at = out, time.Now()
	d.mu.Unlock()
	return out, nil
}

// byNode finds a device by Headscale node id, refreshing the list when it
// is old or the device is new.
func (d *chatDirectory) byNode(ctx context.Context, nodeID string) (chat.Identity, bool) {
	find := func() (chat.Identity, bool, bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, id := range d.list {
			if id.NodeID == nodeID {
				return id, true, time.Since(d.at) < time.Minute
			}
		}
		return chat.Identity{}, false, false
	}
	if id, ok, fresh := find(); ok && fresh {
		return id, true
	}
	if _, err := d.Devices(ctx); err != nil {
		id, ok, _ := find()
		return id, ok
	}
	id, ok, _ := find()
	return id, ok
}

func chatIdentity(n hs.Node, osName string) chat.Identity {
	owner := ""
	switch {
	case len(n.Tags) > 0:
		owner = strings.Join(n.Tags, ", ")
	case n.User != nil && n.User.DisplayName != "":
		owner = n.User.DisplayName
	case n.User != nil:
		owner = n.User.Name
	}
	return chat.Identity{
		ID:     chat.DeviceUserID(n.ID, n.MachineKey),
		Name:   displayName(n),
		Owner:  owner,
		OS:     osName,
		NodeID: n.ID,
	}
}

// ---- HTTPS ----

// watchSecure gets the HTTPS chat address's certificate, and once it works
// points the name at Sphynx home for devices on the tailnet (a MagicDNS
// record). The certificate itself comes from Let's Encrypt over the
// internet, so the name must also resolve publicly to this server.
func (c *chatService) watchSecure(ctx context.Context) {
	// Let's Encrypt allows 5 failed attempts per name and hour.
	const firstRetry = 5 * time.Minute
	backoff := firstRetry
	for {
		host := c.secureHost()
		err := errors.New("off")
		if host != "" {
			err = c.a.front.checkCertificate(ctx, host)
		}
		c.mu.Lock()
		changed := false
		if err == nil {
			changed = c.secureOK != host
			c.secureOK, c.secureErr = host, ""
			backoff = firstRetry
		} else {
			changed = c.secureOK != ""
			c.secureOK = ""
			if host != "" {
				c.secureErr = err.Error()
			} else {
				c.secureErr = ""
			}
		}
		c.mu.Unlock()
		if changed {
			if err == nil {
				c.a.Logf("chat: secure address https://%s/ is ready", host)
			}
			c.a.syncManagedRecords()
		} else if host != "" && err != nil {
			c.a.Logf("chat: no certificate for %s yet: %v", host, err)
		}
		wait := 6 * time.Hour
		if host != "" && err != nil {
			wait = backoff
			backoff = min(backoff*2, 6*time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-c.kick:
			backoff = firstRetry
		case <-time.After(wait):
		}
	}
}

// recheck makes watchSecure look again now (settings changed).
func (c *chatService) recheck() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// certCovers reports whether a certificate is valid for host.
func certCovers(cert *tls.Certificate, host string) error {
	if cert == nil || len(cert.Certificate) == 0 {
		return errors.New("no certificate")
	}
	leaf := cert.Leaf
	if leaf == nil {
		var err error
		if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return err
		}
	}
	if time.Now().After(leaf.NotAfter) {
		return errors.New("the certificate has expired")
	}
	return leaf.VerifyHostname(host)
}

// managedRecords are DNS records Sphynx adds for its own features.
func (a *App) managedRecords() []extraRecord {
	var out []extraRecord
	if a.chat == nil {
		return out
	}
	host := a.chat.secureReady()
	if host == "" {
		return out
	}
	for _, ip := range a.homeIPs() {
		typ := "A"
		if strings.Contains(ip, ":") {
			typ = "AAAA"
		}
		out = append(out, extraRecord{Name: host, Type: typ, Value: ip, Managed: true})
	}
	return out
}

// homeIPs are Sphynx home's tailnet addresses, as last seen.
func (a *App) homeIPs() []string {
	st, _, _ := a.helpers.snapshot()
	if st == nil || st.Self == nil {
		return nil
	}
	return st.Self.TailscaleIPs
}

// serveChatLocal serves the chat to the PC itself (published on the
// host's localhost only).
func (a *App) serveChatLocal(ctx context.Context) error {
	if a.cfg.ChatLocalAddr == "" {
		return nil
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.chat.enabled() {
			http.Error(w, "Sphynx Chat is turned off. Turn it on under Chat in the Sphynx admin page.", http.StatusNotFound)
			return
		}
		a.chat.local.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: a.cfg.ChatLocalAddr, Handler: h, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if err != nil {
		// The chat for this PC is optional; never take Sphynx down for it.
		a.Logf("chat for this PC can't listen on %s: %v", a.cfg.ChatLocalAddr, err)
		<-ctx.Done()
	}
	return nil
}
