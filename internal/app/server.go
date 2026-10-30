package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/s-rakim/sphynx/internal/hs"
	"github.com/s-rakim/sphynx/web"
)

func (a *App) serveAdmin(ctx context.Context) error {
	srv := &http.Server{
		Addr:              a.cfg.AdminAddr,
		Handler:           a.adminHandler(),
		ReadHeaderTimeout: 30 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	a.Logf("admin UI listening on %s", a.cfg.AdminAddr)
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type apiFunc func(w http.ResponseWriter, r *http.Request) (any, error)

// httpError carries a status code for the UI.
type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(msg string) error { return &httpError{http.StatusBadRequest, msg} }
func notFound(msg string) error   { return &httpError{http.StatusNotFound, msg} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid request: " + err.Error())
	}
	return nil
}

func (a *App) wrap(fn apiFunc, auth bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if auth && !a.authenticated(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "please sign in"})
			return
		}
		// Every state-changing request must come from the UI's own scripts;
		// combined with SameSite cookies this blocks cross-site requests.
		if r.Method != http.MethodGet && r.Header.Get("X-Sphynx") != "1" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing X-Sphynx header"})
			return
		}
		v, err := fn(w, r)
		if err != nil {
			code := http.StatusInternalServerError
			msg := err.Error()
			var he *httpError
			var ae *hs.APIError
			switch {
			case errors.As(err, &he):
				code = he.code
			case errors.As(err, &ae):
				code = http.StatusBadRequest
				if ae.Status == http.StatusNotFound {
					code = http.StatusNotFound
				}
				msg = ae.Message
			case errors.Is(err, hs.ErrUnauthorized):
				code = http.StatusServiceUnavailable
				msg = "Sphynx is reconnecting to Headscale, try again in a moment"
			}
			writeJSON(w, code, map[string]string{"error": msg})
			return
		}
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func (a *App) adminHandler() http.Handler {
	mux := http.NewServeMux()
	pub := func(p string, fn apiFunc) { mux.HandleFunc(p, a.wrap(fn, false)) }
	api := func(p string, fn apiFunc) { mux.HandleFunc(p, a.wrap(fn, true)) }

	pub("GET /api/session", a.apiSession)
	pub("POST /api/setup/password", a.apiSetupPassword)
	pub("POST /api/login", a.apiLogin)
	pub("POST /api/logout", a.apiLogout)

	api("POST /api/setup/finish", a.apiSetupFinish)
	api("POST /api/password", a.apiChangePassword)
	api("GET /api/overview", a.apiOverview)

	api("GET /api/nodes", a.apiNodes)
	api("GET /api/nodes/{id}", a.apiNode)
	api("POST /api/nodes/{id}/rename", a.apiNodeRename)
	api("POST /api/nodes/{id}/tags", a.apiNodeTags)
	api("POST /api/nodes/{id}/routes", a.apiNodeRoutes)
	api("POST /api/nodes/{id}/expire", a.apiNodeExpire)
	api("POST /api/nodes/{id}/options", a.apiNodeOptions)
	api("DELETE /api/nodes/{id}", a.apiNodeDelete)
	api("POST /api/register", a.apiRegister)

	api("GET /api/users", a.apiUsers)
	api("POST /api/users", a.apiUserCreate)
	api("POST /api/users/{id}/rename", a.apiUserRename)
	api("DELETE /api/users/{id}", a.apiUserDelete)

	api("GET /api/keys", a.apiKeys)
	api("POST /api/keys", a.apiKeyCreate)
	api("POST /api/keys/{id}/expire", a.apiKeyExpire)
	api("DELETE /api/keys/{id}", a.apiKeyDelete)
	api("GET /api/apikeys", a.apiAPIKeys)
	api("POST /api/apikeys", a.apiAPIKeyCreate)
	api("POST /api/apikeys/{prefix}/expire", a.apiAPIKeyExpire)
	api("DELETE /api/apikeys/{prefix}", a.apiAPIKeyDelete)

	api("GET /api/policy", a.apiPolicy)
	api("PUT /api/policy", a.apiPolicySave)
	api("POST /api/policy/check", a.apiPolicyCheck)

	api("GET /api/dns", a.apiDNS)
	api("PUT /api/dns", a.apiDNSSave)

	api("GET /api/settings", a.apiSettings)
	api("PUT /api/settings", a.apiSettingsSave)
	api("POST /api/headscale/restart", a.apiHeadscaleRestart)

	api("GET /api/wol", a.apiWoL)
	api("POST /api/wol", a.apiWoLSave)
	api("DELETE /api/wol/{id}", a.apiWoLDelete)
	api("POST /api/wol/{id}/wake", a.apiWoLWake)

	api("GET /api/shares", a.apiShares)
	api("POST /api/shares", a.apiShareSave)
	api("PUT /api/shares/settings", a.apiShareSettings)
	api("DELETE /api/shares/{id}", a.apiShareDelete)

	api("GET /api/chat", a.apiChat)
	api("PUT /api/chat", a.apiChatSave)

	api("GET /api/radio", a.apiRadio)
	api("PUT /api/radio", a.apiRadioSave)
	api("POST /api/radio/code", a.apiRadioCode)
	api("DELETE /api/radio/code", a.apiRadioCodeDelete)
	api("POST /api/radio/test", a.apiRadioTest)

	api("GET /api/link", a.apiLink)
	api("PUT /api/link", a.apiLinkSave)
	api("POST /api/link/rotate", a.apiLinkRotate)
	api("DELETE /api/link/inbox/{name}", a.apiInboxDelete)
	mux.HandleFunc("GET /api/link/inbox/{name}", a.serveInboxFile)

	api("GET /api/logs", a.apiLogs)
	mux.HandleFunc("GET /api/backup", func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			http.Error(w, "please sign in", http.StatusUnauthorized)
			return
		}
		a.serveBackup(w, r)
	})

	static, _ := fs.Sub(web.Files, ".")
	files := http.FileServerFS(static)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown API endpoint"})
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		if r.URL.Path != "/" && !strings.Contains(r.URL.Path, ".") {
			r.URL.Path = "/"
		}
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
	return mux
}

// Session and setup

func (a *App) apiSession(w http.ResponseWriter, r *http.Request) (any, error) {
	ready, why := a.Ready()
	s := a.settings.Get()
	return map[string]any{
		"authenticated": a.authenticated(r),
		"passwordSet":   a.PasswordSet(),
		"setupDone":     s.SetupDone,
		"version":       a.cfg.Version,
		"ready":         ready,
		"readyError":    why,
	}, nil
}

func (a *App) apiSetupPassword(w http.ResponseWriter, r *http.Request) (any, error) {
	if a.PasswordSet() {
		return nil, &httpError{http.StatusConflict, "a password is already set; sign in instead"}
	}
	var req struct{ Password string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := a.SetPassword(req.Password); err != nil {
		return nil, badRequest(err.Error())
	}
	a.newSession(w, r)
	a.Logf("admin password created")
	return nil, nil
}

func (a *App) apiLogin(w http.ResponseWriter, r *http.Request) (any, error) {
	ip := clientIP(r)
	if !a.limiter.allowed(ip, 0) {
		return nil, &httpError{http.StatusTooManyRequests, "too many attempts, wait 15 minutes"}
	}
	var req struct{ Password string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if !a.checkPassword(req.Password) {
		a.limiter.fail(ip)
		time.Sleep(500 * time.Millisecond)
		return nil, &httpError{http.StatusUnauthorized, "wrong password"}
	}
	a.limiter.reset(ip)
	a.newSession(w, r)
	return nil, nil
}

func (a *App) apiLogout(w http.ResponseWriter, r *http.Request) (any, error) {
	a.clearSession(w)
	return nil, nil
}

func (a *App) apiChangePassword(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ Current, New string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if !a.checkPassword(req.Current) {
		return nil, badRequest("current password is wrong")
	}
	if err := a.SetPassword(req.New); err != nil {
		return nil, badRequest(err.Error())
	}
	a.newSession(w, r)
	return nil, nil
}

// apiSetupFinish completes the first-run wizard: server address and the
// first user, who becomes the owner of Sphynx home.
func (a *App) apiSetupFinish(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		ServerURL string `json:"serverUrl"`
		UserName  string `json:"userName"`
		Email     string `json:"email"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(req.UserName))
	if err := validUserName(name); err != nil {
		return nil, err
	}
	s := a.settings.Get()
	s.ServerURL = strings.TrimRight(strings.TrimSpace(req.ServerURL), "/")
	if err := s.Validate(); err != nil {
		return nil, badRequest(err.Error())
	}
	if ok, why := a.Ready(); !ok {
		return nil, &httpError{http.StatusServiceUnavailable, "Headscale is not ready yet: " + why}
	}
	ctx := r.Context()
	users, err := a.hs.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	exists := false
	for _, u := range users {
		if u.Name == name {
			exists = true
		}
	}
	if !exists {
		if _, err := a.hs.CreateUser(ctx, name, req.UserName, strings.TrimSpace(req.Email)); err != nil {
			return nil, err
		}
	}
	old := a.settings.Get()
	if err := a.settings.Update(func(cur *Settings) error {
		cur.ServerURL = s.ServerURL
		cur.Owner = name
		cur.SetupDone = true
		return nil
	}); err != nil {
		return nil, err
	}
	a.Logf("setup finished: server %s, owner %s", s.ServerURL, name)
	if old.ServerURL != s.ServerURL {
		if err := a.RestartHeadscale(); err != nil {
			return nil, err
		}
	} else {
		go a.helpers.reconcile(a.ctx)
	}
	return nil, nil
}

func validUserName(name string) error {
	if name == "" {
		return badRequest("enter a name")
	}
	if len(name) > 63 {
		return badRequest("names can be at most 63 characters")
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') {
			return badRequest("use lowercase letters, numbers, dots, dashes or underscores")
		}
	}
	if strings.HasSuffix(name, "@") || name == "tagged-devices" {
		return badRequest("that name is reserved")
	}
	return nil
}
