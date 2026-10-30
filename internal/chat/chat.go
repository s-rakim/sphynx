// Package chat is Sphynx Chat: a WhatsApp-style messenger that only devices
// on the tailnet can reach. It is a Go port of Tailchat
// (github.com/s-rakim/private-rea), changed so that every device on the
// tailnet is its own contact with its own chats, instead of one account per
// person.
//
// Messages, files, status updates and call setup are end-to-end encrypted
// by the browsers (see PROTOCOL.md); the server stores ciphertext and the
// routing metadata it needs. Identity comes from the tailnet: Sphynx asks
// its own tailscaled which device opened each connection, so there are no
// passwords.
package chat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Config holds the server's limits and where it keeps its data.
type Config struct {
	DataDir        string
	MaxUploadBytes int64
	StatusHours    int
	EditWindow     time.Duration
	DeleteWindow   time.Duration
	SweepInterval  time.Duration
	Logf           func(format string, args ...any)
	// Network describes where the chat can be opened, for the app's help
	// texts. Optional.
	Network func() NetworkInfo
}

// NetworkInfo tells the app how people reach the chat.
type NetworkInfo struct {
	// Address is where any device on the tailnet opens the chat.
	Address string `json:"address"`
	// SecureAddress is the HTTPS address, when there is one. Calls, voice
	// notes and notifications need it: browsers only allow them on HTTPS.
	SecureAddress string `json:"secureAddress"`
}

func (s *Server) networkInfo() NetworkInfo {
	if s.cfg.Network == nil {
		return NetworkInfo{}
	}
	return s.cfg.Network()
}

// Defaults fills in zero values with Tailchat's defaults.
func (c Config) withDefaults() Config {
	if c.MaxUploadBytes <= 0 {
		c.MaxUploadBytes = 2048 << 20
	}
	if c.StatusHours <= 0 {
		c.StatusHours = 24
	}
	if c.EditWindow <= 0 {
		c.EditWindow = 15 * time.Minute
	}
	if c.DeleteWindow <= 0 {
		c.DeleteWindow = 48 * time.Hour
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = 15 * time.Second
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	return c
}

// Identity is the tailnet device behind a request.
type Identity struct {
	ID     string // chat account id, see DeviceUserID
	Name   string // the device's name on the tailnet
	Owner  string // the person it belongs to
	OS     string
	NodeID string // Headscale node id
	Avatar string // optional https:// picture
}

// Resolver tells the server which device a request comes from. It returns
// an *Error (status 401, 403, 421 or 503) when it can't.
type Resolver interface {
	Identify(r *http.Request) (*Identity, error)
	// AllowedHost reports whether host (without port) names this server.
	// Anything else is refused, so a web page can't point its own domain
	// at Sphynx's address (DNS rebinding).
	AllowedHost(host string) bool
	// Mode is shown to clients: "tailnet", "local" or "dev".
	Mode() string
}

// Directory lists every device on the tailnet, so the chat can show them
// as contacts before they have opened the chat themselves.
type Directory interface {
	Devices(ctx context.Context) ([]Identity, error)
}

// DeviceUserID is the chat account id of a tailnet device. It covers the
// device's machine key as well as its node id, so a node id that is ever
// handed out again never inherits someone else's chats.
func DeviceUserID(nodeID, machineKey string) string {
	sum := sha256.Sum256([]byte("sphynx-chat/v1|" + nodeID + "|" + machineKey))
	return "d" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))[:15]
}

// Error is an API error with an HTTP status. Handlers raise it with fail;
// the request wrapper turns it into a JSON response. Link and LinkText
// optionally point people somewhere when they open the app in a browser.
type Error struct {
	Status   int
	Message  string
	Link     string
	LinkText string
}

func (e *Error) Error() string { return e.Message }

func fail(status int, msg string) { panic(&Error{Status: status, Message: msg}) }

func failf(status int, format string, args ...any) {
	panic(&Error{Status: status, Message: fmt.Sprintf(format, args...)})
}

func now() int64 { return time.Now().UnixMilli() }

// newID returns n random bytes as unpadded base64url, like Python's
// secrets.token_urlsafe.
func newID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return fmt.Sprintf("%x", sum)
}

// Server is the chat backend. All API work happens under mu, one request
// at a time, the same way Tailchat's single-threaded server behaves.
type Server struct {
	cfg Config
	db  *store
	hub *hub
	dir Directory

	mu        sync.Mutex
	calls     map[string]*call
	uploading map[string]int64 // user id -> bytes being uploaded now
	maxUpload atomic.Int64     // can change while the server runs

	dirMu    sync.Mutex
	dirCache []Identity
	dirAt    time.Time

	ctx    context.Context
	cancel context.CancelFunc
}

// New opens (or creates) the chat database under cfg.DataDir. dir may be nil.
func New(cfg Config, dir Directory) (*Server, error) {
	cfg = cfg.withDefaults()
	db, err := openStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:       cfg,
		db:        db,
		dir:       dir,
		calls:     map[string]*call{},
		uploading: map[string]int64{},
		ctx:       ctx,
		cancel:    cancel,
	}
	s.hub = newHub(s)
	s.maxUpload.Store(cfg.MaxUploadBytes)
	logf = cfg.Logf
	return s, nil
}

// SetMaxUploadBytes changes the largest file people may send.
func (s *Server) SetMaxUploadBytes(n int64) {
	if n > 0 {
		s.maxUpload.Store(n)
	}
}

func (s *Server) maxUploadBytes() int64 { return s.maxUpload.Load() }

// Disconnect closes every open connection, e.g. when the chat is turned
// off. Browsers reconnect by themselves once it is back on.
func (s *Server) Disconnect() {
	s.hub.disconnectAll(websocket.StatusGoingAway, "Sphynx Chat was turned off")
}

// Run sweeps expired messages, statuses and unused uploads until ctx ends.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.refreshDirectory(ctx, 30*time.Second)
			s.locked(func() { s.sweep() })
		}
	}
}

// Close disconnects everyone and closes the database.
func (s *Server) Close() error {
	s.cancel()
	s.hub.closeAll()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.close()
}

// Backup writes a consistent copy of the chat database to w. Attachments
// (files/) are left out: they can be large, and they are ciphertext that
// only the devices' browsers can read anyway.
func (s *Server) Backup(w io.Writer) error {
	tmp := filepath.Join(s.cfg.DataDir, "backup-"+newID(8)+".db")
	defer os.Remove(tmp)
	s.mu.Lock()
	_, err := s.db.db.Exec("VACUUM INTO ?", tmp)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// locked runs fn under the server lock, logging (not propagating) panics:
// it is for background work, where there is no request to fail.
func (s *Server) locked(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if v := recover(); v != nil {
			s.cfg.Logf("chat: %v", v)
		}
	}()
	fn()
}

func (s *Server) filePath(id string) string {
	return filepath.Join(s.cfg.DataDir, "files", id)
}

// guard turns a panic from fail (or a failed query) into an error.
func guard(fn func()) (err error) {
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		switch e := v.(type) {
		case *Error:
			err = e
		case dbError:
			err = e
		case error:
			if errors.Is(e, http.ErrAbortHandler) {
				panic(e)
			}
			err = fmt.Errorf("%w", e)
		default:
			err = fmt.Errorf("%v", e)
		}
	}()
	fn()
	return nil
}
