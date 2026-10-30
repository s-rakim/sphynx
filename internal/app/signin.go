package app

import (
	"context"
	"encoding/base64"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/s-rakim/sphynx/web"
)

// serveSignin is the page a device opens when it joins without a key
// (iPhone, Apple TV, `tailscale up` without --auth-key). Headscale's own
// page only shows a command to run on the server, which is no help when
// the server is at home and you're holding a phone. Sphynx's page lets the
// admin approve the device right there with the admin password. With
// single sign-on (OIDC) turned on, Headscale's sign-in flow is used.
func (a *App) serveSignin(w http.ResponseWriter, r *http.Request) {
	a.serveAuthPage(w, r, false)
}

// serveCheck is the page Tailscale SSH sends people to for rules with
// "action": "check": they confirm it's them before the session starts.
// Like the sign-in page, it takes the admin password instead of a command
// on the server.
func (a *App) serveCheck(w http.ResponseWriter, r *http.Request) {
	a.serveAuthPage(w, r, true)
}

func (a *App) serveAuthPage(w http.ResponseWriter, r *http.Request, check bool) {
	s := a.settings.Get()
	if s.OIDC.Enabled {
		a.front.proxy.ServeHTTP(w, r)
		return
	}
	authID := r.PathValue("authid")
	if !authIDRe.MatchString(authID) {
		http.NotFound(w, r)
		return
	}
	d := signinData{Owner: s.Owner, Icon: signinIcon, Check: check, Insecure: r.TLS == nil && !strings.HasPrefix(s.ServerURL, "https://")}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if check {
			d.Error = a.approveCheck(r.Context(), clientIP(r), authID, r.PostForm.Get("password"))
		} else {
			d.User = strings.TrimSpace(r.PostForm.Get("user"))
			d.Error, d.Device = a.approveSignin(r.Context(), clientIP(r), authID, r.PostForm.Get("password"), d.User)
		}
		d.Done = d.Error == ""
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	_ = signinTmpl.Execute(w, d)
}

type signinData struct {
	Icon     template.URL
	Owner    string
	User     string
	Device   string
	Error    string
	Done     bool
	Check    bool
	Insecure bool
}

// signinPassword checks the admin password typed on the sign-in and check
// pages. It returns a message for the person on failure.
func (a *App) signinPassword(ip, password string) string {
	// Per address, and overall in case every visitor shows up with the
	// same address (Docker Desktop hides real client addresses).
	if !a.signinLimiter.allowed(ip, 5) || !a.signinLimiter.allowed("*", 20) {
		return "Too many wrong passwords. Wait 15 minutes, or approve it in the Sphynx admin page."
	}
	if !a.checkPassword(password) {
		a.signinLimiter.fail(ip)
		a.signinLimiter.fail("*")
		time.Sleep(500 * time.Millisecond)
		return "That isn't the Sphynx admin password."
	}
	return ""
}

// expiredAuth reports whether Headscale refused an auth request because it
// timed out or was already answered.
func expiredAuth(msg string) bool {
	return strings.Contains(msg, "not found") || strings.Contains(msg, "expired") || strings.Contains(msg, "cache")
}

// approveSignin checks the admin password and registers the device. It
// returns a message for the person on failure.
func (a *App) approveSignin(ctx context.Context, ip, authID, password, user string) (errMsg, device string) {
	if msg := a.signinPassword(ip, password); msg != "" {
		return msg, ""
	}
	if user == "" {
		user = a.settings.Get().Owner
	}
	if user == "" {
		return "Finish setting up Sphynx in its admin page first.", ""
	}
	n, err := a.hs.AuthRegister(ctx, strings.ToLower(user), authID)
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "user") && strings.Contains(msg, "not found"):
			return "There's no Sphynx user called “" + user + "”. Leave the field empty to add the device for " + a.settings.Get().Owner + ".", ""
		case expiredAuth(msg):
			return "This sign-in request has expired or was already used. Start signing in again on the device.", ""
		}
		a.Logf("sign-in page: approving %s failed: %v", authID, err)
		return "Sphynx couldn't approve the device: " + msg, ""
	}
	a.signinLimiter.reset(ip)
	name := n.GivenName
	if name == "" {
		name = n.Name
	}
	a.Logf("device %s approved on the sign-in page for %s (from %s)", name, user, ip)
	return "", name
}

// approveCheck checks the admin password and lets a waiting check, such as
// a Tailscale SSH session, go ahead.
func (a *App) approveCheck(ctx context.Context, ip, authID, password string) string {
	if msg := a.signinPassword(ip, password); msg != "" {
		return msg
	}
	if err := a.hs.AuthApprove(ctx, authID); err != nil {
		if expiredAuth(err.Error()) {
			return "This request has expired or was already answered. Try again on the device."
		}
		a.Logf("check page: approving %s failed: %v", authID, err)
		return "Sphynx couldn't approve the request: " + err.Error()
	}
	a.signinLimiter.reset(ip)
	a.Logf("check %s approved on the check page (from %s)", authID, ip)
	return ""
}

// signinIcon is the Sphynx logo as a data: URL, so the page needs no other
// requests.
var signinIcon = func() template.URL {
	b, err := web.Files.ReadFile("icon.svg")
	if err != nil {
		return ""
	}
	return template.URL("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(b))
}()

var signinTmpl = template.Must(template.New("signin").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{if .Check}}Confirm it's you{{else}}Add device{{end}} · Sphynx</title>
<style>
:root { color-scheme: light dark; --bg: #f4f4f8; --card: #fff; --text: #17171c; --muted: #5d5d6b; --border: #e2e2ea; --accent: #5b45e8; --bad: #c62828; --good: #13804b; --warn-bg: #fff4dc; --warn: #7a4b00; }
@media (prefers-color-scheme: dark) { :root { --bg: #111116; --card: #1b1b22; --text: #ececf2; --muted: #a2a2b2; --border: #2e2e39; --accent: #8f80ff; --bad: #ff7a7a; --good: #5fd39a; --warn-bg: #3a2c0c; --warn: #ffd27a; } }
* { box-sizing: border-box; }
body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 20px 16px; background: var(--bg); color: var(--text); font: 16px/1.5 system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
main { width: min(440px, 100%); background: var(--card); border: 1px solid var(--border); border-radius: 16px; padding: 28px 24px; box-shadow: 0 6px 24px rgba(0,0,0,.06); }
.brand { display: flex; align-items: center; gap: 10px; font-weight: 700; font-size: 18px; margin-bottom: 18px; }
.logo { width: 34px; height: 34px; }
h1 { font-size: 22px; margin: 0 0 6px; letter-spacing: -.01em; }
p { margin: 0 0 14px; color: var(--muted); }
label { display: block; font-weight: 600; font-size: 14px; margin: 14px 0 6px; }
input { width: 100%; font: inherit; padding: 11px 12px; border-radius: 10px; border: 1px solid var(--border); background: var(--bg); color: var(--text); }
input:focus { outline: 2px solid var(--accent); outline-offset: 1px; }
.hint { font-size: 13px; color: var(--muted); margin-top: 4px; }
button { margin-top: 20px; width: 100%; font: inherit; font-weight: 650; padding: 12px; border: 0; border-radius: 10px; background: var(--accent); color: #fff; cursor: pointer; }
.error { color: var(--bad); font-weight: 600; margin: 12px 0 0; }
.warn { background: var(--warn-bg); color: var(--warn); border-radius: 10px; padding: 10px 12px; font-size: 14px; margin-bottom: 14px; }
.done { color: var(--good); font-size: 40px; line-height: 1; margin-bottom: 8px; }
.small { font-size: 13px; margin-top: 18px; }
</style>
</head>
<body>
<main>
  <div class="brand"><img class="logo" src="{{.Icon}}" alt="">Sphynx</div>
  {{if and .Done .Check}}
  <div class="done" aria-hidden="true">✓</div>
  <h1>Allowed</h1>
  <p>You can close this page and go back to the device.</p>
  {{else if .Check}}
  <h1>Confirm it's you</h1>
  <p>A device on your private network asks you to confirm it's you, for example before a Tailscale SSH session starts. Enter your Sphynx admin password to allow it.</p>
  {{if .Insecure}}<div class="warn">HTTPS is off, so this page isn't encrypted. Only use it on your home network, or turn on HTTPS in Sphynx's settings.</div>{{end}}
  <form method="post" autocomplete="off">
    <label for="password">Admin password</label>
    <input id="password" name="password" type="password" required autocomplete="current-password" autofocus>
    {{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
    <button type="submit">Allow</button>
  </form>
  <p class="small">Not expecting this? Close the page and nothing is allowed.</p>
  {{else if .Done}}
  <div class="done" aria-hidden="true">✓</div>
  <h1>Device added</h1>
  <p>{{if .Device}}<strong>{{.Device}}</strong> is{{else}}This device is{{end}} now on your private network. You can close this page and go back to the Tailscale app.</p>
  {{else}}
  <h1>Add this device</h1>
  <p>This device is asking to join your private network. Enter your Sphynx admin password to let it in.</p>
  {{if .Insecure}}<div class="warn">HTTPS is off, so this page isn't encrypted. Only use it on your home network, or turn on HTTPS in Sphynx's settings.</div>{{end}}
  <form method="post" autocomplete="off">
    <label for="password">Admin password</label>
    <input id="password" name="password" type="password" required autocomplete="current-password" autofocus>
    <label for="user">Device belongs to</label>
    <input id="user" name="user" value="{{.User}}" placeholder="{{.Owner}}" autocapitalize="none" spellcheck="false">
    <div class="hint">Leave empty to add it for {{if .Owner}}{{.Owner}}{{else}}the owner{{end}}.</div>
    {{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
    <button type="submit">Approve device</button>
  </form>
  <p class="small">Not expecting this? Close the page and the device stays out. You can also approve it in the Sphynx admin page under Add device.</p>
  {{end}}
</main>
</body>
</html>
`))
