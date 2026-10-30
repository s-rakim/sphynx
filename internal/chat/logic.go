package chat

import (
	"crypto/subtle"
	"encoding/json"
	"os"
	"strings"
)

const (
	maxPayload       = 512 * 1024 // an encrypted message envelope, thumbnails included
	maxUnusedPhotos  = 20
	maxGroup         = 1024
	defaultAbout     = "Hey there! I am using Sphynx Chat."
	maxCommunityKids = 100
)

// unusedFile matches files no message, status, profile or group uses (SQL,
// on the files table).
const unusedFile = `NOT EXISTS (SELECT 1 FROM file_links l WHERE l.file_id = files.id)
	AND NOT EXISTS (SELECT 1 FROM users u WHERE u.avatar = files.id)
	AND NOT EXISTS (SELECT 1 FROM chats c WHERE c.avatar = files.id)`

// ---- accounts ----

// upsertUser creates the account of a device the first time it opens the
// chat, and keeps its tailnet details current.
func (s *Server) upsertUser(id *Identity) *userRow {
	s.sawDevice(*id)
	name := truncate(strings.TrimSpace(id.Name), 60)
	if name == "" {
		name = id.ID
	}
	u := s.getUser(id.ID)
	if u != nil {
		if id.Avatar != "" && (u.TailscaleAvatar == nil || *u.TailscaleAvatar != id.Avatar) {
			s.db.run("UPDATE users SET tailscale_avatar = ? WHERE id = ?", id.Avatar, id.ID)
			u.TailscaleAvatar = &id.Avatar
		}
		rename := !u.CustomName && u.Name != name
		if rename || u.DeviceName != name || u.Owner != id.Owner || u.OS != id.OS || u.NodeID != id.NodeID {
			if rename {
				u.Name = name
			}
			u.DeviceName, u.Owner, u.OS, u.NodeID = name, id.Owner, id.OS, id.NodeID
			s.db.run("UPDATE users SET name = ?, device_name = ?, owner = ?, os = ?, node_id = ? WHERE id = ?",
				u.Name, u.DeviceName, u.Owner, u.OS, u.NodeID, u.ID)
			s.broadcastUser(u)
		}
		return u
	}
	var avatar *string
	if id.Avatar != "" {
		avatar = &id.Avatar
	}
	s.db.run(`INSERT INTO users (id, name, device_name, about, tailscale_avatar, owner, os, node_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id.ID, name, name, defaultAbout, avatar, id.Owner, id.OS, id.NodeID, now())
	u = s.getUser(id.ID)
	s.broadcastUser(u)
	return u
}

// ensureUser makes sure a device that has never opened the chat has an
// account, so others can start a chat with it. Only devices listed in the
// tailnet directory qualify.
func (s *Server) ensureUser(userID string) bool {
	if s.userExists(userID) {
		return true
	}
	d, ok := s.directoryEntry(userID)
	if !ok {
		return false
	}
	s.upsertUser(&d)
	return true
}

func (s *Server) broadcastUser(u *userRow) {
	for _, uid := range s.allUserIDs() {
		s.hub.sendToUser(uid, map[string]any{"type": "user", "user": s.userView(u, uid)}, sendOpts{})
	}
}

func (s *Server) pushUserTo(subjectID, viewerID string) {
	if u := s.getUser(subjectID); u != nil {
		s.hub.sendToUser(viewerID, map[string]any{"type": "user", "user": s.userView(u, viewerID)}, sendOpts{})
	}
}

func (s *Server) authenticateToken(token any, id *Identity) *deviceRow {
	t, ok := token.(string)
	if !ok {
		fail(401, "Missing device token")
	}
	deviceID, secret, _ := strings.Cut(t, ".")
	var d deviceRow
	found := deviceID != "" && s.db.one(scanDevice(&d), "SELECT "+deviceCols+" FROM devices WHERE id = ?", deviceID)
	if !found || secret == "" {
		fail(401, "Unknown device")
	}
	if subtle.ConstantTimeCompare([]byte(d.TokenHash), []byte(hashToken(secret))) != 1 {
		fail(401, "Unknown device")
	}
	if d.RevokedAt != nil {
		fail(401, "This browser was logged out")
	}
	if d.UserID != id.ID {
		fail(403, "This browser is signed in as another device on your network")
	}
	s.db.run("UPDATE devices SET last_active = ? WHERE id = ?", now(), d.ID)
	return &d
}

// notifyDeviceChange tells everyone who shares a chat with this account
// that it has a new key set to encrypt for.
func (s *Server) notifyDeviceChange(userID string) {
	peers := s.db.strings(`SELECT DISTINCT m2.user_id FROM members m1 JOIN members m2 ON m1.chat_id = m2.chat_id
		JOIN chats c ON c.id = m1.chat_id
		WHERE m1.user_id = ? AND m2.left_at IS NULL AND c.type != 'channel'`, userID)
	s.hub.sendToUsers(append([]string{userID}, peers...), map[string]any{"type": "devices", "userId": userID}, sendOpts{})
	// Joined may have changed for this account.
	if u := s.getUser(userID); u != nil {
		s.broadcastUser(u)
	}
}

// ---- chats ----

// broadcastChat sends each member their own view of the chat (unread
// counts and receipts differ per viewer).
func (s *Server) broadcastChat(chatID string, extra ...string) {
	chat := s.getChat(chatID)
	if chat == nil {
		return
	}
	for _, uid := range uniq(append(s.everMemberIDs(chatID), extra...)) {
		if !s.db.exists("SELECT 1 FROM members WHERE chat_id = ? AND user_id = ?", chatID, uid) {
			continue
		}
		s.hub.sendToUser(uid, map[string]any{"type": "chat", "chat": s.chatView(chat, uid)}, sendOpts{})
	}
}

func (s *Server) broadcastMessage(m *messageRow, kind string, opts sendOpts) {
	for _, uid := range s.everMemberIDs(m.ChatID) {
		if s.canSeeMessage(m, uid) {
			s.hub.sendToUser(uid, map[string]any{"type": kind, "message": s.messageView(m, uid)}, opts)
		}
	}
}

type newMessage struct {
	id           string
	senderID     string
	senderDevice *string
	kind         string
	payload      *string
	files        []string
	viewOnce     bool
}

func (s *Server) insertMessage(chat *chatRow, nm newMessage) *messageRow {
	if nm.files == nil {
		nm.files = []string{}
	}
	var out *messageRow
	s.db.inTx(func() {
		var seq int64
		s.db.scalar(&seq, "SELECT last_seq FROM chats WHERE id = ?", chat.ID)
		seq++
		s.db.run("UPDATE chats SET last_seq = ? WHERE id = ?", seq, chat.ID)
		created := now()
		var expires *int64
		if chat.Disappearing > 0 && nm.kind == "msg" {
			expires = ptr(created + chat.Disappearing*1000)
		}
		s.db.run(`INSERT INTO messages (id, chat_id, seq, sender_id, sender_device, kind, payload, files, view_once, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nm.id, chat.ID, seq, nm.senderID, nm.senderDevice, nm.kind, nm.payload, mustJSON(nm.files), nm.viewOnce, created, expires)
		for _, f := range nm.files {
			s.db.run("INSERT INTO file_links (file_id, message_id) VALUES (?, ?)", f, nm.id)
		}
		if nm.kind == "msg" {
			// A sender has read everything up to their own message.
			s.db.run("UPDATE members SET read_seq = ?, delivered_seq = ?, marked_unread = 0 WHERE chat_id = ? AND user_id = ?",
				seq, seq, chat.ID, nm.senderID)
		}
		s.db.run("UPDATE members SET removed = 0 WHERE chat_id = ?", chat.ID)
		out = s.getMessage(nm.id)
	})
	return out
}

func (s *Server) systemMessage(chat *chatRow, actor string, event map[string]any) *messageRow {
	payload := mustJSON(event)
	m := s.insertMessage(chat, newMessage{id: newID(16), senderID: actor, kind: "system", payload: &payload})
	s.broadcastMessage(m, "message", sendOpts{})
	return m
}

func (s *Server) createGroup(creator, name, description string, avatar *string, others []string, typ string, communityID *string) *chatRow {
	if len(others)+1 > maxGroup {
		failf(400, "Groups are limited to %d members", maxGroup)
	}
	chatID := newID(16)
	ap := adminPosts(typ)
	// Everyone added at creation sees the "created" message, as on WhatsApp.
	s.db.inTx(func() {
		s.db.run(`INSERT INTO chats (id, type, name, description, avatar, created_by, created_at, admins_only_send, admins_only_edit, community_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, chatID, typ, name, description, avatar, creator, now(), ap, ap, communityID)
		s.db.run("INSERT INTO members (chat_id, user_id, role, joined_at) VALUES (?, ?, 'admin', ?)", chatID, creator, now())
		for _, uid := range others {
			s.db.run("INSERT INTO members (chat_id, user_id, role, joined_at) VALUES (?, ?, 'member', ?)", chatID, uid, now())
		}
	})
	chat := s.getChat(chatID)
	s.systemMessage(chat, creator, map[string]any{"event": "created", "name": name})
	if len(others) > 0 {
		s.systemMessage(chat, creator, map[string]any{"event": "added", "users": others})
	}
	if communityID != nil {
		community := s.getChat(*communityID)
		s.addMembers(community, creator, append([]string{creator}, others...), true, "")
		s.systemMessage(chat, creator, map[string]any{"event": "community", "name": community.name()})
		s.systemMessage(community, creator, map[string]any{"event": "community-group", "name": name})
		s.broadcastChat(*communityID)
	}
	s.broadcastChat(chatID)
	return s.getChat(chatID)
}

// readNewChat reads the name, description, photo and first members of a
// new group, community or channel.
func (s *Server) readNewChat(b body, me string) (string, string, *string, []string) {
	name := reqStr(b["name"], "name", 1, 100)
	desc := ""
	if d := vStr(b["description"], "description", 0, 2048, true); d != nil {
		desc = *d
	}
	var raw []any
	if v, ok := b["members"]; ok && v != nil {
		list, ok := v.([]any)
		if !ok {
			fail(400, "members must be a list of user ids")
		}
		raw = list
	}
	var others []string
	seen := map[string]bool{}
	for _, x := range raw {
		uid, ok := x.(string)
		if ok && uid == me {
			continue
		}
		if !ok || !s.ensureUser(uid) {
			fail(400, "Unknown member")
		}
		s.noRadio(uid)
		if !seen[uid] {
			seen[uid] = true
			others = append(others, uid)
		}
	}
	var avatar *string
	if a, ok := b["avatar"].(string); ok && a != "" {
		var id string
		if s.db.scalar(&id, "SELECT id FROM files WHERE id = ? AND public = 1", a) {
			avatar = &id
		}
	}
	return name, desc, avatar, others
}

func (s *Server) addMembers(chat *chatRow, actor string, userIDs []string, quiet bool, via string) []string {
	var added []string
	// Channel followers read the whole history (posts aren't end-to-end
	// encrypted, so there is nothing they couldn't decrypt).
	history := chat.Type == "channel"
	s.db.inTx(func() {
		for _, uid := range userIDs {
			if !s.ensureUser(uid) {
				failf(400, "Unknown user %s", uid)
			}
			s.noRadio(uid)
			existing := s.getMember(chat.ID, uid)
			if existing != nil && existing.active() {
				continue
			}
			var seq int64
			s.db.scalar(&seq, "SELECT last_seq FROM chats WHERE id = ?", chat.ID)
			floor := seq
			if history {
				floor = 0
			}
			if existing != nil {
				s.db.run(`UPDATE members SET left_at = NULL, left_seq = NULL, role = 'member', joined_at = ?, joined_seq = ?,
					cleared_seq = ?, read_seq = ?, delivered_seq = ?, removed = 0 WHERE chat_id = ? AND user_id = ?`,
					now(), floor, floor, seq, seq, chat.ID, uid)
			} else {
				s.db.run(`INSERT INTO members (chat_id, user_id, role, joined_at, joined_seq, read_seq, delivered_seq)
					VALUES (?, ?, 'member', ?, ?, ?, ?)`, chat.ID, uid, now(), floor, seq, seq)
			}
			added = append(added, uid)
		}
		if s.db.count("SELECT COUNT(*) FROM members WHERE chat_id = ? AND left_at IS NULL", chat.ID) > maxGroup {
			failf(400, "Groups are limited to %d members", maxGroup)
		}
	})
	if len(added) > 0 && chat.Type != "channel" && !quiet {
		event := "added"
		if len(added) == 1 && added[0] == actor {
			event = "joined"
		}
		ev := map[string]any{"event": event, "users": added}
		if via != "" {
			ev["via"] = via
		}
		s.systemMessage(chat, actor, ev)
	}
	// Everyone in a community's group is a member of the community.
	if len(added) > 0 && chat.CommunityID != nil {
		if community := s.getChat(*chat.CommunityID); community != nil {
			if len(s.addMembers(community, actor, added, true, "")) > 0 {
				s.broadcastChat(community.ID)
			}
		}
	}
	return added
}

func (s *Server) removeMember(chat *chatRow, actor, uid string) {
	var seq int64
	s.db.scalar(&seq, "SELECT last_seq FROM chats WHERE id = ?", chat.ID)
	s.db.run("UPDATE members SET left_at = ?, left_seq = ?, role = 'member' WHERE chat_id = ? AND user_id = ?", now(), seq, chat.ID, uid)
	if chat.Type != "channel" {
		event := "removed"
		if actor == uid {
			event = "left"
		}
		s.systemMessage(chat, actor, map[string]any{"event": event, "users": []string{uid}})
	}
	// Leaving a community means leaving its groups too.
	if chat.Type == "community" {
		for _, g := range s.chats(`SELECT `+prefixed("c.", chatCols)+` FROM chats c JOIN members m ON m.chat_id = c.id
			WHERE c.community_id = ? AND m.user_id = ? AND m.left_at IS NULL`, chat.ID, uid) {
			s.removeMember(g, actor, uid)
		}
	}
	// Keep at least one admin, like WhatsApp: promote the longest-standing member.
	if chat.Type != "channel" && !s.db.exists("SELECT 1 FROM members WHERE chat_id = ? AND left_at IS NULL AND role = 'admin'", chat.ID) {
		var next string
		if s.db.scalar(&next, "SELECT user_id FROM members WHERE chat_id = ? AND left_at IS NULL ORDER BY joined_at LIMIT 1", chat.ID) {
			s.db.run("UPDATE members SET role = 'admin' WHERE chat_id = ? AND user_id = ?", chat.ID, next)
		}
	}
	s.broadcastChat(chat.ID, uid)
}

// prefixed qualifies every column in a column list with a table alias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ", ")
	for i, p := range parts {
		parts[i] = alias + p
	}
	return strings.Join(parts, ", ")
}

// ---- files ----

func (s *Server) getFile(id string) *fileRow {
	var f fileRow
	if !s.db.one(scanFile(&f), "SELECT "+fileCols+" FROM files WHERE id = ?", id) {
		return nil
	}
	return &f
}

func (s *Server) canReadFile(fileID, userID string) *fileRow {
	f := s.getFile(fileID)
	if f == nil {
		return nil
	}
	if f.Owner == userID || f.Public {
		return f
	}
	type link struct{ message, status *string }
	var links []link
	s.db.all(func(r scanner) error {
		var l link
		if err := r.Scan(&l.message, &l.status); err != nil {
			return err
		}
		links = append(links, l)
		return nil
	}, "SELECT message_id, status_id FROM file_links WHERE file_id = ?", fileID)
	for _, l := range links {
		if l.message != nil {
			m := s.getMessage(*l.message)
			if m == nil || m.DeletedAt != nil || !s.canSeeMessage(m, userID) {
				continue
			}
			if m.ViewOnce && m.SenderID != userID &&
				s.db.exists("SELECT 1 FROM view_once_opened WHERE message_id = ? AND user_id = ?", m.ID, userID) {
				continue
			}
			return f
		}
		if l.status != nil {
			var owner string
			if s.db.scalar(&owner, "SELECT user_id FROM statuses WHERE id = ? AND expires_at > ?", *l.status, now()) &&
				!s.eitherBlocked(owner, userID) {
				return f
			}
		}
	}
	return nil
}

func (s *Server) checkFiles(v any, userID string) []string {
	if v == nil {
		return []string{}
	}
	raw, ok := v.([]any)
	if !ok || len(raw) > 32 {
		fail(400, "files must be a list of up to 32 file ids")
	}
	var ids []string
	for _, x := range raw {
		id, ok := x.(string)
		if !ok || s.canReadFile(id, userID) == nil {
			fail(400, "Unknown file")
		}
		ids = append(ids, id)
	}
	return uniq(ids)
}

func (s *Server) deleteOrphanFiles(ids []string, photos bool) {
	for _, id := range ids {
		var public bool
		if !s.db.scalar(&public, "SELECT public FROM files WHERE id = ? AND "+unusedFile, id) {
			continue
		}
		if public && !photos {
			continue
		}
		s.db.run("DELETE FROM files WHERE id = ?", id)
		_ = os.Remove(s.filePath(id))
	}
}

// checkUploadQuota: uploads wait on the server until they're sent; cap how
// much one device can leave waiting, so nobody can fill the disk.
func (s *Server) checkUploadQuota(userID string, length int64, public bool) {
	if public {
		if s.db.count("SELECT COUNT(*) FROM files WHERE owner = ? AND public = 1 AND "+unusedFile, userID) >= maxUnusedPhotos {
			fail(429, "Too many photos uploaded without being used. Try again tomorrow.")
		}
		return
	}
	waiting := s.db.count("SELECT COALESCE(SUM(size), 0) FROM files WHERE owner = ? AND public = 0 AND "+unusedFile, userID)
	if waiting+s.uploading[userID]+length > 2*s.maxUploadBytes() {
		fail(429, "Too many files waiting to be sent. Send or cancel some first.")
	}
}

func (s *Server) wipeMessage(m *messageRow) {
	files := jsonList(m.Files)
	s.db.run("DELETE FROM file_links WHERE message_id = ?", m.ID)
	s.db.run("DELETE FROM reactions WHERE message_id = ?", m.ID)
	s.db.run("DELETE FROM poll_votes WHERE message_id = ?", m.ID)
	s.deleteOrphanFiles(files, false)
}

// ---- periodic cleanup ----

func (s *Server) sweep() {
	t := now()
	for _, m := range s.messages("SELECT "+messageCols+" FROM messages WHERE expires_at IS NOT NULL AND expires_at <= ?", t) {
		s.wipeMessage(m)
		for _, table := range []string{"stars", "hidden", "view_once_opened"} {
			s.db.run("DELETE FROM "+table+" WHERE message_id = ?", m.ID)
		}
		s.db.run("DELETE FROM messages WHERE id = ?", m.ID)
		s.hub.sendToUsers(s.everMemberIDs(m.ChatID), map[string]any{"type": "message-gone", "chatId": m.ChatID, "id": m.ID}, sendOpts{})
	}
	for _, st := range s.statuses("SELECT "+statusCols+" FROM statuses WHERE expires_at <= ?", t) {
		s.deleteStatus(st)
	}
	// Uploads that never made it into a message or status, and photos
	// nobody uses as a profile or group photo any more.
	stale := s.db.strings("SELECT id FROM files WHERE "+unusedFile+" AND created_at < ?", t-24*3600*1000)
	s.deleteOrphanFiles(stale, true)
}

func (s *Server) deleteStatus(st *statusRow) {
	s.db.run("DELETE FROM file_links WHERE status_id = ?", st.ID)
	s.db.run("DELETE FROM status_views WHERE status_id = ?", st.ID)
	s.db.run("DELETE FROM statuses WHERE id = ?", st.ID)
	s.deleteOrphanFiles(jsonList(st.Files), false)
	s.hub.sendToUsers(s.allUserIDs(), map[string]any{"type": "status-gone", "id": st.ID}, sendOpts{})
}

func checkPayload(chat *chatRow, v any) string {
	p, ok := v.(string)
	if !ok || len(p) > maxPayload {
		fail(400, "payload must be an encrypted envelope string")
	}
	if chat.Type == "channel" {
		var post map[string]any
		if err := json.Unmarshal([]byte(p), &post); err != nil || post == nil {
			fail(400, "A channel post must be a JSON object with a type")
		}
		_, typed := post["type"].(string)
		_, sealed := post["ct"]
		if !typed || sealed {
			fail(400, "A channel post must be a JSON object with a type")
		}
	}
	return p
}

func (s *Server) requireMessage(id, userID string) *messageRow {
	m := s.getMessage(id)
	if m == nil || !s.canSeeMessage(m, userID) {
		fail(404, "No such message")
	}
	return m
}

func validMessageID(id string) bool {
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
