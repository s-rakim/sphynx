package chat

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	routesOnce sync.Once
	apiRoutes  *router
)

// routes is the whole REST API (PROTOCOL.md §7). Handlers run under the
// server lock unless marked as streaming.
func (s *Server) routes() *router {
	routesOnce.Do(func() { apiRoutes = buildRoutes() })
	return apiRoutes
}

func buildRoutes() *router {
	r := &router{}
	add := func(method, pattern string, need deviceNeed, fn func(*Server, *reqCtx) any) *route {
		return r.add(method, pattern, need, func(c *reqCtx) any { return fn(c.server(), c) })
	}

	add("GET", "/api/me", deviceOptional, (*Server).apiMe)
	add("PATCH", "/api/me", deviceRequired, (*Server).apiUpdateMe)
	add("POST", "/api/devices", deviceNone, (*Server).apiRegisterDevice)
	add("GET", "/api/devices", deviceRequired, (*Server).apiListDevices)
	add("DELETE", "/api/devices/:id", deviceRequired, (*Server).apiRevokeDevice)
	add("GET", "/api/keys", deviceRequired, (*Server).apiKeys)
	add("GET", "/api/users", deviceRequired, (*Server).apiUsers).fresh = true
	add("GET", "/api/users/:id/devices", deviceRequired, (*Server).apiUserDevices)
	add("POST", "/api/blocks", deviceRequired, (*Server).apiBlock)
	add("DELETE", "/api/blocks/:id", deviceRequired, (*Server).apiUnblock)
	add("GET", "/api/chats", deviceRequired, (*Server).apiChats)
	add("GET", "/api/chats/:id", deviceRequired, (*Server).apiChat)
	add("POST", "/api/chats/direct", deviceRequired, (*Server).apiDirect).fresh = true
	add("POST", "/api/chats/group", deviceRequired, (*Server).apiGroup).fresh = true
	add("POST", "/api/chats/community", deviceRequired, (*Server).apiCommunity).fresh = true
	add("POST", "/api/chats/channel", deviceRequired, (*Server).apiChannel)
	add("PATCH", "/api/chats/:id", deviceRequired, (*Server).apiUpdateChat)
	add("PATCH", "/api/chats/:id/prefs", deviceRequired, (*Server).apiPrefs)
	add("POST", "/api/chats/:id/clear", deviceRequired, (*Server).apiClear)
	add("DELETE", "/api/chats/:id", deviceRequired, (*Server).apiDeleteChat)
	add("POST", "/api/chats/:id/members", deviceRequired, (*Server).apiAddMembers).fresh = true
	add("DELETE", "/api/chats/:id/members/:userId", deviceRequired, (*Server).apiRemoveMember)
	add("POST", "/api/chats/:id/admins", deviceRequired, (*Server).apiAdmins)
	add("POST", "/api/chats/:id/invite", deviceRequired, (*Server).apiNewInvite)
	add("DELETE", "/api/chats/:id/invite", deviceRequired, (*Server).apiResetInvite)
	add("GET", "/api/invite/:code", deviceRequired, (*Server).apiInvitePreview)
	add("POST", "/api/invite/:code/join", deviceRequired, (*Server).apiInviteJoin)
	add("POST", "/api/communities/:id/groups", deviceRequired, (*Server).apiCommunityAddGroup)
	add("DELETE", "/api/communities/:id/groups/:groupId", deviceRequired, (*Server).apiCommunityRemoveGroup)
	add("POST", "/api/communities/:id/groups/:groupId/join", deviceRequired, (*Server).apiCommunityJoinGroup)
	add("DELETE", "/api/communities/:id", deviceRequired, (*Server).apiDeactivateCommunity)
	add("GET", "/api/channels", deviceRequired, (*Server).apiChannelDirectory)
	add("POST", "/api/channels/:id/follow", deviceRequired, (*Server).apiFollow)
	add("GET", "/api/chats/:id/devices", deviceRequired, (*Server).apiChatDevices)
	add("GET", "/api/chats/:id/messages", deviceRequired, (*Server).apiMessages)
	add("POST", "/api/chats/:id/messages", deviceRequired, (*Server).apiSend)
	add("GET", "/api/messages/:id", deviceRequired, (*Server).apiGetMessage)
	add("PUT", "/api/messages/:id", deviceRequired, (*Server).apiEdit)
	add("DELETE", "/api/messages/:id", deviceRequired, (*Server).apiDeleteMessage)
	add("POST", "/api/messages/:id/react", deviceRequired, (*Server).apiReact)
	add("POST", "/api/messages/:id/star", deviceRequired, (*Server).apiStar)
	add("POST", "/api/messages/:id/vote", deviceRequired, (*Server).apiVote)
	add("POST", "/api/messages/:id/opened", deviceRequired, (*Server).apiOpened)
	add("POST", "/api/chats/:id/pin", deviceRequired, (*Server).apiPin)
	add("POST", "/api/chats/:id/receipts", deviceRequired, (*Server).apiReceipts)
	add("GET", "/api/starred", deviceRequired, (*Server).apiStarred)
	add("POST", "/api/files", deviceRequired, (*Server).apiUpload).stream = true
	add("GET", "/api/files/:id", deviceRequired, (*Server).apiDownload).stream = true
	// Profile and group photos are loaded by <img> tags, which can't send
	// the device token, so these only need a tailnet identity.
	add("GET", "/api/avatars/:id", deviceNone, (*Server).apiAvatar).stream = true
	add("GET", "/api/status", deviceRequired, (*Server).apiStatuses)
	add("GET", "/api/status/devices", deviceRequired, (*Server).apiStatusDevices)
	add("POST", "/api/status", deviceRequired, (*Server).apiPostStatus)
	add("DELETE", "/api/status/:id", deviceRequired, (*Server).apiDeleteStatus)
	add("POST", "/api/status/:id/view", deviceRequired, (*Server).apiViewStatus)
	add("GET", "/api/calls", deviceRequired, (*Server).apiCallLog)
	return r
}

// server is set by the handler before a route runs.
func (c *reqCtx) server() *Server { return c.srv }

func (c *reqCtx) uid() string { return c.user.ID }

func (c *reqCtx) query(k string) string { return c.r.URL.Query().Get(k) }

func (c *reqCtx) json(limit int64) body { return readJSON(c.r, limit) }

// ---- me, devices, users ----

func (s *Server) apiMe(c *reqCtx) any {
	var dev *deviceView
	if c.device != nil {
		dev = ptr(devView(c.device))
	}
	return map[string]any{
		"user":     s.userView(c.user, c.uid()),
		"device":   dev,
		"authMode": c.mode,
		"limits": map[string]any{
			"maxUploadBytes":      s.maxUploadBytes(),
			"editWindowSeconds":   int64(s.cfg.EditWindow / time.Second),
			"deleteWindowSeconds": int64(s.cfg.DeleteWindow / time.Second),
		},
		"network": s.networkInfo(),
	}
}

func (s *Server) apiUpdateMe(c *reqCtx) any {
	b := c.json(maxJSON)
	uid := c.uid()
	name := vStr(b["name"], "name", 1, 60, true)
	about := vStr(b["about"], "about", 0, 140, true)
	hide := vBool(b["hideLastSeen"], "hideLastSeen", true)
	receipts := vBool(b["readReceipts"], "readReceipts", true)
	if b.has("avatar") {
		var avatar *string
		if b["avatar"] != nil {
			a, ok := b["avatar"].(string)
			if !ok || !s.db.exists("SELECT 1 FROM files WHERE id = ? AND owner = ? AND public = 1", a, uid) {
				fail(400, "avatar must be a public image you uploaded")
			}
			avatar = &a
		}
		s.db.run("UPDATE users SET avatar = ? WHERE id = ?", avatar, uid)
	}
	if name != nil {
		s.db.run("UPDATE users SET name = ?, custom_name = 1 WHERE id = ?", *name, uid)
	}
	if about != nil {
		s.db.run("UPDATE users SET about = ? WHERE id = ?", *about, uid)
	}
	if hide != nil {
		s.db.run("UPDATE users SET hide_last_seen = ? WHERE id = ?", *hide, uid)
	}
	if receipts != nil {
		s.db.run("UPDATE users SET read_receipts = ? WHERE id = ?", *receipts, uid)
	}
	u := s.getUser(uid)
	s.broadcastUser(u)
	return map[string]any{"user": s.userView(u, uid)}
}

func (s *Server) apiRegisterDevice(c *reqCtx) any {
	b := c.json(maxJSON)
	name := reqStr(b["name"], "name", 1, 80)
	key, ok := b["publicKey"].(map[string]any)
	x, xok := key["x"].(string)
	y, yok := key["y"].(string)
	_, hasD := key["d"]
	if !ok || key["kty"] != "EC" || key["crv"] != "P-256" || !xok || !yok || hasD {
		fail(400, "publicKey must be a P-256 public JWK")
	}
	deviceID := newID(12)
	secret := newID(32)
	pub := mustJSON(map[string]string{"kty": "EC", "crv": "P-256", "x": x, "y": y})
	s.db.run(`INSERT INTO devices (id, user_id, name, public_key, token_hash, created_at, last_active)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, deviceID, c.uid(), name, pub, hashToken(secret), now(), now())
	s.notifyDeviceChange(c.uid())
	var d deviceRow
	s.db.one(scanDevice(&d), "SELECT "+deviceCols+" FROM devices WHERE id = ?", deviceID)
	return map[string]any{"device": devView(&d), "token": deviceID + "." + secret}
}

func (s *Server) apiListDevices(c *reqCtx) any {
	return map[string]any{"devices": s.devices("SELECT "+deviceCols+" FROM devices WHERE user_id = ? ORDER BY created_at", c.uid())}
}

func (s *Server) apiRevokeDevice(c *reqCtx) any {
	var d deviceRow
	if !s.db.one(scanDevice(&d), "SELECT "+deviceCols+" FROM devices WHERE id = ? AND user_id = ?", c.params["id"], c.uid()) {
		fail(404, "No such device")
	}
	s.db.run("UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now(), d.ID)
	s.hub.revokeDevice(d.ID)
	s.notifyDeviceChange(c.uid())
	return nil
}

// apiKeys returns public keys of any devices, including logged-out ones,
// so old messages from them still decrypt.
func (s *Server) apiKeys(c *reqCtx) any {
	var ids []string
	for _, id := range strings.Split(c.query("ids"), ",") {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) > 500 {
		ids = ids[:500]
	}
	if len(ids) == 0 {
		return map[string]any{"devices": []deviceView{}}
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := "SELECT " + deviceCols + " FROM devices WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
	return map[string]any{"devices": s.devices(q, args...)}
}

// apiUsers lists every device on the tailnet: the ones with a chat account
// and the ones that haven't opened the chat yet.
func (s *Server) apiUsers(c *reqCtx) any {
	uid := c.uid()
	out := []userView{}
	known := map[string]bool{}
	var rows []*userRow
	s.db.all(func(r scanner) error {
		var u userRow
		if err := scanUser(&u)(r); err != nil {
			return err
		}
		rows = append(rows, &u)
		return nil
	}, "SELECT "+userCols+" FROM users ORDER BY name COLLATE NOCASE")
	for _, u := range rows {
		known[u.ID] = true
		s.refreshFromDirectory(u)
		out = append(out, s.userView(u, uid))
	}
	for _, d := range s.directorySnapshot() {
		if !known[d.ID] {
			out = append(out, directoryView(d))
		}
	}
	slices.SortStableFunc(out, func(a, b userView) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return map[string]any{"users": out}
}

func (s *Server) apiUserDevices(c *reqCtx) any {
	return map[string]any{"devices": s.devices("SELECT "+deviceCols+" FROM devices WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at", c.params["id"])}
}

func (s *Server) apiBlock(c *reqCtx) any {
	b := c.json(maxJSON)
	target := reqStr(b["userId"], "userId", 0, 200)
	if target == c.uid() {
		fail(400, "You cannot block yourself")
	}
	if !s.ensureUser(target) {
		fail(404, "No such user")
	}
	s.db.run("INSERT OR IGNORE INTO blocks (user_id, blocked_id) VALUES (?, ?)", c.uid(), target)
	s.pushUserTo(target, c.uid())
	s.pushUserTo(c.uid(), target)
	return nil
}

func (s *Server) apiUnblock(c *reqCtx) any {
	s.db.run("DELETE FROM blocks WHERE user_id = ? AND blocked_id = ?", c.uid(), c.params["id"])
	s.pushUserTo(c.params["id"], c.uid())
	s.pushUserTo(c.uid(), c.params["id"])
	return nil
}

// ---- chats ----

func (s *Server) apiChats(c *reqCtx) any {
	out := []chatView{}
	for _, chat := range s.chats("SELECT "+prefixed("c.", chatCols)+" FROM chats c JOIN members m ON m.chat_id = c.id WHERE m.user_id = ? AND m.removed = 0", c.uid()) {
		out = append(out, s.chatView(chat, c.uid()))
	}
	return map[string]any{"chats": out}
}

func (s *Server) apiChat(c *reqCtx) any {
	chat, _ := s.requireMember(c.params["id"], c.uid(), true)
	return map[string]any{"chat": s.chatView(chat, c.uid())}
}

func (s *Server) apiDirect(c *reqCtx) any {
	b := c.json(maxJSON)
	other := reqStr(b["userId"], "userId", 0, 200)
	if !s.ensureUser(other) {
		fail(404, "No such user")
	}
	chat := s.directChat(c.uid(), other)
	s.db.run("UPDATE members SET removed = 0 WHERE chat_id = ? AND user_id = ?", chat.ID, c.uid())
	return map[string]any{"chat": s.chatView(chat, c.uid())}
}

func (s *Server) apiGroup(c *reqCtx) any {
	b := c.json(maxJSON)
	if _, ok := b["members"].([]any); !ok {
		fail(400, "members must be a list of user ids")
	}
	name, desc, avatar, others := s.readNewChat(b, c.uid())
	chat := s.createGroup(c.uid(), name, desc, avatar, others, "group", nil)
	return map[string]any{"chat": s.chatView(chat, c.uid())}
}

// apiCommunity: a community is its announcements group (admins post,
// everyone reads) plus the groups linked to it.
func (s *Server) apiCommunity(c *reqCtx) any {
	b := c.json(maxJSON)
	name, desc, avatar, others := s.readNewChat(b, c.uid())
	chat := s.createGroup(c.uid(), name, desc, avatar, others, "community", nil)
	return map[string]any{"chat": s.chatView(chat, c.uid())}
}

// apiChannel: channels are one-way broadcasts that anyone on the tailnet
// can follow. Like WhatsApp channels, posts are not end-to-end encrypted.
func (s *Server) apiChannel(c *reqCtx) any {
	b := c.json(maxJSON)
	b["members"] = []any{}
	name, desc, avatar, _ := s.readNewChat(b, c.uid())
	chat := s.createGroup(c.uid(), name, desc, avatar, nil, "channel", nil)
	return map[string]any{"chat": s.chatView(chat, c.uid())}
}

func (s *Server) apiUpdateChat(c *reqCtx) any {
	b := c.json(maxJSON)
	uid := c.uid()
	chat, member := s.requireMember(c.params["id"], uid, false)
	isAdmin := member.Role == "admin"
	var changes []map[string]any
	info := b.has("name") || b.has("description") || b.has("avatar")
	settings := b.has("adminsOnlySend") || b.has("adminsOnlyEdit")
	if info {
		if !groupish(chat.Type) {
			fail(400, "Only groups have a name, description and photo")
		}
		if chat.AdminsOnlyEdit && !isAdmin {
			fail(403, "Only admins can edit this group's info")
		}
	}
	if settings && !isAdmin {
		fail(403, "Only admins can change group settings")
	}
	if settings && adminPosts(chat.Type) {
		fail(400, "Only admins can post and edit info here")
	}
	if b.has("name") {
		name := reqStr(b["name"], "name", 1, 100)
		s.db.run("UPDATE chats SET name = ? WHERE id = ?", name, chat.ID)
		changes = append(changes, map[string]any{"event": "renamed", "name": name})
	}
	if b.has("description") {
		s.db.run("UPDATE chats SET description = ? WHERE id = ?", reqStr(b["description"], "description", 0, 2048), chat.ID)
		changes = append(changes, map[string]any{"event": "description"})
	}
	if b.has("avatar") {
		var avatar *string
		if b["avatar"] != nil {
			var id string
			a, _ := b["avatar"].(string)
			if !s.db.scalar(&id, "SELECT id FROM files WHERE id = ? AND public = 1", a) {
				fail(400, "avatar must be a public image")
			}
			avatar = &id
		}
		s.db.run("UPDATE chats SET avatar = ? WHERE id = ?", avatar, chat.ID)
		changes = append(changes, map[string]any{"event": "photo"})
	}
	if b.has("disappearing") {
		secs := *vInt(b["disappearing"], "disappearing", 0, 90*86400, false)
		if groupish(chat.Type) && chat.AdminsOnlyEdit && !isAdmin {
			fail(403, "Only admins can change disappearing messages")
		}
		s.db.run("UPDATE chats SET disappearing = ? WHERE id = ?", secs, chat.ID)
		changes = append(changes, map[string]any{"event": "disappearing", "seconds": secs})
	}
	for _, f := range [][2]string{{"adminsOnlySend", "admins_only_send"}, {"adminsOnlyEdit", "admins_only_edit"}} {
		if !b.has(f[0]) {
			continue
		}
		v := reqBool(b[f[0]], f[0])
		s.db.run("UPDATE chats SET "+f[1]+" = ? WHERE id = ?", v, chat.ID)
		changes = append(changes, map[string]any{"event": "settings", f[0]: v})
	}
	for _, ev := range changes {
		s.systemMessage(s.getChat(chat.ID), uid, ev)
	}
	s.broadcastChat(chat.ID)
	if chat.CommunityID != nil && info {
		s.broadcastChat(*chat.CommunityID)
	}
	return map[string]any{"chat": s.chatView(s.getChat(chat.ID), uid)}
}

func (s *Server) apiPrefs(c *reqCtx) any {
	b := c.json(maxJSON)
	uid := c.uid()
	chat, _ := s.requireMember(c.params["id"], uid, true)
	set := func(col string, v any) {
		s.db.run("UPDATE members SET "+col+" = ? WHERE chat_id = ? AND user_id = ?", v, chat.ID, uid)
	}
	if b.has("pinned") {
		if reqBool(b["pinned"], "pinned") {
			if s.db.count("SELECT COUNT(*) FROM members WHERE user_id = ? AND pinned_at IS NOT NULL AND chat_id != ?", uid, chat.ID) >= 3 {
				fail(400, "You can only pin up to 3 chats")
			}
			set("pinned_at", now())
		} else {
			set("pinned_at", nil)
		}
	}
	if b.has("archived") {
		set("archived", reqBool(b["archived"], "archived"))
	}
	if b.has("mutedUntil") {
		set("muted_until", *vInt(b["mutedUntil"], "mutedUntil", 0, big, false))
	}
	if b.has("markedUnread") {
		set("marked_unread", reqBool(b["markedUnread"], "markedUnread"))
	}
	v := s.chatView(chat, uid)
	s.hub.sendToUser(uid, map[string]any{"type": "chat", "chat": v}, sendOpts{})
	return map[string]any{"chat": v}
}

func (s *Server) apiClear(c *reqCtx) any {
	uid := c.uid()
	chat, _ := s.requireMember(c.params["id"], uid, true)
	seq := s.getChat(chat.ID).LastSeq
	s.db.run("UPDATE members SET cleared_seq = ?, read_seq = MAX(read_seq, ?) WHERE chat_id = ? AND user_id = ?", seq, seq, chat.ID, uid)
	v := s.chatView(chat, uid)
	s.hub.sendToUser(uid, map[string]any{"type": "chat", "chat": v}, sendOpts{})
	return map[string]any{"chat": v}
}

// apiDeleteChat: "Delete chat" clears it and hides it until someone writes
// again. A former group member's copy is removed for good.
func (s *Server) apiDeleteChat(c *reqCtx) any {
	uid := c.uid()
	chat, member := s.requireMember(c.params["id"], uid, true)
	if groupish(chat.Type) && member.active() {
		fail(400, "Exit the group before deleting it")
	}
	seq := chat.LastSeq
	if !member.active() {
		s.db.run("DELETE FROM members WHERE chat_id = ? AND user_id = ?", chat.ID, uid)
	} else {
		s.db.run(`UPDATE members SET cleared_seq = ?, read_seq = MAX(read_seq, ?), removed = 1, pinned_at = NULL
			WHERE chat_id = ? AND user_id = ?`, seq, seq, chat.ID, uid)
	}
	s.hub.sendToUser(uid, map[string]any{"type": "chat-removed", "chatId": chat.ID}, sendOpts{})
	return nil
}

func (s *Server) apiAddMembers(c *reqCtx) any {
	b := c.json(maxJSON)
	chat, _ := s.requireAdmin(c.params["id"], c.uid())
	list, ok := b["userIds"].([]any)
	if !ok || len(list) == 0 {
		fail(400, "userIds must be a non-empty list")
	}
	var ids []string
	for _, x := range list {
		if id, ok := x.(string); ok {
			ids = append(ids, id)
		}
	}
	added := s.addMembers(chat, c.uid(), ids, false, "")
	for _, uid := range added {
		s.notifyDeviceChange(uid)
	}
	s.broadcastChat(chat.ID)
	if added == nil {
		added = []string{}
	}
	return map[string]any{"added": added}
}

func (s *Server) apiRemoveMember(c *reqCtx) any {
	target := c.params["userId"]
	var chat *chatRow
	if target == c.uid() {
		chat, _ = s.requireMember(c.params["id"], c.uid(), false)
	} else {
		chat, _ = s.requireAdmin(c.params["id"], c.uid())
	}
	if !groupish(chat.Type) {
		fail(400, "You can only leave groups")
	}
	tm := s.activeMember(chat.ID, target)
	if tm == nil {
		fail(404, "Not a member")
	}
	if chat.Type == "channel" && tm.Role == "admin" &&
		!s.db.exists("SELECT 1 FROM members WHERE chat_id = ? AND left_at IS NULL AND role = 'admin' AND user_id != ?", chat.ID, target) {
		fail(400, "Make another follower an admin before leaving your channel")
	}
	s.removeMember(chat, c.uid(), target)
	return nil
}

func (s *Server) apiAdmins(c *reqCtx) any {
	b := c.json(maxJSON)
	chat, _ := s.requireAdmin(c.params["id"], c.uid())
	target := reqStr(b["userId"], "userId", 0, 200)
	admin := reqBool(b["admin"], "admin")
	if s.activeMember(chat.ID, target) == nil {
		fail(404, "Not a member")
	}
	if !admin && s.db.count("SELECT COUNT(*) FROM members WHERE chat_id = ? AND left_at IS NULL AND role = 'admin'", chat.ID) <= 1 {
		fail(400, "A group needs at least one admin")
	}
	role := "member"
	if admin {
		role = "admin"
	}
	s.db.run("UPDATE members SET role = ? WHERE chat_id = ? AND user_id = ?", role, chat.ID, target)
	if chat.Type != "channel" {
		event := "demoted"
		if admin {
			event = "promoted"
		}
		s.systemMessage(chat, c.uid(), map[string]any{"event": event, "users": []string{target}})
	}
	s.broadcastChat(chat.ID)
	return nil
}

func (s *Server) apiNewInvite(c *reqCtx) any {
	chat, _ := s.requireAdmin(c.params["id"], c.uid())
	code := newID(12)
	s.db.run("UPDATE chats SET invite_code = ? WHERE id = ?", code, chat.ID)
	s.broadcastChat(chat.ID)
	return map[string]any{"code": code}
}

func (s *Server) apiResetInvite(c *reqCtx) any {
	chat, _ := s.requireAdmin(c.params["id"], c.uid())
	s.db.run("UPDATE chats SET invite_code = NULL WHERE id = ?", chat.ID)
	s.broadcastChat(chat.ID)
	return nil
}

func (s *Server) inviteChat(code string) *chatRow {
	var chat chatRow
	if !s.db.one(scanChat(&chat), "SELECT "+chatCols+" FROM chats WHERE invite_code = ?", code) {
		fail(404, "This invite link was reset or never existed")
	}
	return &chat
}

func (s *Server) apiInvitePreview(c *reqCtx) any {
	chat := s.inviteChat(c.params["code"])
	return map[string]any{"chat": map[string]any{
		"id":          chat.ID,
		"name":        chat.Name,
		"description": chat.Description,
		"avatar":      avatarURL(chat.Avatar),
		"type":        chat.Type,
		"memberCount": len(s.activeMemberIDs(chat.ID)),
		"isMember":    s.activeMember(chat.ID, c.uid()) != nil,
	}}
}

func (s *Server) apiInviteJoin(c *reqCtx) any {
	chat := s.inviteChat(c.params["code"])
	s.addMembers(chat, c.uid(), []string{c.uid()}, false, "")
	s.notifyDeviceChange(c.uid())
	s.broadcastChat(chat.ID)
	return map[string]any{"chat": s.chatView(s.getChat(chat.ID), c.uid())}
}

// ---- communities ----

func (s *Server) requireCommunityAdmin(id, uid string) *chatRow {
	chat, _ := s.requireAdmin(id, uid)
	if chat.Type != "community" {
		fail(404, "No such community")
	}
	return chat
}

func (s *Server) linkedGroup(community *chatRow, groupID string) *chatRow {
	g := s.getChat(groupID)
	if g == nil || g.CommunityID == nil || *g.CommunityID != community.ID {
		fail(404, "That group isn't in this community")
	}
	return g
}

// apiCommunityAddGroup links an existing group you admin, or creates a new
// group inside the community.
func (s *Server) apiCommunityAddGroup(c *reqCtx) any {
	b := c.json(maxJSON)
	uid := c.uid()
	community := s.requireCommunityAdmin(c.params["id"], uid)
	if !b.has("groupId") {
		name, desc, avatar, others := s.readNewChat(b, uid)
		g := s.createGroup(uid, name, desc, avatar, others, "group", &community.ID)
		return map[string]any{"chat": s.chatView(g, uid)}
	}
	group, _ := s.requireAdmin(reqStr(b["groupId"], "groupId", 0, 200), uid)
	if group.Type != "group" {
		fail(400, "Only groups can be added to a community")
	}
	if group.CommunityID != nil {
		fail(400, "That group is already in a community")
	}
	if s.db.count("SELECT COUNT(*) FROM chats WHERE community_id = ?", community.ID) >= maxCommunityKids {
		failf(400, "A community can have up to %d groups", maxCommunityKids)
	}
	s.db.run("UPDATE chats SET community_id = ? WHERE id = ?", community.ID, group.ID)
	s.addMembers(community, uid, s.activeMemberIDs(group.ID), true, "")
	s.systemMessage(s.getChat(group.ID), uid, map[string]any{"event": "community", "name": community.name()})
	s.systemMessage(community, uid, map[string]any{"event": "community-group", "name": group.name()})
	s.broadcastChat(group.ID)
	s.broadcastChat(community.ID)
	return map[string]any{"chat": s.chatView(s.getChat(group.ID), uid)}
}

func (s *Server) apiCommunityRemoveGroup(c *reqCtx) any {
	uid := c.uid()
	community := s.requireCommunityAdmin(c.params["id"], uid)
	group := s.linkedGroup(community, c.params["groupId"])
	s.db.run("UPDATE chats SET community_id = NULL WHERE id = ?", group.ID)
	s.systemMessage(s.getChat(group.ID), uid, map[string]any{"event": "community-left", "name": community.name()})
	s.systemMessage(community, uid, map[string]any{"event": "community-group-removed", "name": group.name()})
	s.broadcastChat(group.ID)
	s.broadcastChat(community.ID)
	return nil
}

// apiCommunityJoinGroup: community members can join any of its groups.
func (s *Server) apiCommunityJoinGroup(c *reqCtx) any {
	uid := c.uid()
	community, _ := s.requireMember(c.params["id"], uid, false)
	if community.Type != "community" {
		fail(404, "No such community")
	}
	group := s.linkedGroup(community, c.params["groupId"])
	s.addMembers(group, uid, []string{uid}, false, "community")
	s.notifyDeviceChange(uid)
	s.broadcastChat(group.ID)
	s.broadcastChat(community.ID)
	return map[string]any{"chat": s.chatView(s.getChat(group.ID), uid)}
}

// apiDeactivateCommunity unlinks every group (they carry on as normal
// groups) and closes the announcements group for everyone.
func (s *Server) apiDeactivateCommunity(c *reqCtx) any {
	uid := c.uid()
	community := s.requireCommunityAdmin(c.params["id"], uid)
	for _, g := range s.chats("SELECT "+chatCols+" FROM chats WHERE community_id = ?", community.ID) {
		s.db.run("UPDATE chats SET community_id = NULL WHERE id = ?", g.ID)
		s.systemMessage(s.getChat(g.ID), uid, map[string]any{"event": "community-left", "name": community.name()})
		s.broadcastChat(g.ID)
	}
	s.systemMessage(community, uid, map[string]any{"event": "deactivated"})
	seq := s.getChat(community.ID).LastSeq
	s.db.run("UPDATE members SET left_at = ?, left_seq = ?, role = 'member' WHERE chat_id = ? AND left_at IS NULL", now(), seq, community.ID)
	s.db.run("UPDATE chats SET invite_code = NULL WHERE id = ?", community.ID)
	s.broadcastChat(community.ID)
	return nil
}

// ---- channels ----

func (s *Server) apiChannelDirectory(c *reqCtx) any {
	type entry struct {
		ID          string  `json:"id"`
		Name        *string `json:"name"`
		Description string  `json:"description"`
		Avatar      *string `json:"avatar"`
		Followers   int64   `json:"followers"`
		Following   bool    `json:"following"`
	}
	out := []entry{}
	s.db.all(func(r scanner) error {
		var e entry
		var following *int64
		if err := r.Scan(&e.ID, &e.Name, &e.Description, &e.Avatar, &e.Followers, &following); err != nil {
			return err
		}
		e.Avatar = avatarURL(e.Avatar)
		e.Following = following != nil
		out = append(out, e)
		return nil
	}, `SELECT c.id, c.name, c.description, c.avatar,
		  (SELECT COUNT(*) FROM members m WHERE m.chat_id = c.id AND m.left_at IS NULL) AS n,
		  (SELECT 1 FROM members m WHERE m.chat_id = c.id AND m.user_id = ? AND m.left_at IS NULL)
		FROM chats c WHERE c.type = 'channel'
		  AND EXISTS (SELECT 1 FROM members m WHERE m.chat_id = c.id AND m.left_at IS NULL AND m.role = 'admin')
		ORDER BY n DESC, c.name COLLATE NOCASE LIMIT 500`, c.uid())
	return map[string]any{"channels": out}
}

func (s *Server) apiFollow(c *reqCtx) any {
	b := c.json(maxJSON)
	uid := c.uid()
	chat := s.getChat(c.params["id"])
	if chat == nil || chat.Type != "channel" {
		fail(404, "No such channel")
	}
	if reqBool(b["follow"], "follow") {
		s.addMembers(chat, uid, []string{uid}, false, "")
		s.broadcastChat(chat.ID)
	} else {
		m := s.activeMember(chat.ID, uid)
		if m == nil {
			return nil
		}
		if m.Role == "admin" && !s.db.exists("SELECT 1 FROM members WHERE chat_id = ? AND left_at IS NULL AND role = 'admin' AND user_id != ?", chat.ID, uid) {
			fail(400, "Make another follower an admin before leaving your channel")
		}
		s.removeMember(chat, uid, uid)
	}
	return map[string]any{"chat": s.chatView(s.getChat(chat.ID), uid)}
}

// apiChatDevices lists every logged-in browser of every current member:
// the sender wraps the message key once for each of them.
func (s *Server) apiChatDevices(c *reqCtx) any {
	chat, _ := s.requireMember(c.params["id"], c.uid(), false)
	if chat.Type == "channel" {
		fail(400, "Channel posts aren't encrypted to devices")
	}
	return map[string]any{"devices": s.devices(`SELECT `+prefixed("d.", deviceCols)+` FROM devices d JOIN members m ON m.user_id = d.user_id
		WHERE m.chat_id = ? AND m.left_at IS NULL AND d.revoked_at IS NULL`, chat.ID)}
}

// ---- messages ----

func (s *Server) apiMessages(c *reqCtx) any {
	uid := c.uid()
	chat, member := s.requireMember(c.params["id"], uid, true)
	qint := func(name string, def int64) int64 {
		v := c.query(name)
		if v == "" {
			return def
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return def
		}
		return n
	}
	limit := min(max(qint("limit", 50), 1), 200)
	before := qint("before", 0)
	after := qint("after", 0)
	floor := max(visibleFloor(member), after)
	ceiling := ceilingOf(member)
	if before != 0 {
		ceiling = min(ceiling, before-1)
	}
	order := "DESC"
	if after != 0 {
		order = "ASC"
	}
	rows := s.messages(`SELECT `+messageCols+` FROM messages WHERE chat_id = ? AND seq > ? AND seq <= ?
		  AND id NOT IN (SELECT message_id FROM hidden WHERE user_id = ?)
		ORDER BY seq `+order+` LIMIT ?`, chat.ID, floor, ceiling, uid, limit)
	if order == "DESC" {
		slices.Reverse(rows)
	}
	out := []messageView{}
	for _, m := range rows {
		out = append(out, s.messageView(m, uid))
	}
	return map[string]any{"messages": out, "hasMore": int64(len(rows)) == limit}
}

func (s *Server) apiSend(c *reqCtx) any {
	b := c.json(maxPayload + 64*1024)
	uid := c.uid()
	chat, member := s.requireMember(c.params["id"], uid, false)
	if groupish(chat.Type) && chat.AdminsOnlySend && member.Role != "admin" {
		fail(403, "Only admins can send messages here")
	}
	if chat.Type == "direct" {
		for _, other := range s.activeMemberIDs(chat.ID) {
			if other == uid {
				continue
			}
			if s.isBlocked(uid, other) {
				fail(403, "Unblock this contact to send a message")
			}
			if s.isBlocked(other, uid) {
				fail(403, "You can't message this contact")
			}
			break
		}
	}
	payload := checkPayload(chat, b["payload"])
	var messageID string
	if b.has("id") {
		messageID = reqStr(b["id"], "id", 16, 32)
		if !validMessageID(messageID) {
			fail(400, "id must be base64url")
		}
	} else {
		messageID = newID(16)
	}
	// Retrying a send after a dropped connection must not duplicate it.
	if existing := s.getMessage(messageID); existing != nil {
		if existing.SenderID != uid {
			fail(409, "Message id already in use")
		}
		return map[string]any{"message": s.messageView(existing, uid)}
	}
	files := s.checkFiles(b["files"], uid)
	viewOnce, _ := b["viewOnce"].(bool)
	m := s.insertMessage(chat, newMessage{
		id: messageID, senderID: uid, senderDevice: &c.device.ID, kind: "msg",
		payload: &payload, files: files, viewOnce: viewOnce,
	})
	s.broadcastMessage(m, "message", sendOpts{})
	s.broadcastChat(chat.ID)
	if chat.Type == "direct" {
		s.radioOutgoing(chat, m, c.user)
	}
	return map[string]any{"message": s.messageView(m, uid)}
}

func (s *Server) apiGetMessage(c *reqCtx) any {
	m := s.requireMessage(c.params["id"], c.uid())
	if s.db.exists("SELECT 1 FROM hidden WHERE user_id = ? AND message_id = ?", c.uid(), m.ID) {
		fail(404, "No such message")
	}
	return map[string]any{"message": s.messageView(m, c.uid())}
}

func (s *Server) apiEdit(c *reqCtx) any {
	b := c.json(maxPayload + 64*1024)
	uid := c.uid()
	m := s.requireMessage(c.params["id"], uid)
	if m.SenderID != uid || m.Kind != "msg" {
		fail(403, "You can only edit your own messages")
	}
	if m.DeletedAt != nil {
		fail(400, "This message was deleted")
	}
	if now()-m.CreatedAt > s.cfg.EditWindow.Milliseconds() {
		fail(400, "Messages can only be edited for a short time after sending")
	}
	if s.activeMember(m.ChatID, uid) == nil {
		fail(403, "You are not in this chat")
	}
	payload := checkPayload(s.getChat(m.ChatID), b["payload"])
	s.db.run("UPDATE messages SET payload = ?, sender_device = ?, edited_at = ? WHERE id = ?", payload, c.device.ID, now(), m.ID)
	updated := s.getMessage(m.ID)
	s.broadcastMessage(updated, "message-update", sendOpts{})
	return map[string]any{"message": s.messageView(updated, uid)}
}

func (s *Server) apiDeleteMessage(c *reqCtx) any {
	uid := c.uid()
	m := s.requireMessage(c.params["id"], uid)
	if c.query("for") == "everyone" {
		chat := s.getChat(m.ChatID)
		member := s.activeMember(m.ChatID, uid)
		asAdmin := groupish(chat.Type) && member != nil && member.Role == "admin"
		if m.Kind != "msg" || (m.SenderID != uid && !asAdmin) {
			fail(403, "You can only delete your own messages for everyone")
		}
		if m.SenderID == uid && now()-m.CreatedAt > s.cfg.DeleteWindow.Milliseconds() {
			fail(400, "This message is too old to delete for everyone")
		}
		s.wipeMessage(m)
		s.db.run("UPDATE messages SET payload = NULL, files = '[]', deleted_at = ? WHERE id = ?", now(), m.ID)
		if chat.PinnedMessage != nil && *chat.PinnedMessage == m.ID {
			s.db.run("UPDATE chats SET pinned_message = NULL WHERE id = ?", chat.ID)
		}
		s.broadcastMessage(s.getMessage(m.ID), "message-update", sendOpts{})
		s.broadcastChat(m.ChatID)
	} else {
		s.db.run("INSERT OR IGNORE INTO hidden (user_id, message_id) VALUES (?, ?)", uid, m.ID)
		s.hub.sendToUser(uid, map[string]any{"type": "message-gone", "chatId": m.ChatID, "id": m.ID}, sendOpts{})
	}
	return nil
}

func (s *Server) apiReact(c *reqCtx) any {
	b := c.json(maxJSON)
	uid := c.uid()
	m := s.requireMessage(c.params["id"], uid)
	if s.activeMember(m.ChatID, uid) == nil {
		fail(403, "You are not in this chat")
	}
	if m.Kind != "msg" || m.DeletedAt != nil {
		fail(400, "You can't react to this message")
	}
	if b["emoji"] == nil {
		s.db.run("DELETE FROM reactions WHERE message_id = ? AND user_id = ?", m.ID, uid)
	} else {
		emoji := reqStr(b["emoji"], "emoji", 0, 32)
		if !isEmoji(emoji) {
			fail(400, "emoji must be a single emoji")
		}
		s.db.run(`INSERT INTO reactions (message_id, user_id, emoji, at) VALUES (?, ?, ?, ?)
			ON CONFLICT (message_id, user_id) DO UPDATE SET emoji = excluded.emoji, at = excluded.at`, m.ID, uid, emoji, now())
	}
	s.broadcastMessage(m, "message-update", sendOpts{})
	return nil
}

func (s *Server) apiStar(c *reqCtx) any {
	b := c.json(maxJSON)
	uid := c.uid()
	m := s.requireMessage(c.params["id"], uid)
	if reqBool(b["starred"], "starred") {
		s.db.run("INSERT OR IGNORE INTO stars (user_id, message_id, at) VALUES (?, ?, ?)", uid, m.ID, now())
	} else {
		s.db.run("DELETE FROM stars WHERE user_id = ? AND message_id = ?", uid, m.ID)
	}
	s.hub.sendToUser(uid, map[string]any{"type": "message-update", "message": s.messageView(m, uid)}, sendOpts{})
	return nil
}

// apiVote: poll options are encrypted; the server only stores the chosen
// indices.
func (s *Server) apiVote(c *reqCtx) any {
	b := c.json(maxJSON)
	uid := c.uid()
	m := s.requireMessage(c.params["id"], uid)
	if s.activeMember(m.ChatID, uid) == nil {
		fail(403, "You are not in this chat")
	}
	if m.Kind != "msg" || m.DeletedAt != nil {
		fail(400, "This poll was deleted")
	}
	raw, ok := b["options"].([]any)
	if !ok || len(raw) > 12 {
		fail(400, "options must be a list of option indices")
	}
	set := map[int64]bool{}
	for _, x := range raw {
		n, ok := asInt(x)
		if !ok || n < 0 || n >= 12 {
			fail(400, "options must be a list of option indices")
		}
		set[n] = true
	}
	options := make([]int64, 0, len(set))
	for n := range set {
		options = append(options, n)
	}
	slices.Sort(options)
	if len(options) > 0 {
		s.db.run(`INSERT INTO poll_votes (message_id, user_id, options, at) VALUES (?, ?, ?, ?)
			ON CONFLICT (message_id, user_id) DO UPDATE SET options = excluded.options, at = excluded.at`, m.ID, uid, mustJSON(options), now())
	} else {
		s.db.run("DELETE FROM poll_votes WHERE message_id = ? AND user_id = ?", m.ID, uid)
	}
	s.broadcastMessage(m, "message-update", sendOpts{})
	return nil
}

func (s *Server) apiOpened(c *reqCtx) any {
	uid := c.uid()
	m := s.requireMessage(c.params["id"], uid)
	if !m.ViewOnce || m.SenderID == uid {
		fail(400, "Not a view-once message for you")
	}
	s.db.run("INSERT OR IGNORE INTO view_once_opened (message_id, user_id, at) VALUES (?, ?, ?)", m.ID, uid, now())
	s.broadcastMessage(m, "message-update", sendOpts{})
	return nil
}

func (s *Server) apiPin(c *reqCtx) any {
	b := c.json(maxJSON)
	uid := c.uid()
	chat, member := s.requireMember(c.params["id"], uid, false)
	if groupish(chat.Type) && chat.AdminsOnlyEdit && member.Role != "admin" {
		fail(403, "Only admins can pin messages")
	}
	var messageID *string
	if b["messageId"] != nil {
		m := s.requireMessage(reqStr(b["messageId"], "messageId", 0, 200), uid)
		if m.ChatID != chat.ID || m.Kind != "msg" || m.DeletedAt != nil {
			fail(400, "You can't pin that message")
		}
		messageID = &m.ID
	}
	s.db.run("UPDATE chats SET pinned_message = ? WHERE id = ?", messageID, chat.ID)
	event := "unpinned"
	if messageID != nil {
		event = "pinned"
	}
	s.systemMessage(chat, uid, map[string]any{"event": event, "messageId": messageID})
	s.broadcastChat(chat.ID)
	return nil
}

func (s *Server) apiReceipts(c *reqCtx) any {
	b := c.json(maxJSON)
	uid := c.uid()
	chat, member := s.requireMember(c.params["id"], uid, true)
	ceiling := min(chat.LastSeq, ceilingOf(member))
	delivered, read := int64(0), int64(0)
	if v := vInt(b["delivered"], "delivered", 0, big, true); v != nil {
		delivered = *v
	}
	if v := vInt(b["read"], "read", 0, big, true); v != nil {
		read = *v
	}
	delivered, read = min(delivered, ceiling), min(read, ceiling)
	nextDelivered := max(member.DeliveredSeq, delivered, read)
	nextRead := max(member.ReadSeq, read)
	if nextDelivered == member.DeliveredSeq && nextRead == member.ReadSeq && !(read > 0 && member.MarkedUnread) {
		return nil
	}
	s.db.run(`UPDATE members SET delivered_seq = ?, read_seq = ?,
		  marked_unread = CASE WHEN ? > 0 THEN 0 ELSE marked_unread END
		WHERE chat_id = ? AND user_id = ?`, nextDelivered, nextRead, read, chat.ID, uid)
	fresh := s.getMember(chat.ID, uid)
	watchers := s.everMemberIDs(chat.ID)
	if chat.Type == "channel" {
		var keep []string
		for _, w := range watchers {
			if w == uid {
				keep = append(keep, w)
			} else if m := s.activeMember(chat.ID, w); m != nil && m.Role == "admin" {
				keep = append(keep, w)
			}
		}
		watchers = keep
	}
	for _, other := range watchers {
		s.hub.sendToUser(other, map[string]any{"type": "receipt", "chatId": chat.ID, "member": s.memberView(fresh, chat, other)}, sendOpts{})
	}
	s.hub.sendToUser(uid, map[string]any{"type": "chat", "chat": s.chatView(chat, uid)}, sendOpts{})
	return nil
}

func (s *Server) apiStarred(c *reqCtx) any {
	uid := c.uid()
	out := []messageView{}
	for _, m := range s.messages(`SELECT `+prefixed("m.", messageCols)+` FROM stars s JOIN messages m ON m.id = s.message_id
		WHERE s.user_id = ? ORDER BY s.at DESC LIMIT 500`, uid) {
		if s.canSeeMessage(m, uid) {
			out = append(out, s.messageView(m, uid))
		}
	}
	return map[string]any{"messages": out}
}

// ---- status (stories) ----

func (s *Server) apiStatuses(c *reqCtx) any {
	uid := c.uid()
	out := []statusView{}
	for _, st := range s.statuses("SELECT "+statusCols+" FROM statuses WHERE expires_at > ? ORDER BY created_at", now()) {
		if st.UserID == uid || !s.eitherBlocked(st.UserID, uid) {
			out = append(out, s.statusView(st, uid))
		}
	}
	return map[string]any{"statuses": out}
}

// apiStatusDevices: status updates are encrypted to every logged-in
// browser on the server (every device on the tailnet that uses the chat),
// like "My contacts" on WhatsApp.
func (s *Server) apiStatusDevices(c *reqCtx) any {
	return map[string]any{"devices": s.devices("SELECT " + deviceCols + " FROM devices WHERE revoked_at IS NULL AND id NOT IN (SELECT device_id FROM radio_keys)")}
}

func (s *Server) apiPostStatus(c *reqCtx) any {
	b := c.json(maxPayload + 64*1024)
	uid := c.uid()
	payload, ok := b["payload"].(string)
	if !ok || len(payload) > maxPayload {
		fail(400, "payload must be an encrypted envelope string")
	}
	files := s.checkFiles(b["files"], uid)
	id := newID(16)
	created := now()
	s.db.inTx(func() {
		s.db.run("INSERT INTO statuses (id, user_id, payload, files, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)",
			id, uid, payload, mustJSON(files), created, created+int64(s.cfg.StatusHours)*3600*1000)
		for _, f := range files {
			s.db.run("INSERT INTO file_links (file_id, status_id) VALUES (?, ?)", f, id)
		}
	})
	var st statusRow
	s.db.one(scanStatus(&st), "SELECT "+statusCols+" FROM statuses WHERE id = ?", id)
	for _, other := range s.allUserIDs() {
		if other == uid || !s.eitherBlocked(other, uid) {
			s.hub.sendToUser(other, map[string]any{"type": "status", "status": s.statusView(&st, other)}, sendOpts{})
		}
	}
	return map[string]any{"status": s.statusView(&st, uid)}
}

func (s *Server) apiDeleteStatus(c *reqCtx) any {
	var st statusRow
	if !s.db.one(scanStatus(&st), "SELECT "+statusCols+" FROM statuses WHERE id = ? AND user_id = ?", c.params["id"], c.uid()) {
		fail(404, "No such status")
	}
	s.deleteStatus(&st)
	return nil
}

func (s *Server) apiViewStatus(c *reqCtx) any {
	uid := c.uid()
	var st statusRow
	if !s.db.one(scanStatus(&st), "SELECT "+statusCols+" FROM statuses WHERE id = ? AND expires_at > ?", c.params["id"], now()) ||
		s.eitherBlocked(st.UserID, uid) {
		fail(404, "No such status")
	}
	if st.UserID == uid {
		return nil
	}
	// With read receipts off, your views aren't recorded (WhatsApp behaves the same).
	var receipts bool
	s.db.scalar(&receipts, "SELECT read_receipts FROM users WHERE id = ?", uid)
	if receipts {
		s.db.run("INSERT OR IGNORE INTO status_views (status_id, viewer_id, at) VALUES (?, ?, ?)", st.ID, uid, now())
		s.hub.sendToUser(st.UserID, map[string]any{"type": "status", "status": s.statusView(&st, st.UserID)}, sendOpts{})
	}
	return nil
}

// ---- calls ----

func (s *Server) apiCallLog(c *reqCtx) any {
	type entry struct {
		ID           string          `json:"id"`
		ChatID       string          `json:"chatId"`
		CallerID     string          `json:"callerId"`
		Video        bool            `json:"video"`
		StartedAt    int64           `json:"startedAt"`
		AnsweredAt   *int64          `json:"answeredAt"`
		EndedAt      *int64          `json:"endedAt"`
		Participants json.RawMessage `json:"participants"`
	}
	out := []entry{}
	s.db.all(func(r scanner) error {
		var cr callRow
		if err := scanCall(&cr)(r); err != nil {
			return err
		}
		out = append(out, entry{cr.ID, cr.ChatID, cr.CallerID, cr.Video, cr.StartedAt, cr.AnsweredAt, cr.EndedAt, json.RawMessage(cr.Participants)})
		return nil
	}, `SELECT `+prefixed("c.", callCols)+` FROM calls c JOIN members m ON m.chat_id = c.chat_id AND m.user_id = ?
		ORDER BY c.started_at DESC LIMIT 200`, c.uid())
	return map[string]any{"calls": out}
}
