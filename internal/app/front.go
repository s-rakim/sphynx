package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/s-rakim/sphynx/internal/chat"
)

// front owns the public ports. It terminates TLS, serves public shares under
// /s/<name>/ and proxies everything else to Headscale, including the
// Tailscale control protocol upgrade (/ts2021) and the DERP relay (/derp).
type front struct {
	a     *App
	proxy *httputil.ReverseProxy

	mu      sync.Mutex
	servers []*http.Server
	restart chan struct{}
	tlsErr  string
	acme    *autocert.Manager // when TLS mode is letsencrypt
	manual  *keyPair          // when TLS mode is manual
}

func newFront(a *App) *front {
	target, _ := url.Parse(headscaleURL)
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.SetXForwarded()
			r.Out.Host = r.In.Host
		},
		// Long-poll and upgraded connections must not be buffered.
		FlushInterval: -1,
		ErrorLog:      log.New(os.Stderr, "[front] ", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Sphynx control server is starting, try again in a moment", http.StatusBadGateway)
		},
	}
	return &front{a: a, proxy: rp, restart: make(chan struct{}, 1)}
}

func (f *front) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/s/", f.a.serveShare)
	mux.HandleFunc("/register/{authid}", f.a.serveSignin)
	mux.HandleFunc("/auth/{authid}", f.a.serveCheck)
	mux.Handle("/", f.proxy)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The HTTPS chat address is its own site on the same port.
		if c := f.a.chat; c != nil {
			if host := c.secureHost(); host != "" && strings.EqualFold(hostOnly(r.Host), host) {
				c.tailnet.ServeHTTP(w, r)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// port80 serves the chat to devices on the tailnet (their connections
// arrive from loopback, through Sphynx home) and Let's Encrypt challenges
// and HTTPS redirects to everyone else. acme may be nil.
func (f *front) port80(acme http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if chat.IsLoopback(r.RemoteAddr) {
			if c := f.a.chat; c != nil && c.enabled() {
				c.tailnet.ServeHTTP(w, r)
				return
			}
			http.Error(w, "Sphynx Chat is turned off. The admin can turn it on under Chat in the Sphynx admin page.", http.StatusNotFound)
			return
		}
		if acme != nil {
			acme.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.TrimSuffix(h, ".")
	}
	return strings.TrimSuffix(hostport, ".")
}

// checkCertificate makes sure there is a working certificate for host,
// asking Let's Encrypt for one if needed.
func (f *front) checkCertificate(ctx context.Context, host string) error {
	f.mu.Lock()
	m, kp := f.acme, f.manual
	f.mu.Unlock()
	switch {
	case m != nil:
		// A hello that supports ECDSA gets the same certificate browsers will.
		hello := &tls.ClientHelloInfo{
			ServerName:        host,
			SignatureSchemes:  []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
			SupportedCurves:   []tls.CurveID{tls.CurveP256},
			CipherSuites:      []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
			SupportedVersions: []uint16{tls.VersionTLS13, tls.VersionTLS12},
		}
		cert, err := m.GetCertificate(hello)
		if err != nil {
			return err
		}
		return certCovers(cert, host)
	case kp != nil:
		cert, err := kp.get(nil)
		if err != nil {
			return err
		}
		if err := certCovers(cert, host); err != nil {
			return fmt.Errorf("the certificate in %s doesn't cover %s", kp.cert, host)
		}
		return nil
	}
	return errors.New("TLS is off")
}

// Reload restarts the listeners with the current TLS settings.
func (f *front) Reload() {
	select {
	case f.restart <- struct{}{}:
	default:
	}
}

func (f *front) run(ctx context.Context) error {
	for {
		if err := f.start(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			f.stop()
			return nil
		case <-f.restart:
			f.stop()
		}
	}
}

func (f *front) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.servers {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = s.Shutdown(ctx)
		cancel()
		_ = s.Close()
	}
	f.servers = nil
}

func (f *front) start() error {
	s := f.a.settings.Get()
	h := f.handler()
	var servers []*http.Server
	server := func(addr string, h http.Handler) *http.Server {
		srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 30 * time.Second, ConnContext: chatConnContext}
		servers = append(servers, srv)
		return srv
	}

	server(f.a.cfg.HTTPAddr, h)

	f.tlsErr = ""
	var m *autocert.Manager
	var kp *keyPair
	var on80 http.Handler
	switch s.TLS.Mode {
	case "letsencrypt":
		hosts := []string{s.TLS.Hostname}
		if c := f.a.chat; c != nil {
			if ch := c.secureHost(); ch != "" && ch != s.TLS.Hostname {
				hosts = append(hosts, ch)
			}
		}
		m = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(hosts...),
			Cache:      autocert.DirCache(f.a.cfg.Paths.ACMECache()),
			Email:      s.TLS.Email,
		}
		tc := m.TLSConfig()
		// The Tailscale control protocol and DERP need HTTP/1.1 upgrades.
		tc.NextProtos = []string{"http/1.1", acme.ALPNProto}
		srv := server(f.a.cfg.TLSAddr, h)
		srv.TLSConfig = tc
		srv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
		if s.TLS.ChallengeType == "HTTP-01" {
			on80 = m.HTTPHandler(redirectHTTPS())
		}
	case "manual":
		kp = &keyPair{cert: s.TLS.CertPath, key: s.TLS.KeyPath}
		if _, err := kp.get(nil); err != nil {
			f.tlsErr = err.Error()
			f.a.Logf("TLS certificate problem: %v", err)
		}
		srv := server(f.a.cfg.TLSAddr, h)
		srv.TLSConfig = &tls.Config{GetCertificate: kp.get, NextProtos: []string{"http/1.1"}}
		srv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
		on80 = redirectHTTPS()
	}
	// Port 80 always listens: it is also where devices open the chat.
	server(f.a.cfg.ACMEAddr, f.port80(on80))

	f.mu.Lock()
	f.servers = servers
	f.acme, f.manual = m, kp
	f.mu.Unlock()
	if c := f.a.chat; c != nil {
		c.recheck()
	}
	for _, srv := range servers {
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			f.a.Logf("can't listen on %s: %v", srv.Addr, err)
			continue
		}
		go func(srv *http.Server, ln net.Listener) {
			var err error
			if srv.TLSConfig != nil {
				err = srv.ServeTLS(ln, "", "")
			} else {
				err = srv.Serve(ln)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				f.a.Logf("server on %s stopped: %v", srv.Addr, err)
			}
		}(srv, ln)
	}
	return nil
}

func redirectHTTPS() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

// keyPair reloads a certificate from disk when the files change, so
// renewals by another tool are picked up without a restart.
type keyPair struct {
	cert, key string
	mu        sync.Mutex
	loaded    *tls.Certificate
	mtime     time.Time
}

func (k *keyPair) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	st, err := os.Stat(k.cert)
	if err != nil {
		return nil, fmt.Errorf("certificate: %w", err)
	}
	if k.loaded != nil && st.ModTime().Equal(k.mtime) {
		return k.loaded, nil
	}
	c, err := tls.LoadX509KeyPair(k.cert, k.key)
	if err != nil {
		return nil, err
	}
	k.loaded, k.mtime = &c, st.ModTime()
	return k.loaded, nil
}

// serveShare publishes a tailnet service at /s/<name>/, proxied through
// Sphynx home. This is Sphynx's stand-in for Tailscale Funnel.
func (a *App) serveShare(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/s/")
	name, _, _ := strings.Cut(rest, "/")
	if !a.settings.Get().Share.Enabled {
		http.NotFound(w, r)
		return
	}
	var sh *Share
	for _, s := range a.state.Get().Shares {
		if s.Name == name && s.Enabled {
			sh = s
			break
		}
	}
	if sh == nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/s/"+name {
		http.Redirect(w, r, "/s/"+name+"/", http.StatusFound)
		return
	}
	home := a.helpers.home()
	if home == nil {
		http.Error(w, "Sphynx home is not connected", http.StatusServiceUnavailable)
		return
	}
	node, err := a.hs.GetNode(r.Context(), sh.NodeID)
	if err != nil || len(node.IPAddresses) == 0 {
		http.Error(w, "the shared device is not on the network", http.StatusBadGateway)
		return
	}
	ip := node.IPAddresses[0]
	for _, x := range node.IPAddresses {
		if !strings.Contains(x, ":") {
			ip = x
		}
	}
	scheme := "http"
	if sh.HTTPS {
		scheme = "https"
	}
	target := &url.URL{Scheme: scheme, Host: net.JoinHostPort(ip, strconv.Itoa(sh.Port))}
	prefix := "/s/" + name
	transport := home.node.HTTPClient(0).Transport.(*http.Transport)
	if sh.HTTPS {
		// Tailnet services usually have self-signed certificates; the hop
		// itself is already encrypted by WireGuard.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	rp := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, prefix)
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
			pr.Out.URL.RawPath = ""
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Prefix", prefix)
			// Browsers send cookies regardless of port, so never pass the
			// admin session on to a shared service.
			dropCookie(pr.Out.Header, sessionCookie)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "the shared service didn't answer: "+err.Error(), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

func dropCookie(h http.Header, name string) {
	var keep []string
	for _, line := range h.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			k, _, _ := strings.Cut(strings.TrimSpace(part), "=")
			if k != name && strings.TrimSpace(part) != "" {
				keep = append(keep, strings.TrimSpace(part))
			}
		}
	}
	h.Del("Cookie")
	if len(keep) > 0 {
		h.Set("Cookie", strings.Join(keep, "; "))
	}
}
