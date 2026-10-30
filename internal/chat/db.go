package chat

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// SQLite storage. The server only ever sees encrypted message bodies: what it
// stores in clear is routing metadata (who is in which chat, when a message
// was sent, receipts, reactions and poll vote indices).
//
// In Sphynx every chat account ("user") is one device on the tailnet, so
// each device is its own contact with its own chats. The browsers and apps
// that open the chat on a device are that account's "devices" and hold the
// encryption keys.
const schema = `
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,               -- chat id of a tailnet device, see DeviceUserID
  name TEXT NOT NULL,
  custom_name INTEGER NOT NULL DEFAULT 0,    -- 1 once the device picked its own chat name
  device_name TEXT NOT NULL DEFAULT '',      -- the device's name on the tailnet
  about TEXT NOT NULL DEFAULT 'Hey there! I am using Sphynx Chat.',
  avatar TEXT,                       -- file id of an uploaded (unencrypted) profile photo
  tailscale_avatar TEXT,             -- profile picture URL reported by the tailnet
  owner TEXT NOT NULL DEFAULT '',    -- the person the device belongs to
  os TEXT NOT NULL DEFAULT '',
  node_id TEXT NOT NULL DEFAULT '',  -- Headscale node id
  created_at INTEGER NOT NULL,
  last_seen INTEGER,
  hide_last_seen INTEGER NOT NULL DEFAULT 0,
  read_receipts INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id),
  name TEXT NOT NULL,
  public_key TEXT NOT NULL,          -- ECDH P-256 public key, JWK JSON
  token_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_active INTEGER,
  revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS devices_user ON devices(user_id);

CREATE TABLE IF NOT EXISTS chats (
  id TEXT PRIMARY KEY,
  -- community: a community's announcements group; channel: one-way posts to followers
  type TEXT NOT NULL CHECK (type IN ('direct', 'group', 'community', 'channel')),
  name TEXT,
  description TEXT NOT NULL DEFAULT '',
  avatar TEXT,
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  direct_key TEXT UNIQUE,            -- "a|b" for a direct chat, so there is only one per pair
  disappearing INTEGER NOT NULL DEFAULT 0,   -- seconds, 0 = off
  invite_code TEXT UNIQUE,
  admins_only_send INTEGER NOT NULL DEFAULT 0,
  admins_only_edit INTEGER NOT NULL DEFAULT 0,
  pinned_message TEXT,
  last_seq INTEGER NOT NULL DEFAULT 0,
  community_id TEXT                  -- a group that belongs to a community
);

CREATE TABLE IF NOT EXISTS members (
  chat_id TEXT NOT NULL REFERENCES chats(id),
  user_id TEXT NOT NULL REFERENCES users(id),
  role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
  joined_at INTEGER NOT NULL,
  joined_seq INTEGER NOT NULL DEFAULT 0,     -- messages up to here predate this member
  left_at INTEGER,
  left_seq INTEGER,                          -- last message a former member may still read
  delivered_seq INTEGER NOT NULL DEFAULT 0,
  read_seq INTEGER NOT NULL DEFAULT 0,
  cleared_seq INTEGER NOT NULL DEFAULT 0,    -- "clear chat" hides everything up to here
  pinned_at INTEGER,
  archived INTEGER NOT NULL DEFAULT 0,
  muted_until INTEGER NOT NULL DEFAULT 0,
  marked_unread INTEGER NOT NULL DEFAULT 0,
  removed INTEGER NOT NULL DEFAULT 0,        -- "delete chat": hidden until the next message
  PRIMARY KEY (chat_id, user_id)
);
CREATE INDEX IF NOT EXISTS members_user ON members(user_id);

CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY,
  chat_id TEXT NOT NULL REFERENCES chats(id),
  seq INTEGER NOT NULL,
  sender_id TEXT NOT NULL,
  sender_device TEXT,
  kind TEXT NOT NULL CHECK (kind IN ('msg', 'system')),
  payload TEXT,                      -- encrypted envelope (msg) or event JSON (system)
  files TEXT NOT NULL DEFAULT '[]',
  view_once INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  edited_at INTEGER,
  deleted_at INTEGER,
  expires_at INTEGER,
  UNIQUE (chat_id, seq)
);
CREATE INDEX IF NOT EXISTS messages_expiry ON messages(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS hidden (user_id TEXT NOT NULL, message_id TEXT NOT NULL, PRIMARY KEY (user_id, message_id));
CREATE TABLE IF NOT EXISTS stars (user_id TEXT NOT NULL, message_id TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY (user_id, message_id));
CREATE TABLE IF NOT EXISTS reactions (
  message_id TEXT NOT NULL, user_id TEXT NOT NULL, emoji TEXT NOT NULL, at INTEGER NOT NULL,
  PRIMARY KEY (message_id, user_id)
);
CREATE TABLE IF NOT EXISTS poll_votes (
  message_id TEXT NOT NULL, user_id TEXT NOT NULL, options TEXT NOT NULL, at INTEGER NOT NULL,
  PRIMARY KEY (message_id, user_id)
);
CREATE TABLE IF NOT EXISTS view_once_opened (
  message_id TEXT NOT NULL, user_id TEXT NOT NULL, at INTEGER NOT NULL,
  PRIMARY KEY (message_id, user_id)
);

CREATE TABLE IF NOT EXISTS files (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  size INTEGER NOT NULL,
  public INTEGER NOT NULL DEFAULT 0, -- profile and group photos, readable by anyone on the tailnet
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS file_links (
  file_id TEXT NOT NULL,
  message_id TEXT,
  status_id TEXT
);
CREATE INDEX IF NOT EXISTS file_links_file ON file_links(file_id);
CREATE INDEX IF NOT EXISTS files_owner ON files(owner);
CREATE INDEX IF NOT EXISTS users_avatar ON users(avatar) WHERE avatar IS NOT NULL;
CREATE INDEX IF NOT EXISTS chats_avatar ON chats(avatar) WHERE avatar IS NOT NULL;

CREATE TABLE IF NOT EXISTS statuses (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  payload TEXT NOT NULL,
  files TEXT NOT NULL DEFAULT '[]',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS status_views (
  status_id TEXT NOT NULL, viewer_id TEXT NOT NULL, at INTEGER NOT NULL,
  PRIMARY KEY (status_id, viewer_id)
);

CREATE TABLE IF NOT EXISTS blocks (user_id TEXT NOT NULL, blocked_id TEXT NOT NULL, PRIMARY KEY (user_id, blocked_id));

-- Radio contacts' bridge "browsers" (see radio.go): Sphynx keeps their
-- private keys to pass messages between the chat and the radio.
CREATE TABLE IF NOT EXISTS radio_keys (
  user_id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL,
  private_key TEXT NOT NULL          -- P-256 private key, base64url
);

CREATE TABLE IF NOT EXISTS calls (
  id TEXT PRIMARY KEY,
  chat_id TEXT NOT NULL,
  caller_id TEXT NOT NULL,
  video INTEGER NOT NULL,
  started_at INTEGER NOT NULL,
  answered_at INTEGER,
  ended_at INTEGER,
  participants TEXT NOT NULL DEFAULT '[]'
);
`

// store wraps the database. The API runs one request at a time (Server.mu),
// so a single connection is enough and transactions can span helper calls.
type store struct {
	db    *sql.DB
	tx    *sql.Tx
	depth int
}

// dbError is a failed query. Handlers don't check every query: like any
// other unexpected error it ends the request with a 500 (see Server.guard).
type dbError struct{ err error }

func (e dbError) Error() string { return "database: " + e.err.Error() }

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dir, "chat.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create chat database: %w", err)
	}
	return &store{db: db}, nil
}

func (s *store) close() error { return s.db.Close() }

type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func (s *store) q() querier {
	if s.tx != nil {
		return s.tx
	}
	return s.db
}

// run executes a statement and returns the number of rows it changed.
func (s *store) run(query string, args ...any) int64 {
	res, err := s.q().Exec(query, args...)
	if err != nil {
		panic(dbError{err})
	}
	n, _ := res.RowsAffected()
	return n
}

// exists reports whether a query returns at least one row.
func (s *store) exists(query string, args ...any) bool {
	var one any
	err := s.q().QueryRow(query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		panic(dbError{err})
	}
	return true
}

// scalar scans a single-column, single-row result into dest. It returns
// false when there is no row.
func (s *store) scalar(dest any, query string, args ...any) bool {
	err := s.q().QueryRow(query, args...).Scan(dest)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		panic(dbError{err})
	}
	return true
}

func (s *store) count(query string, args ...any) int64 {
	var n int64
	s.scalar(&n, query, args...)
	return n
}

func (s *store) strings(query string, args ...any) []string {
	rows, err := s.q().Query(query, args...)
	if err != nil {
		panic(dbError{err})
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			panic(dbError{err})
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		panic(dbError{err})
	}
	return out
}

// scanner is *sql.Row or *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

// all runs a query and calls fn for every row.
func (s *store) all(fn func(scanner) error, query string, args ...any) {
	rows, err := s.q().Query(query, args...)
	if err != nil {
		panic(dbError{err})
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			panic(dbError{err})
		}
	}
	if err := rows.Err(); err != nil {
		panic(dbError{err})
	}
}

// one runs a query and scans its first row with fn; false if there is none.
func (s *store) one(fn func(scanner) error, query string, args ...any) bool {
	err := fn(s.q().QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		panic(dbError{err})
	}
	return true
}

// inTx runs fn in a transaction. Nested calls join the outer transaction.
// A panic (a failed check or query) rolls everything back.
func (s *store) inTx(fn func()) {
	if s.tx != nil {
		s.depth++
		defer func() { s.depth-- }()
		fn()
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		panic(dbError{err})
	}
	s.tx, s.depth = tx, 1
	done := false
	defer func() {
		s.tx, s.depth = nil, 0
		if !done {
			_ = tx.Rollback()
		}
	}()
	fn()
	if err := tx.Commit(); err != nil {
		panic(dbError{err})
	}
	done = true
}

// ---- rows ----

type userRow struct {
	ID              string
	Name            string
	CustomName      bool
	DeviceName      string
	About           string
	Avatar          *string
	TailscaleAvatar *string
	Owner           string
	OS              string
	NodeID          string
	CreatedAt       int64
	LastSeen        *int64
	HideLastSeen    bool
	ReadReceipts    bool
}

const userCols = "id, name, custom_name, device_name, about, avatar, tailscale_avatar, owner, os, node_id, created_at, last_seen, hide_last_seen, read_receipts"

func scanUser(u *userRow) func(scanner) error {
	return func(r scanner) error {
		return r.Scan(&u.ID, &u.Name, &u.CustomName, &u.DeviceName, &u.About, &u.Avatar, &u.TailscaleAvatar, &u.Owner, &u.OS,
			&u.NodeID, &u.CreatedAt, &u.LastSeen, &u.HideLastSeen, &u.ReadReceipts)
	}
}

type deviceRow struct {
	ID         string
	UserID     string
	Name       string
	PublicKey  string
	TokenHash  string
	CreatedAt  int64
	LastActive *int64
	RevokedAt  *int64
}

const deviceCols = "id, user_id, name, public_key, token_hash, created_at, last_active, revoked_at"

func scanDevice(d *deviceRow) func(scanner) error {
	return func(r scanner) error {
		return r.Scan(&d.ID, &d.UserID, &d.Name, &d.PublicKey, &d.TokenHash, &d.CreatedAt, &d.LastActive, &d.RevokedAt)
	}
}

type chatRow struct {
	ID             string
	Type           string
	Name           *string
	Description    string
	Avatar         *string
	CreatedBy      string
	CreatedAt      int64
	DirectKey      *string
	Disappearing   int64
	InviteCode     *string
	AdminsOnlySend bool
	AdminsOnlyEdit bool
	PinnedMessage  *string
	LastSeq        int64
	CommunityID    *string
}

const chatCols = "id, type, name, description, avatar, created_by, created_at, direct_key, disappearing, invite_code, admins_only_send, admins_only_edit, pinned_message, last_seq, community_id"

func scanChat(c *chatRow) func(scanner) error {
	return func(r scanner) error {
		return r.Scan(&c.ID, &c.Type, &c.Name, &c.Description, &c.Avatar, &c.CreatedBy, &c.CreatedAt, &c.DirectKey,
			&c.Disappearing, &c.InviteCode, &c.AdminsOnlySend, &c.AdminsOnlyEdit, &c.PinnedMessage, &c.LastSeq, &c.CommunityID)
	}
}

func (c *chatRow) name() string {
	if c.Name == nil {
		return ""
	}
	return *c.Name
}

type memberRow struct {
	ChatID       string
	UserID       string
	Role         string
	JoinedAt     int64
	JoinedSeq    int64
	LeftAt       *int64
	LeftSeq      *int64
	DeliveredSeq int64
	ReadSeq      int64
	ClearedSeq   int64
	PinnedAt     *int64
	Archived     bool
	MutedUntil   int64
	MarkedUnread bool
	Removed      bool
}

const memberCols = "chat_id, user_id, role, joined_at, joined_seq, left_at, left_seq, delivered_seq, read_seq, cleared_seq, pinned_at, archived, muted_until, marked_unread, removed"

func scanMember(m *memberRow) func(scanner) error {
	return func(r scanner) error {
		return r.Scan(&m.ChatID, &m.UserID, &m.Role, &m.JoinedAt, &m.JoinedSeq, &m.LeftAt, &m.LeftSeq, &m.DeliveredSeq,
			&m.ReadSeq, &m.ClearedSeq, &m.PinnedAt, &m.Archived, &m.MutedUntil, &m.MarkedUnread, &m.Removed)
	}
}

func (m *memberRow) active() bool { return m.LeftAt == nil }

type messageRow struct {
	ID           string
	ChatID       string
	Seq          int64
	SenderID     string
	SenderDevice *string
	Kind         string
	Payload      *string
	Files        string
	ViewOnce     bool
	CreatedAt    int64
	EditedAt     *int64
	DeletedAt    *int64
	ExpiresAt    *int64
}

const messageCols = "id, chat_id, seq, sender_id, sender_device, kind, payload, files, view_once, created_at, edited_at, deleted_at, expires_at"

func scanMessage(m *messageRow) func(scanner) error {
	return func(r scanner) error {
		return r.Scan(&m.ID, &m.ChatID, &m.Seq, &m.SenderID, &m.SenderDevice, &m.Kind, &m.Payload, &m.Files,
			&m.ViewOnce, &m.CreatedAt, &m.EditedAt, &m.DeletedAt, &m.ExpiresAt)
	}
}

type fileRow struct {
	ID        string
	Owner     string
	Size      int64
	Public    bool
	CreatedAt int64
}

const fileCols = "id, owner, size, public, created_at"

func scanFile(f *fileRow) func(scanner) error {
	return func(r scanner) error {
		return r.Scan(&f.ID, &f.Owner, &f.Size, &f.Public, &f.CreatedAt)
	}
}

type statusRow struct {
	ID        string
	UserID    string
	Payload   string
	Files     string
	CreatedAt int64
	ExpiresAt int64
}

const statusCols = "id, user_id, payload, files, created_at, expires_at"

func scanStatus(s *statusRow) func(scanner) error {
	return func(r scanner) error {
		return r.Scan(&s.ID, &s.UserID, &s.Payload, &s.Files, &s.CreatedAt, &s.ExpiresAt)
	}
}

type callRow struct {
	ID           string
	ChatID       string
	CallerID     string
	Video        bool
	StartedAt    int64
	AnsweredAt   *int64
	EndedAt      *int64
	Participants string
}

const callCols = "id, chat_id, caller_id, video, started_at, answered_at, ended_at, participants"

func scanCall(c *callRow) func(scanner) error {
	return func(r scanner) error {
		return r.Scan(&c.ID, &c.ChatID, &c.CallerID, &c.Video, &c.StartedAt, &c.AnsweredAt, &c.EndedAt, &c.Participants)
	}
}
