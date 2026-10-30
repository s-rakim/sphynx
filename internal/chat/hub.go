package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// The hub pushes new messages, receipts, presence and typing to every
// connected browser, and relays call signalling between them.

type socket struct {
	conn     *websocket.Conn
	userID   string
	deviceID string
	// Outgoing frames go through a queue so events are delivered in order
	// without the sender waiting on the network.
	out    chan []byte
	cancel context.CancelFunc
	once   sync.Once
}

func (sk *socket) send(data []byte) {
	select {
	case sk.out <- data:
	default:
		// A client that stopped reading is dropped; it resyncs on reconnect.
		go sk.close(4008, "too slow")
	}
}

func (sk *socket) close(code websocket.StatusCode, reason string) {
	sk.once.Do(func() {
		go func() {
			_ = sk.conn.Close(code, reason)
			sk.cancel()
		}()
	})
}

type hub struct {
	s      *Server
	mu     sync.Mutex
	byUser map[string]map[*socket]bool
	closed bool
}

func newHub(s *Server) *hub { return &hub{s: s, byUser: map[string]map[*socket]bool{}} }

type sendOpts struct {
	exceptDevice string
	onlyDevice   string
}

func (h *hub) isOnline(userID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.byUser[userID]) > 0
}

func (h *hub) sendToUser(userID string, event any, o sendOpts) {
	h.mu.Lock()
	sockets := make([]*socket, 0, len(h.byUser[userID]))
	for sk := range h.byUser[userID] {
		sockets = append(sockets, sk)
	}
	h.mu.Unlock()
	if len(sockets) == 0 {
		return
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	for _, sk := range sockets {
		if o.exceptDevice != "" && sk.deviceID == o.exceptDevice {
			continue
		}
		if o.onlyDevice != "" && sk.deviceID != o.onlyDevice {
			continue
		}
		sk.send(data)
	}
}

func (h *hub) sendToUsers(userIDs []string, event any, o sendOpts) {
	for _, uid := range uniq(userIDs) {
		h.sendToUser(uid, event, o)
	}
}

func (h *hub) revokeDevice(deviceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.byUser {
		for sk := range set {
			if sk.deviceID == deviceID {
				sk.close(4003, "device revoked")
			}
		}
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.disconnectAll(websocket.StatusGoingAway, "server shutting down")
}

func (h *hub) disconnectAll(code websocket.StatusCode, reason string) {
	h.mu.Lock()
	var all []*socket
	for _, set := range h.byUser {
		for sk := range set {
			all = append(all, sk)
		}
	}
	h.mu.Unlock()
	for _, sk := range all {
		sk.close(code, reason)
	}
}

// serve upgrades the request and runs the socket until it closes. The
// first frame must authenticate the browser with its device token.
func (h *hub) serve(w http.ResponseWriter, r *http.Request, id *Identity) {
	// The handler already refused cross-origin upgrades.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(256 * 1024)
	ctx, cancel := context.WithCancel(h.s.ctx)
	defer cancel()

	var device *deviceRow
	func() {
		actx, acancel := context.WithTimeout(ctx, 10*time.Second)
		defer acancel()
		typ, data, err := conn.Read(actx)
		if err != nil || typ != websocket.MessageText {
			return
		}
		var msg struct {
			Type  string `json:"type"`
			Token any    `json:"token"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.Type != "auth" {
			return
		}
		h.s.mu.Lock()
		_ = guard(func() { device = h.s.authenticateToken(msg.Token, id) })
		h.s.mu.Unlock()
	}()
	if device == nil {
		_ = conn.Close(4003, "auth failed")
		return
	}

	sk := &socket{conn: conn, userID: device.UserID, deviceID: device.ID, out: make(chan []byte, 5000), cancel: cancel}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	set := h.byUser[sk.userID]
	if set == nil {
		set = map[*socket]bool{}
		h.byUser[sk.userID] = set
	}
	first := len(set) == 0
	set[sk] = true
	h.mu.Unlock()

	go h.writer(ctx, sk)
	go h.heartbeat(ctx, sk)
	ready, _ := json.Marshal(map[string]any{"type": "ready", "deviceId": sk.deviceID})
	sk.send(ready)
	if first {
		h.s.locked(func() { h.s.presenceChanged(sk.userID) })
	}

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) != nil || msg == nil {
			continue
		}
		h.s.mu.Lock()
		err = guard(func() { h.s.onSocketMessage(sk, msg) })
		h.s.mu.Unlock()
		if err != nil {
			// A bad frame must not kill the socket.
			_, text := errorStatus(err)
			if text == "Internal error" {
				logf("chat: websocket frame failed: %v", err)
			}
			out, _ := json.Marshal(map[string]any{"type": "error", "message": text})
			sk.send(out)
		}
	}

	h.mu.Lock()
	delete(set, sk)
	last := len(set) == 0
	if last {
		delete(h.byUser, sk.userID)
	}
	closed := h.closed
	h.mu.Unlock()
	cancel()
	if !closed {
		h.s.locked(func() {
			h.s.dropDeviceFromCalls(sk.deviceID)
			if last {
				h.s.onDisconnect(sk.userID)
			}
		})
	}
}

func (h *hub) writer(ctx context.Context, sk *socket) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-sk.out:
			wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := sk.conn.Write(wctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				sk.close(websocket.StatusAbnormalClosure, "write failed")
				return
			}
		}
	}
}

// heartbeat pings every 30 seconds so dead connections are noticed.
func (h *hub) heartbeat(ctx context.Context, sk *socket) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := sk.conn.Ping(pctx)
			cancel()
			if err != nil {
				sk.close(websocket.StatusGoingAway, "no pong")
				return
			}
		}
	}
}

// ---- presence and typing ----

func (s *Server) presenceChanged(userID string) {
	u := s.getUser(userID)
	if u == nil {
		return
	}
	for _, uid := range s.allUserIDs() {
		if uid != userID {
			s.hub.sendToUser(uid, map[string]any{"type": "user", "user": s.userView(u, uid)}, sendOpts{})
		}
	}
}

func (s *Server) onDisconnect(userID string) {
	s.db.run("UPDATE users SET last_seen = ? WHERE id = ?", now(), userID)
	s.presenceChanged(userID)
}

func (s *Server) onSocketMessage(sk *socket, msg map[string]any) {
	switch msg["type"] {
	case "typing":
		chatID, _ := msg["chatId"].(string)
		member := s.activeMember(chatID, sk.userID)
		if member == nil {
			return
		}
		chat := s.getChat(chatID)
		if chat.Type == "channel" || (groupish(chat.Type) && chat.AdminsOnlySend && member.Role != "admin") {
			return
		}
		kind := "typing"
		if msg["kind"] == "recording" {
			kind = "recording"
		}
		on := truthy(msg["on"])
		for _, uid := range s.activeMemberIDs(chatID) {
			if uid != sk.userID && !s.eitherBlocked(uid, sk.userID) {
				s.hub.sendToUser(uid, map[string]any{"type": "typing", "chatId": chatID, "userId": sk.userID, "on": on, "kind": kind}, sendOpts{})
			}
		}
	case "call":
		s.handleCallSignal(sk, msg)
	}
}

// truthy follows Python's bool() for JSON values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case json.Number:
		return x != "0" && x != "0.0"
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}
