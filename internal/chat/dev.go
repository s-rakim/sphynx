package chat

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// DevResolver is for local testing only: the device comes from the
// chat_dev_user cookie (set at /dev/login) or an X-Dev-User header, and
// only loopback connections are accepted, because anyone could pick any
// identity. Devices, if set, describes the made-up tailnet's devices.
type DevResolver struct {
	Devices DevDirectory
}

// DevDirectory is a made-up list of tailnet devices for dev mode, from a
// spec like "phone:Android:Ana,laptop:Windows:Ben" (name:os:owner).
type DevDirectory []Identity

func ParseDevDirectory(spec string) DevDirectory {
	var d DevDirectory
	for _, item := range strings.Split(spec, ",") {
		parts := strings.SplitN(strings.TrimSpace(item), ":", 3)
		name := strings.ToLower(parts[0])
		if !devUser.MatchString(name) {
			continue
		}
		id := devIdentity(name)
		if len(parts) > 1 {
			id.OS = parts[1]
		}
		if len(parts) > 2 {
			id.Owner = parts[2]
		}
		d = append(d, id)
	}
	return d
}

func (d DevDirectory) Devices(context.Context) ([]Identity, error) { return d, nil }

func devIdentity(user string) Identity {
	return Identity{ID: user + "@dev", Name: strings.ToUpper(user[:1]) + user[1:], OS: "dev"}
}

var devUser = regexp.MustCompile(`^[a-z0-9._-]{1,32}$`)

func (DevResolver) Mode() string { return "dev" }

func (DevResolver) AllowedHost(host string) bool {
	return host == "localhost" || IsLoopback(host)
}

func (d DevResolver) Identify(r *http.Request) (*Identity, error) {
	if !IsLoopback(r.RemoteAddr) {
		return nil, &Error{Status: http.StatusForbidden, Message: "Dev mode only accepts loopback connections."}
	}
	user := ""
	if c, err := r.Cookie("chat_dev_user"); err == nil {
		user = c.Value
	}
	if user == "" {
		user = r.Header.Get("X-Dev-User")
	}
	if !devUser.MatchString(user) {
		return nil, &Error{Status: http.StatusUnauthorized, Message: "Dev mode: pick a user at /dev/login"}
	}
	id := devIdentity(user)
	for _, dev := range d.Devices {
		if dev.ID == id.ID {
			id = dev
		}
	}
	return &id, nil
}

const devLoginPage = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Sphynx Chat dev login</title><body style="font-family:system-ui;max-width:420px;margin:15vh auto;padding:0 16px">
<h1>Sphynx Chat (dev mode)</h1><p>In Sphynx your identity comes from the tailnet device you use. Pick a test device:</p>
<form><input name="user" placeholder="laptop" pattern="[a-z0-9._-]{1,32}" required autofocus> <button>Sign in</button></form>`

func devLogin(w http.ResponseWriter, r *http.Request) {
	user := strings.ToLower(r.URL.Query().Get("user"))
	if devUser.MatchString(user) {
		http.SetCookie(w, &http.Cookie{Name: "chat_dev_user", Value: user, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
	_, _ = w.Write([]byte(devLoginPage))
}
