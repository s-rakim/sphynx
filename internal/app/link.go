package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/s-rakim/sphynx/internal/proc"
)

// LinkSettings configure Sphynx Link: a Tailcat server run by Sphynx home.
// Tailcat (github.com/tailscale/tailcat) uses Tailscale's data plane
// (WireGuard, NAT traversal, DERP) without any control server, so a
// computer that only has the small tailcat program can reach home with a
// secret address, and no port forwarding is needed on either side.
type LinkSettings struct {
	Enabled bool `json:"enabled"`
	// ExitNode lets link clients use this PC's internet and home network
	// (through `tailcat socks`).
	ExitNode bool `json:"exitNode"`
	// Inbox accepts files sent with `tailcat cp file <address>:`.
	Inbox bool `json:"inbox"`
	// Admin serves the Sphynx admin page's port, for
	// `tailcat forward --open-browser <address> 0:<port>`.
	Admin bool `json:"admin"`
	// Allow restricts the link to these client keys. Empty means anyone
	// who knows the address.
	Allow []LinkClient `json:"allow"`
	// DERPMapURL overrides the relays used to set up connections
	// (default: Tailcat's free relays).
	DERPMapURL string `json:"derpMapUrl"`
}

type LinkClient struct {
	Name string `json:"name"`
	Key  string `json:"key"` // nodekey:<hex>
}

var nodeKeyRe = regexp.MustCompile(`^nodekey:[0-9a-f]{64}$`)

func (l LinkSettings) validate() error {
	for _, c := range l.Allow {
		if !nodeKeyRe.MatchString(c.Key) {
			return fmt.Errorf("%q is not a tailcat client key (it looks like nodekey: followed by 64 hex characters)", c.Key)
		}
	}
	if l.DERPMapURL != "" && !strings.HasPrefix(l.DERPMapURL, "https://") {
		return errors.New("the relay map URL must start with https://")
	}
	return nil
}

// spec is the list of services passed to `tailcat serve` (the inbox is
// added with --files). Served ports go to the same port on localhost.
func (l LinkSettings) spec(adminPort string) []string {
	var s []string
	if l.ExitNode {
		s = append(s, "exit-node")
	}
	if l.Admin {
		s = append(s, adminPort)
	}
	return s
}

type link struct {
	a *App

	mu     sync.Mutex
	proc   *proc.Process
	cancel context.CancelFunc
	err    string
}

func (p Paths) TailcatDir() string { return filepath.Join(p.DataDir, "tailcat") }
func (p Paths) InboxDir() string   { return filepath.Join(p.DataDir, "inbox") }
func (p Paths) linkKey() string    { return filepath.Join(p.TailcatDir(), "link.private.json") }

func (l *link) env() []string {
	// Tailcat keeps its relay map cache under $HOME.
	return []string{"HOME=" + l.a.cfg.Paths.TailcatDir()}
}

// apply (re)starts or stops the Tailcat server to match the settings.
func (l *link) apply(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.cancel()
		old := l.proc
		l.cancel, l.proc = nil, nil
		for i := 0; i < 50 && old.Status().Running; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	l.err = ""
	s := l.a.settings.Get().Link
	if !s.Enabled {
		return
	}
	if !l.a.linkAvailable() {
		l.err = "the tailcat program isn't in this Sphynx image"
		return
	}
	adminPort := l.a.adminPort()
	if len(s.spec(adminPort)) == 0 && !s.Inbox {
		l.err = "turn on at least one Link service"
		return
	}
	p := l.a.cfg.Paths
	_ = os.MkdirAll(p.TailcatDir(), 0o700)
	if s.Inbox {
		_ = os.MkdirAll(p.InboxDir(), 0o750)
	}
	if _, err := os.Stat(p.linkKey()); errors.Is(err, os.ErrNotExist) {
		if err := l.genKey(ctx, s); err != nil {
			l.err = err.Error()
			l.a.Logf("Sphynx Link: %v", err)
			return
		}
	}
	pr := proc.New("tailcat", func() []string {
		args := []string{p.TailcatBin, "--json", "--key=" + p.linkKey()}
		if s.DERPMapURL != "" {
			args = append(args, "--derpmap-url="+s.DERPMapURL)
		}
		args = append(args, "serve")
		if len(s.Allow) > 0 {
			var keys []string
			for _, c := range s.Allow {
				keys = append(keys, c.Key)
			}
			args = append(args, "--allow="+strings.Join(keys, ","))
		}
		if s.Inbox {
			// A flat, write-only drop box: senders can't list or read files.
			args = append(args, "--files="+p.InboxDir()+":wo")
		}
		if spec := s.spec(adminPort); len(spec) > 0 {
			args = append(args, strings.Join(spec, ","))
		}
		return args
	}, l.env())
	cctx, cancel := context.WithCancel(ctx)
	l.proc, l.cancel = pr, cancel
	go pr.Run(cctx)
	l.a.Logf("Sphynx Link starting")
}

func (l *link) genKey(ctx context.Context, s LinkSettings) error {
	p := l.a.cfg.Paths
	try := func(extra ...string) error {
		args := []string{}
		if s.DERPMapURL != "" {
			args = append(args, "--derpmap-url="+s.DERPMapURL)
		}
		args = append(args, "genkey", "--key="+p.linkKey())
		args = append(args, extra...)
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, p.TailcatBin, args...)
		cmd.Env = append(os.Environ(), l.env()...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("tailcat genkey: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	// A fixed relay region keeps the address the same across restarts.
	if err := try("--fixed-region"); err != nil {
		l.a.Logf("Sphynx Link: couldn't pin a relay region (%v), using automatic selection", err)
		return try()
	}
	return nil
}

// address returns the current Tailcat address from the server's output.
func (l *link) address() string {
	l.mu.Lock()
	pr := l.proc
	l.mu.Unlock()
	if pr == nil {
		return ""
	}
	lines := pr.Logs.Lines(0)
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		j := strings.Index(line, "{")
		if j < 0 || !strings.Contains(line, "listenAddr") {
			continue
		}
		var v struct {
			ListenAddr string `json:"listenAddr"`
		}
		if json.Unmarshal([]byte(line[j:]), &v) == nil && v.ListenAddr != "" {
			return v.ListenAddr
		}
	}
	return ""
}

func (l *link) status() (running bool, errMsg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.proc != nil {
		st := l.proc.Status()
		running = st.Running
		if st.Crashes > 0 && st.LastError != "" {
			errMsg = "tailcat stopped (" + st.LastError + ")"
			if why := lastComplaint(l.proc.Logs.Lines(20)); why != "" {
				errMsg += ": " + why
			}
		}
	}
	if l.err != "" {
		errMsg = l.err
	}
	return running, errMsg
}

// lastComplaint is tailcat's last own log line, without time stamps,
// which usually says why it stopped.
func lastComplaint(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		f := strings.Fields(lines[i])
		for len(f) > 0 && strings.Trim(f[0], "0123456789:/.") == "" {
			f = f[1:]
		}
		if len(f) > 0 && f[0] != "sphynx:" {
			return strings.Join(f, " ")
		}
	}
	return ""
}

func (l *link) logs(n int) []string {
	l.mu.Lock()
	pr := l.proc
	l.mu.Unlock()
	if pr == nil {
		return []string{}
	}
	if lines := pr.Logs.Lines(n); lines != nil {
		return lines
	}
	return []string{}
}

// rotate throws the key away so a new address is generated. Everyone who
// had the old address loses access.
func (l *link) rotate(ctx context.Context) error {
	if err := os.Remove(l.a.cfg.Paths.linkKey()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l.apply(ctx)
	return nil
}

// API

type inboxFile struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

func (a *App) inboxFiles() []inboxFile {
	out := []inboxFile{}
	ents, err := os.ReadDir(a.cfg.Paths.InboxDir())
	if err != nil {
		return out
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, inboxFile{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	slices.SortFunc(out, func(x, y inboxFile) int { return y.ModTime.Compare(x.ModTime) })
	return out
}

func (a *App) linkAvailable() bool {
	if a.cfg.Paths.TailcatBin == "" {
		return false
	}
	_, err := os.Stat(a.cfg.Paths.TailcatBin)
	return err == nil
}

func (a *App) apiLink(w http.ResponseWriter, r *http.Request) (any, error) {
	running, errMsg := a.link.status()
	s := a.settings.Get().Link
	if s.Allow == nil {
		s.Allow = []LinkClient{}
	}
	return map[string]any{
		"settings":  s,
		"address":   a.link.address(),
		"running":   running,
		"error":     errMsg,
		"inbox":     a.inboxFiles(),
		"available": a.linkAvailable(),
		"logs":      a.link.logs(30),
		"adminPort": a.adminPort(),
	}, nil
}

func (a *App) apiLinkSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req LinkSettings
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	for i := range req.Allow {
		req.Allow[i].Key = strings.ToLower(strings.TrimSpace(req.Allow[i].Key))
		req.Allow[i].Name = strings.TrimSpace(req.Allow[i].Name)
	}
	req.DERPMapURL = strings.TrimSpace(req.DERPMapURL)
	if err := req.validate(); err != nil {
		return nil, badRequest(err.Error())
	}
	if err := a.settings.Update(func(s *Settings) error {
		s.Link = req
		return nil
	}); err != nil {
		return nil, err
	}
	a.link.apply(a.ctx)
	return a.apiLink(w, r)
}

func (a *App) apiLinkRotate(w http.ResponseWriter, r *http.Request) (any, error) {
	if err := a.link.rotate(a.ctx); err != nil {
		return nil, err
	}
	a.Logf("Sphynx Link address rotated")
	return a.apiLink(w, r)
}

func (a *App) inboxPath(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", badRequest("bad file name")
	}
	return filepath.Join(a.cfg.Paths.InboxDir(), name), nil
}

func (a *App) serveInboxFile(w http.ResponseWriter, r *http.Request) {
	if !a.authenticated(r) {
		http.Error(w, "please sign in", http.StatusUnauthorized)
		return
	}
	p, err := a.inboxPath(r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(p)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, p)
}

func (a *App) apiInboxDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	p, err := a.inboxPath(r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	if err := os.Remove(p); err != nil {
		return nil, notFound("file not found")
	}
	return nil, nil
}
