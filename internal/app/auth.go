package app

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie  = "sphynx_session"
	sessionTTL     = 30 * 24 * time.Hour
	pbkdf2Rounds   = 600_000
	minPasswordLen = 8
)

func hashPassword(password, saltHex string) (string, error) {
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Rounds, 32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

// SetPassword stores a new admin password. Existing sessions stop working.
func (a *App) SetPassword(password string) error {
	if len(password) < minPasswordLen {
		return errors.New("use at least 8 characters")
	}
	salt := randomHex(16)
	h, err := hashPassword(password, salt)
	if err != nil {
		return err
	}
	return a.secrets.Update(func(s *Secrets) error {
		s.PasswordSalt, s.PasswordHash = salt, h
		return nil
	})
}

func (a *App) checkPassword(password string) bool {
	s := a.secrets.Get()
	if s.PasswordHash == "" {
		return false
	}
	h, err := hashPassword(password, s.PasswordSalt)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h), []byte(s.PasswordHash)) == 1
}

func (a *App) PasswordSet() bool { return a.secrets.Get().PasswordHash != "" }

// Session tokens are "<expiry>.<mac>", where the MAC also covers the
// password hash so changing the password logs everyone out.
func (a *App) sessionMAC(exp string) string {
	s := a.secrets.Get()
	m := hmac.New(sha256.New, []byte(s.SessionKey))
	m.Write([]byte(exp + "|" + s.PasswordHash))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *App) newSession(w http.ResponseWriter, r *http.Request) {
	exp := strconv.FormatInt(time.Now().Add(sessionTTL).Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    exp + "." + a.sessionMAC(exp),
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (a *App) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

func (a *App) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, mac, ok := strings.Cut(c.Value, ".")
	if !ok || !a.PasswordSet() {
		return false
	}
	t, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > t {
		return false
	}
	return hmac.Equal([]byte(mac), []byte(a.sessionMAC(exp)))
}

// loginLimiter slows down password guessing per client address.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	limiterWindow = 15 * time.Minute
	limiterMax    = 10
)

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// allowed reports whether key (a client address) has fewer than max
// failures in the window. max <= 0 means the default.
func (l *loginLimiter) allowed(key string, max int) bool {
	if max <= 0 {
		max = limiterMax
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-limiterWindow)
	var keep []time.Time
	for _, t := range l.failures[key] {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.failures, key)
	} else {
		l.failures[key] = keep
	}
	return len(keep) < max
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	l.failures[ip] = append(l.failures[ip], time.Now())
	l.mu.Unlock()
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.failures, ip)
	l.mu.Unlock()
}
