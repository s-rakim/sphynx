package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// csp is the Content-Security-Policy for host (already checked by the
// resolver). The app talks to its own address, over http or WebSocket,
// and asks the HTTPS chat address, if any, whether it can move there.
func (h *handler) csp(host string) string {
	connect := "connect-src 'self' ws://" + host + " wss://" + host
	if sa := h.s.networkInfo().SecureAddress; sa != "" {
		if u, err := url.Parse(sa); err == nil && u.Scheme == "https" && u.Host != "" {
			connect += " https://" + u.Host
		}
	}
	return strings.Join([]string{
		"default-src 'self'",
		"img-src 'self' blob: data: https:",
		"media-src 'self' blob:",
		"script-src 'self'",
		"style-src 'self'",
		connect,
		"worker-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}

var securityHeaders = map[string]string{
	"X-Content-Type-Options":       "nosniff",
	"Referrer-Policy":              "no-referrer",
	"Permissions-Policy":           "camera=(self), microphone=(self), geolocation=(self), display-capture=(self)",
	"Cross-Origin-Opener-Policy":   "same-origin",
	"Cross-Origin-Resource-Policy": "same-origin",
	"Strict-Transport-Security":    "max-age=31536000",
}

// Handler serves the web app, the REST API and the WebSocket for the
// devices res identifies. Sphynx runs one per listener (tailnet, local).
func (s *Server) Handler(res Resolver) http.Handler {
	return &handler{s: s, res: res, static: webApp}
}

type handler struct {
	s      *Server
	res    Resolver
	static fs.FS
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for k, v := range securityHeaders {
		w.Header().Set(k, v)
	}
	if !h.res.AllowedHost(hostName(r.Host)) {
		writeError(w, &Error{Status: http.StatusMisdirectedRequest, Message: "Open Sphynx Chat at its own address on your Sphynx network, like http://sphynx-home/."})
		return
	}
	w.Header().Set("Content-Security-Policy", h.csp(r.Host))
	if host := hostName(r.Host); r.TLS == nil && host != "localhost" && !IsLoopback(host) {
		// Browsers ignore (and complain about) this one on plain http.
		w.Header().Del("Cross-Origin-Opener-Policy")
	}
	switch p := r.URL.Path; {
	case p == "/api/reach":
		h.serveReach(w, r)
	case strings.HasPrefix(p, "/api/"):
		h.serveAPI(w, r)
	case p == "/ws":
		h.serveWS(w, r)
	case p == "/dev/login" && h.res.Mode() == "dev":
		devLogin(w, r)
	default:
		h.serveStatic(w, r)
	}
}

// serveReach answers the app at the plain http address, which asks before
// sending a new browser to the HTTPS address: that address only works when
// the name leads through the tailnet, not to the public internet.
func (h *handler) serveReach(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
		return
	}
	_, err := h.res.Identify(r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil})
}

// crossOrigin: browsers send Origin with every WebSocket upgrade and every
// non-GET fetch; it must be this site. Other clients send none.
func crossOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return true
	}
	return !strings.EqualFold(u.Host, r.Host)
}

func (h *handler) serveAPI(w http.ResponseWriter, r *http.Request) {
	rt, params, status := h.s.routes().match(r.Method, r.URL.Path)
	switch status {
	case http.StatusNotFound:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Not found"})
		return
	case http.StatusMethodNotAllowed:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && crossOrigin(r) {
		writeError(w, &Error{Status: http.StatusForbidden, Message: "Cross-origin request refused"})
		return
	}
	id, err := h.res.Identify(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if rt.fresh {
		h.s.refreshDirectory(r.Context(), 10*time.Second)
	}
	var result any
	if rt.stream {
		// Uploads and downloads move file bodies without holding the
		// server lock; they take it themselves around database work.
		err = guard(func() {
			c := &reqCtx{srv: h.s, mode: h.res.Mode(), r: r, w: w, params: params, identity: id}
			h.s.mu.Lock()
			err := guard(func() { h.s.prepare(c, rt) })
			h.s.mu.Unlock()
			if err != nil {
				panic(err)
			}
			result = rt.fn(c)
		})
	} else {
		h.s.mu.Lock()
		err = guard(func() {
			c := &reqCtx{srv: h.s, mode: h.res.Mode(), r: r, w: w, params: params, identity: id}
			h.s.prepare(c, rt)
			result = rt.fn(c)
		})
		h.s.mu.Unlock()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if result == handled {
		return
	}
	if result == nil {
		result = map[string]any{"ok": true}
	}
	writeJSON(w, http.StatusOK, result)
}

// prepare creates the caller's account on first sight and checks the
// device token the route needs.
func (s *Server) prepare(c *reqCtx, rt *route) {
	c.user = s.upsertUser(c.identity)
	auth := c.r.Header.Get("Authorization")
	switch {
	case rt.device != deviceNone && strings.HasPrefix(auth, "Bearer "):
		c.device = s.authenticateToken(auth[len("Bearer "):], c.identity)
	case rt.device == deviceRequired:
		fail(http.StatusUnauthorized, "Missing device token")
	}
}

func (h *handler) serveWS(w http.ResponseWriter, r *http.Request) {
	if crossOrigin(r) {
		writeError(w, &Error{Status: http.StatusForbidden, Message: "Cross-origin WebSocket refused"})
		return
	}
	id, err := h.res.Identify(r)
	if err == nil {
		h.s.mu.Lock()
		err = guard(func() { h.s.upsertUser(id) })
		h.s.mu.Unlock()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	h.s.hub.serve(w, r, id)
}

var staticTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json; charset=utf-8",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".ico":         "image/x-icon",
	".mp3":         "audio/mpeg",
}

func (h *handler) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
		return
	}
	p := r.URL.Path
	// The app shell is only served to devices on the tailnet, too.
	if p == "/" || strings.HasPrefix(p, "/invite/") {
		if _, err := h.res.Identify(r); err != nil {
			var e *Error
			if errors.As(err, &e) && e.Status == http.StatusUnauthorized && h.res.Mode() == "dev" {
				http.Redirect(w, r, "/dev/login", http.StatusFound)
				return
			}
			writeErrorPage(w, r, err)
			return
		}
		p = "/index.html"
	}
	name := strings.TrimPrefix(path.Clean(p), "/")
	if name == "" || strings.HasPrefix(name, ".") || strings.Contains(name, "/.") {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Not found"})
		return
	}
	data, err := fs.ReadFile(h.static, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Not found"})
		return
	}
	ct := staticTypes[path.Ext(name)]
	if ct == "" {
		ct = mime.TypeByExtension(path.Ext(name))
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	// Browsers check back every time (no-cache) and get a 304 while the
	// file is unchanged, which matters on a weak connection.
	tag := etag(name, data)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

var etags sync.Map // file name -> ETag of the embedded file

func etag(name string, data []byte) string {
	if v, ok := etags.Load(name); ok {
		return v.(string)
	}
	sum := sha256.Sum256(data)
	tag := `"` + hex.EncodeToString(sum[:12]) + `"`
	etags.Store(name, tag)
	return tag
}

// writeErrorPage explains in a page, rather than JSON, why the app can't
// open: people see it when they open the address in a browser.
func writeErrorPage(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := errorStatus(err)
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		writeJSON(w, status, map[string]any{"error": msg})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	var b bytes.Buffer
	b.WriteString(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<title>Sphynx Chat</title><body style="font-family:system-ui,sans-serif;max-width:460px;margin:12vh auto;padding:0 20px;line-height:1.5;color:#1f2328">`)
	b.WriteString(`<h1 style="font-size:22px">Sphynx Chat</h1><p>`)
	b.WriteString(htmlEscape(msg))
	b.WriteString(`</p>`)
	var e *Error
	if errors.As(err, &e) && e.Link != "" {
		b.WriteString(`<p><a href="` + htmlEscape(e.Link) + `">` + htmlEscape(e.LinkText) + `</a></p>`)
	}
	_, _ = w.Write(b.Bytes())
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

func errorStatus(err error) (int, string) {
	var e *Error
	if errors.As(err, &e) {
		if e.Status >= 500 && e.Status != http.StatusServiceUnavailable {
			return e.Status, "Internal error"
		}
		return e.Status, e.Message
	}
	return http.StatusInternalServerError, "Internal error"
}

func writeError(w http.ResponseWriter, err error) {
	status, msg := errorStatus(err)
	if status == http.StatusInternalServerError {
		logf("chat request failed: %v", err)
	}
	body := map[string]any{"error": msg}
	var e *Error
	if errors.As(err, &e) && e.Link != "" {
		body["link"], body["linkText"] = e.Link, e.LinkText
	}
	writeJSON(w, status, body)
}

// logf is replaced by the server's logger; package-level so writeError
// can use it without a server.
var logf = func(format string, args ...any) {}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		status = http.StatusInternalServerError
		buf.Reset()
		buf.WriteString(`{"error": "Internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// hostName returns the name in a Host header, without the port.
func hostName(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if strings.HasPrefix(v, "[") {
		if end := strings.Index(v, "]"); end > 0 {
			return v[1:end]
		}
		return ""
	}
	if strings.Count(v, ":") == 1 {
		v, _, _ = strings.Cut(v, ":")
	}
	return strings.TrimSuffix(v, ".")
}

// IsLoopback reports whether addr (an IP, or ip:port) is a loopback address.
func IsLoopback(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.SplitN(host, "%", 2)[0])
	return ip != nil && ip.IsLoopback()
}

// ---- routing ----

type deviceNeed int

const (
	deviceRequired deviceNeed = iota // a device token is required
	deviceOptional                   // used when present
	deviceNone                       // the tailnet identity is enough
)

type reqCtx struct {
	srv      *Server
	mode     string
	r        *http.Request
	w        http.ResponseWriter
	params   map[string]string
	identity *Identity
	user     *userRow
	device   *deviceRow
}

// handled is returned by handlers that wrote the response themselves.
var handled = &struct{ handled bool }{true}

type route struct {
	method string
	re     *regexp.Regexp
	keys   []string
	fn     func(*reqCtx) any
	device deviceNeed
	stream bool // moves a file body; runs without the server lock
	fresh  bool // needs an up-to-date tailnet directory
}

type router struct{ routes []*route }

var paramRe = regexp.MustCompile(`:([a-zA-Z]+)`)

func (rt *router) add(method, pattern string, device deviceNeed, fn func(*reqCtx) any) *route {
	var keys []string
	re := paramRe.ReplaceAllStringFunc(pattern, func(m string) string {
		keys = append(keys, m[1:])
		return "([^/]+)"
	})
	r := &route{method: method, re: regexp.MustCompile("^" + re + "$"), keys: keys, fn: fn, device: device}
	rt.routes = append(rt.routes, r)
	return r
}

func (rt *router) match(method, p string) (*route, map[string]string, int) {
	pathMatched := false
	for _, r := range rt.routes {
		m := r.re.FindStringSubmatch(p)
		if m == nil {
			continue
		}
		pathMatched = true
		if r.method != method {
			continue
		}
		params := map[string]string{}
		for i, k := range r.keys {
			v, err := url.PathUnescape(m[i+1])
			if err != nil {
				v = m[i+1]
			}
			params[k] = v
		}
		return r, params, http.StatusOK
	}
	if pathMatched {
		return nil, nil, http.StatusMethodNotAllowed
	}
	return nil, nil, http.StatusNotFound
}

// ---- request bodies and validation ----

type body map[string]any

func (b body) has(k string) bool {
	_, ok := b[k]
	return ok
}

// readJSON requires application/json, which a cross-site form post can
// never send without a preflight (and the server answers no preflights).
func readJSON(r *http.Request, limit int64) body {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		fail(http.StatusUnsupportedMediaType, "Expected application/json")
	}
	if r.ContentLength > limit {
		fail(http.StatusRequestEntityTooLarge, "Request body too large")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		fail(http.StatusBadRequest, "Could not read the request")
	}
	if int64(len(raw)) > limit {
		fail(http.StatusRequestEntityTooLarge, "Request body too large")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return body{}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		fail(http.StatusBadRequest, "Malformed JSON")
	}
	if dec.More() {
		fail(http.StatusBadRequest, "Malformed JSON")
	}
	m, ok := v.(map[string]any)
	if !ok {
		fail(http.StatusBadRequest, "Expected a JSON object")
	}
	return body(m)
}

const maxJSON = 1 << 20

// vStr validates an optional or required string. Lengths count characters.
func vStr(v any, name string, min, max int, optional bool) *string {
	if v == nil {
		if optional {
			return nil
		}
		failf(http.StatusBadRequest, "%s is required", name)
	}
	s, ok := v.(string)
	if !ok {
		failf(http.StatusBadRequest, "%s must be a string", name)
	}
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n < min {
		failf(http.StatusBadRequest, "%s is too short", name)
	}
	if n > max {
		failf(http.StatusBadRequest, "%s is too long (max %d)", name, max)
	}
	return &s
}

func reqStr(v any, name string, min, max int) string { return *vStr(v, name, min, max, false) }

const maxSafeInt = 1<<53 - 1

// vInt validates an integer: JSON numbers with a fraction or exponent, and
// booleans, are refused.
func vInt(v any, name string, min, max int64, optional bool) *int64 {
	if v == nil {
		if optional {
			return nil
		}
		failf(http.StatusBadRequest, "%s is required", name)
	}
	n, ok := asInt(v)
	if !ok || n < min || n > max {
		failf(http.StatusBadRequest, "%s must be an integer between %d and %d", name, min, max)
	}
	return &n
}

func asInt(v any) (int64, bool) {
	num, ok := v.(json.Number)
	if !ok || strings.ContainsAny(string(num), ".eE") {
		return 0, false
	}
	n, err := num.Int64()
	return n, err == nil
}

func vBool(v any, name string, optional bool) *bool {
	if v == nil {
		if optional {
			return nil
		}
		failf(http.StatusBadRequest, "%s is required", name)
	}
	b, ok := v.(bool)
	if !ok {
		failf(http.StatusBadRequest, "%s must be true or false", name)
	}
	return &b
}

func reqBool(v any, name string) bool { return *vBool(v, name, false) }

func strList(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func uniq(list []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, x := range list {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
