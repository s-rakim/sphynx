package chat

import (
	"encoding/json"
	"unicode/utf8"
)

// JSON views of the rows, shaped exactly as PROTOCOL.md and the web app
// expect (null where Tailchat sends None).

const big = int64(1<<53 - 1)

// Chats with admins, a name and members: groups, community announcement
// groups and channels.
func groupish(t string) bool { return t == "group" || t == "community" || t == "channel" }

// In these only admins post, and everyone else reads.
func adminPosts(t string) bool { return t == "community" || t == "channel" }

func avatarURL(id *string) *string {
	if id == nil {
		return nil
	}
	u := "/api/avatars/" + *id
	return &u
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func ptr[T any](v T) *T { return &v }

func (s *Server) getUser(id string) *userRow {
	var u userRow
	if !s.db.one(scanUser(&u), "SELECT "+userCols+" FROM users WHERE id = ?", id) {
		return nil
	}
	return &u
}

func (s *Server) userExists(id string) bool {
	return s.db.exists("SELECT 1 FROM users WHERE id = ?", id)
}

func (s *Server) allUserIDs() []string { return s.db.strings("SELECT id FROM users") }

func (s *Server) isBlocked(by, whom string) bool {
	return s.db.exists("SELECT 1 FROM blocks WHERE user_id = ? AND blocked_id = ?", by, whom)
}

func (s *Server) eitherBlocked(a, b string) bool { return s.isBlocked(a, b) || s.isBlocked(b, a) }

type userView struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	About        string  `json:"about"`
	Avatar       *string `json:"avatar"`
	Online       *bool   `json:"online"`
	LastSeen     *int64  `json:"lastSeen"`
	HideLastSeen *bool   `json:"hideLastSeen,omitempty"`
	ReadReceipts *bool   `json:"readReceipts,omitempty"`
	Blocked      *bool   `json:"blocked,omitempty"`
	// Sphynx: the device's name on the tailnet (Name can be one it picked
	// itself), who it belongs to and what it runs.
	Device string `json:"device"`
	Owner  string `json:"owner"`
	OS     string `json:"os"`
	// Joined: the device has a browser signed in to the chat, so messages
	// can be encrypted for it.
	Joined bool `json:"joined"`
	// OnNetwork: the device is still part of the tailnet.
	OnNetwork bool `json:"onNetwork"`
}

func (s *Server) userView(u *userRow, viewer string) userView {
	hidden := false
	if viewer != u.ID {
		var viewerHides bool
		if viewer != "" {
			s.db.scalar(&viewerHides, "SELECT hide_last_seen FROM users WHERE id = ?", viewer)
		}
		// Like WhatsApp: hiding your own last seen also hides everyone else's from you.
		hidden = u.HideLastSeen || viewerHides || (viewer != "" && s.eitherBlocked(u.ID, viewer))
	}
	v := userView{
		ID:        u.ID,
		Name:      u.Name,
		About:     u.About,
		Avatar:    avatarURL(u.Avatar),
		Device:    u.DeviceName,
		Owner:     u.Owner,
		OS:        u.OS,
		Joined:    s.db.exists("SELECT 1 FROM devices WHERE user_id = ? AND revoked_at IS NULL", u.ID),
		OnNetwork: s.onNetwork(u.ID),
	}
	if v.Avatar == nil && u.TailscaleAvatar != nil {
		v.Avatar = u.TailscaleAvatar
	}
	if !hidden {
		v.Online = ptr(s.hub.isOnline(u.ID))
		v.LastSeen = u.LastSeen
	}
	if viewer == u.ID {
		v.HideLastSeen = ptr(u.HideLastSeen)
		v.ReadReceipts = ptr(u.ReadReceipts)
	} else if viewer != "" {
		v.Blocked = ptr(s.isBlocked(viewer, u.ID))
	}
	return v
}

// directoryView is a device on the tailnet that has never opened the chat.
func directoryView(d Identity) userView {
	return userView{
		ID:        d.ID,
		Name:      d.Name,
		Device:    d.Name,
		About:     "",
		Owner:     d.Owner,
		OS:        d.OS,
		Online:    ptr(false),
		OnNetwork: true,
	}
}

type deviceView struct {
	ID         string          `json:"id"`
	UserID     string          `json:"userId"`
	Name       string          `json:"name"`
	PublicKey  json.RawMessage `json:"publicKey"`
	CreatedAt  int64           `json:"createdAt"`
	LastActive *int64          `json:"lastActive"`
	RevokedAt  *int64          `json:"revokedAt"`
}

func devView(d *deviceRow) deviceView {
	return deviceView{
		ID: d.ID, UserID: d.UserID, Name: d.Name, PublicKey: json.RawMessage(d.PublicKey),
		CreatedAt: d.CreatedAt, LastActive: d.LastActive, RevokedAt: d.RevokedAt,
	}
}

func (s *Server) devices(query string, args ...any) []deviceView {
	out := []deviceView{}
	var d deviceRow
	s.db.all(func(r scanner) error {
		if err := scanDevice(&d)(r); err != nil {
			return err
		}
		out = append(out, devView(&d))
		return nil
	}, query, args...)
	return out
}

// ---- chats ----

func (s *Server) getChat(id string) *chatRow {
	var c chatRow
	if !s.db.one(scanChat(&c), "SELECT "+chatCols+" FROM chats WHERE id = ?", id) {
		return nil
	}
	return &c
}

func (s *Server) chats(query string, args ...any) []*chatRow {
	var out []*chatRow
	s.db.all(func(r scanner) error {
		var c chatRow
		if err := scanChat(&c)(r); err != nil {
			return err
		}
		out = append(out, &c)
		return nil
	}, query, args...)
	return out
}

func (s *Server) getMember(chatID, userID string) *memberRow {
	var m memberRow
	if !s.db.one(scanMember(&m), "SELECT "+memberCols+" FROM members WHERE chat_id = ? AND user_id = ?", chatID, userID) {
		return nil
	}
	return &m
}

func (s *Server) members(query string, args ...any) []*memberRow {
	var out []*memberRow
	s.db.all(func(r scanner) error {
		var m memberRow
		if err := scanMember(&m)(r); err != nil {
			return err
		}
		out = append(out, &m)
		return nil
	}, query, args...)
	return out
}

func (s *Server) activeMember(chatID, userID string) *memberRow {
	m := s.getMember(chatID, userID)
	if m == nil || !m.active() {
		return nil
	}
	return m
}

func (s *Server) requireMember(chatID, userID string, allowFormer bool) (*chatRow, *memberRow) {
	chat := s.getChat(chatID)
	if chat == nil {
		fail(404, "No such chat")
	}
	m := s.getMember(chatID, userID)
	if m == nil || (!m.active() && !allowFormer) {
		fail(403, "You are not in this chat")
	}
	return chat, m
}

func (s *Server) requireAdmin(chatID, userID string) (*chatRow, *memberRow) {
	chat, m := s.requireMember(chatID, userID, false)
	if !groupish(chat.Type) {
		fail(400, "Only groups have admins")
	}
	if m.Role != "admin" {
		fail(403, "Only group admins can do that")
	}
	return chat, m
}

func (s *Server) activeMemberIDs(chatID string) []string {
	return s.db.strings("SELECT user_id FROM members WHERE chat_id = ? AND left_at IS NULL", chatID)
}

func (s *Server) everMemberIDs(chatID string) []string {
	return s.db.strings("SELECT user_id FROM members WHERE chat_id = ?", chatID)
}

// visibleFloor is the lowest seq a member may see; former members keep
// what they saw before leaving.
func visibleFloor(m *memberRow) int64 { return max(m.JoinedSeq, m.ClearedSeq) }

func ceilingOf(m *memberRow) int64 {
	if m.LeftSeq != nil {
		return *m.LeftSeq
	}
	return big
}

type memberView struct {
	UserID       string `json:"userId"`
	Role         string `json:"role"`
	JoinedAt     int64  `json:"joinedAt"`
	JoinedSeq    int64  `json:"joinedSeq"`
	Left         bool   `json:"left"`
	DeliveredSeq int64  `json:"deliveredSeq"`
	ReadSeq      int64  `json:"readSeq"`
}

func (s *Server) memberView(m *memberRow, chat *chatRow, viewer string) memberView {
	readSeq := m.ReadSeq
	// Turning read receipts off works both ways, as on WhatsApp, except in groups.
	if chat.Type == "direct" && m.UserID != viewer {
		var theirs, mine bool
		s.db.scalar(&theirs, "SELECT read_receipts FROM users WHERE id = ?", m.UserID)
		s.db.scalar(&mine, "SELECT read_receipts FROM users WHERE id = ?", viewer)
		if !theirs || !mine {
			readSeq = 0
		}
	}
	return memberView{
		UserID: m.UserID, Role: m.Role, JoinedAt: m.JoinedAt, JoinedSeq: m.JoinedSeq,
		Left: !m.active(), DeliveredSeq: m.DeliveredSeq, ReadSeq: readSeq,
	}
}

type meView struct {
	Role         string `json:"role"`
	Left         bool   `json:"left"`
	JoinedSeq    int64  `json:"joinedSeq"`
	ClearedSeq   int64  `json:"clearedSeq"`
	LeftSeq      *int64 `json:"leftSeq"`
	ReadSeq      int64  `json:"readSeq"`
	PinnedAt     *int64 `json:"pinnedAt"`
	Archived     bool   `json:"archived"`
	MutedUntil   int64  `json:"mutedUntil"`
	MarkedUnread bool   `json:"markedUnread"`
}

type groupSummary struct {
	ID          string  `json:"id"`
	Name        *string `json:"name"`
	Description string  `json:"description"`
	Avatar      *string `json:"avatar"`
	Members     int64   `json:"members"`
	Joined      bool    `json:"joined"`
}

type chatView struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Name           *string         `json:"name"`
	Description    string          `json:"description"`
	Avatar         *string         `json:"avatar"`
	CreatedBy      string          `json:"createdBy"`
	CreatedAt      int64           `json:"createdAt"`
	Disappearing   int64           `json:"disappearing"`
	AdminsOnlySend bool            `json:"adminsOnlySend"`
	AdminsOnlyEdit bool            `json:"adminsOnlyEdit"`
	InviteCode     *string         `json:"inviteCode"`
	PinnedMessage  *string         `json:"pinnedMessage"`
	LastSeq        int64           `json:"lastSeq"`
	CommunityID    *string         `json:"communityId"`
	CommunityName  *string         `json:"communityName"`
	Followers      *int            `json:"followers"`
	Groups         *[]groupSummary `json:"groups"`
	Members        []memberView    `json:"members"`
	Me             meView          `json:"me"`
	Unread         int64           `json:"unread"`
	LastMessage    *messageView    `json:"lastMessage"`
}

func (s *Server) chatView(chat *chatRow, viewer string) chatView {
	me := s.getMember(chat.ID, viewer)
	if me == nil {
		fail(403, "You are not in this chat")
	}
	members := s.members("SELECT "+memberCols+" FROM members WHERE chat_id = ? ORDER BY joined_at", chat.ID)
	var followers *int
	if chat.Type == "channel" {
		n := 0
		for _, m := range members {
			if m.active() {
				n++
			}
		}
		followers = &n
		// Followers don't see each other, only the channel's admins.
		if me.Role != "admin" {
			var keep []*memberRow
			for _, m := range members {
				if m.Role == "admin" || m.UserID == viewer {
					keep = append(keep, m)
				}
			}
			members = keep
		}
	}
	var communityID, communityName *string
	if chat.CommunityID != nil {
		if c := s.getChat(*chat.CommunityID); c != nil {
			communityID, communityName = &c.ID, c.Name
		}
	}
	floor := visibleFloor(me)
	ceiling := ceilingOf(me)
	var last *messageRow
	var lm messageRow
	if s.db.one(scanMessage(&lm), `SELECT `+messageCols+` FROM messages WHERE chat_id = ? AND seq > ? AND seq <= ?
		  AND id NOT IN (SELECT message_id FROM hidden WHERE user_id = ?)
		ORDER BY seq DESC LIMIT 1`, chat.ID, floor, ceiling, viewer) {
		last = &lm
	}
	unread := s.db.count(`SELECT COUNT(*) FROM messages WHERE chat_id = ? AND kind = 'msg' AND sender_id != ?
		  AND deleted_at IS NULL AND seq > ? AND seq <= ?`, chat.ID, viewer, max(floor, me.ReadSeq), ceiling)
	active := me.active()
	v := chatView{
		ID:             chat.ID,
		Type:           chat.Type,
		Name:           chat.Name,
		Description:    chat.Description,
		Avatar:         avatarURL(chat.Avatar),
		CreatedBy:      chat.CreatedBy,
		CreatedAt:      chat.CreatedAt,
		Disappearing:   chat.Disappearing,
		AdminsOnlySend: chat.AdminsOnlySend,
		AdminsOnlyEdit: chat.AdminsOnlyEdit,
		PinnedMessage:  chat.PinnedMessage,
		LastSeq:        chat.LastSeq,
		CommunityID:    communityID,
		CommunityName:  communityName,
		Followers:      followers,
		Members:        []memberView{},
		Me: meView{
			Role: me.Role, Left: !active, JoinedSeq: me.JoinedSeq, ClearedSeq: me.ClearedSeq, LeftSeq: me.LeftSeq,
			ReadSeq: me.ReadSeq, PinnedAt: me.PinnedAt, Archived: me.Archived, MutedUntil: me.MutedUntil,
			MarkedUnread: me.MarkedUnread,
		},
		Unread: unread,
	}
	if me.Role == "admin" && active {
		v.InviteCode = chat.InviteCode
	}
	if chat.Type == "community" {
		g := s.communityGroups(chat.ID, viewer)
		v.Groups = &g
	}
	for _, m := range members {
		v.Members = append(v.Members, s.memberView(m, chat, viewer))
	}
	if last != nil {
		mv := s.messageView(last, viewer)
		v.LastMessage = &mv
	}
	return v
}

func (s *Server) communityGroups(communityID, viewer string) []groupSummary {
	out := []groupSummary{}
	s.db.all(func(r scanner) error {
		var g groupSummary
		var joined *int64
		if err := r.Scan(&g.ID, &g.Name, &g.Description, &g.Avatar, &g.Members, &joined); err != nil {
			return err
		}
		g.Avatar = avatarURL(g.Avatar)
		g.Joined = joined != nil
		out = append(out, g)
		return nil
	}, `SELECT c.id, c.name, c.description, c.avatar,
		  (SELECT COUNT(*) FROM members m WHERE m.chat_id = c.id AND m.left_at IS NULL),
		  (SELECT 1 FROM members m WHERE m.chat_id = c.id AND m.user_id = ? AND m.left_at IS NULL)
		FROM chats c WHERE c.community_id = ? ORDER BY c.name COLLATE NOCASE`, viewer, communityID)
	return out
}

// ---- messages ----

func (s *Server) getMessage(id string) *messageRow {
	var m messageRow
	if !s.db.one(scanMessage(&m), "SELECT "+messageCols+" FROM messages WHERE id = ?", id) {
		return nil
	}
	return &m
}

func (s *Server) messages(query string, args ...any) []*messageRow {
	var out []*messageRow
	s.db.all(func(r scanner) error {
		var m messageRow
		if err := scanMessage(&m)(r); err != nil {
			return err
		}
		out = append(out, &m)
		return nil
	}, query, args...)
	return out
}

type reactionView struct {
	UserID *string `json:"userId"`
	Emoji  string  `json:"emoji"`
}

type voteView struct {
	UserID  *string         `json:"userId"`
	Options json.RawMessage `json:"options"`
}

type openedView struct {
	UserID string `json:"userId"`
	At     int64  `json:"at"`
}

type messageView struct {
	ID           string          `json:"id"`
	ChatID       string          `json:"chatId"`
	Seq          int64           `json:"seq"`
	SenderID     string          `json:"senderId"`
	SenderDevice *string         `json:"senderDevice"`
	Kind         string          `json:"kind"`
	Payload      *string         `json:"payload"`
	Files        json.RawMessage `json:"files"`
	ViewOnce     bool            `json:"viewOnce"`
	CreatedAt    int64           `json:"createdAt"`
	EditedAt     *int64          `json:"editedAt"`
	DeletedAt    *int64          `json:"deletedAt"`
	ExpiresAt    *int64          `json:"expiresAt"`
	Reactions    []reactionView  `json:"reactions"`
	Votes        []voteView      `json:"votes"`
	Starred      bool            `json:"starred"`
	// Opened: for the sender, who opened a view-once message; for anyone
	// else, whether they have.
	Opened any `json:"opened,omitempty"`
}

func (s *Server) openedBy(messageID string) []openedView {
	out := []openedView{}
	s.db.all(func(r scanner) error {
		var o openedView
		if err := r.Scan(&o.UserID, &o.At); err != nil {
			return err
		}
		out = append(out, o)
		return nil
	}, "SELECT user_id, at FROM view_once_opened WHERE message_id = ?", messageID)
	return out
}

func (s *Server) messageView(m *messageRow, viewer string) messageView {
	opened := m.ViewOnce && s.db.exists("SELECT 1 FROM view_once_opened WHERE message_id = ? AND user_id = ?", m.ID, viewer)
	mine := m.SenderID == viewer
	// Channel followers see reaction and vote counts, not who.
	anonymous := s.db.exists(`SELECT 1 FROM chats c LEFT JOIN members me ON me.chat_id = c.id AND me.user_id = ?
		WHERE c.id = ? AND c.type = 'channel' AND (me.role IS NULL OR me.role != 'admin')`, viewer, m.ChatID)
	who := func(id string) *string {
		if !anonymous || id == viewer {
			return &id
		}
		return nil
	}
	v := messageView{
		ID: m.ID, ChatID: m.ChatID, Seq: m.Seq, SenderID: m.SenderID, SenderDevice: m.SenderDevice, Kind: m.Kind,
		Payload: m.Payload, Files: json.RawMessage(m.Files), ViewOnce: m.ViewOnce, CreatedAt: m.CreatedAt,
		EditedAt: m.EditedAt, DeletedAt: m.DeletedAt, ExpiresAt: m.ExpiresAt,
		Reactions: []reactionView{}, Votes: []voteView{},
		Starred: s.db.exists("SELECT 1 FROM stars WHERE user_id = ? AND message_id = ?", viewer, m.ID),
	}
	// A view-once message is gone for a recipient once they have opened it.
	if m.DeletedAt != nil || (opened && !mine) {
		v.Payload = nil
	}
	if len(v.Files) == 0 {
		v.Files = json.RawMessage("[]")
	}
	s.db.all(func(r scanner) error {
		var uid, emoji string
		if err := r.Scan(&uid, &emoji); err != nil {
			return err
		}
		v.Reactions = append(v.Reactions, reactionView{UserID: who(uid), Emoji: emoji})
		return nil
	}, "SELECT user_id, emoji FROM reactions WHERE message_id = ? ORDER BY at", m.ID)
	s.db.all(func(r scanner) error {
		var uid, options string
		if err := r.Scan(&uid, &options); err != nil {
			return err
		}
		v.Votes = append(v.Votes, voteView{UserID: who(uid), Options: json.RawMessage(options)})
		return nil
	}, "SELECT user_id, options FROM poll_votes WHERE message_id = ?", m.ID)
	if m.ViewOnce {
		if mine {
			v.Opened = s.openedBy(m.ID)
		} else {
			v.Opened = opened
		}
	}
	return v
}

func (s *Server) canSeeMessage(m *messageRow, userID string) bool {
	member := s.getMember(m.ChatID, userID)
	if member == nil {
		return false
	}
	return m.Seq > visibleFloor(member) && (member.LeftSeq == nil || m.Seq <= *member.LeftSeq)
}

// ---- status ----

type statusView struct {
	ID        string          `json:"id"`
	UserID    string          `json:"userId"`
	Payload   string          `json:"payload"`
	Files     json.RawMessage `json:"files"`
	CreatedAt int64           `json:"createdAt"`
	ExpiresAt int64           `json:"expiresAt"`
	Seen      bool            `json:"seen"`
	Viewers   *[]openedView   `json:"viewers,omitempty"`
}

func (s *Server) statusView(st *statusRow, viewer string) statusView {
	v := statusView{
		ID: st.ID, UserID: st.UserID, Payload: st.Payload, Files: json.RawMessage(st.Files),
		CreatedAt: st.CreatedAt, ExpiresAt: st.ExpiresAt,
		Seen: s.db.exists("SELECT 1 FROM status_views WHERE status_id = ? AND viewer_id = ?", st.ID, viewer),
	}
	if st.UserID == viewer {
		viewers := []openedView{}
		s.db.all(func(r scanner) error {
			var o openedView
			if err := r.Scan(&o.UserID, &o.At); err != nil {
				return err
			}
			viewers = append(viewers, o)
			return nil
		}, "SELECT viewer_id, at FROM status_views WHERE status_id = ? ORDER BY at DESC", st.ID)
		v.Viewers = &viewers
	}
	return v
}

func (s *Server) statuses(query string, args ...any) []*statusRow {
	var out []*statusRow
	s.db.all(func(r scanner) error {
		var st statusRow
		if err := scanStatus(&st)(r); err != nil {
			return err
		}
		out = append(out, &st)
		return nil
	}, query, args...)
	return out
}

// jsonList decodes a stored JSON list of strings ("files", "participants").
func jsonList(raw string) []string {
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(dbError{err})
	}
	return string(b)
}
