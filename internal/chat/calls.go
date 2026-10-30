package chat

import (
	"encoding/json"
	"time"
)

// Call signalling arrives over the WebSocket. Media flows browser to
// browser over WebRTC, directly across the tailnet.

type call struct {
	chatID       string
	video        bool
	participants map[string]string // device id -> user id, in the call now
	order        []string          // device ids in the order they joined
	invited      map[string]bool   // user ids still ringing
}

func (c *call) peers() []map[string]string {
	out := []map[string]string{}
	for _, d := range c.order {
		if u, ok := c.participants[d]; ok {
			out = append(out, map[string]string{"deviceId": d, "userId": u})
		}
	}
	return out
}

func (c *call) join(deviceID, userID string) {
	if _, ok := c.participants[deviceID]; !ok {
		c.order = append(c.order, deviceID)
	}
	c.participants[deviceID] = userID
}

func (c *call) leave(deviceID string) {
	delete(c.participants, deviceID)
	for i, d := range c.order {
		if d == deviceID {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func sendJSON(sk *socket, v any) {
	data, err := json.Marshal(v)
	if err == nil {
		sk.send(data)
	}
}

func (s *Server) handleCallSignal(sk *socket, msg map[string]any) {
	callID, _ := msg["callId"].(string)
	if callID == "" {
		return
	}
	if len(callID) > 40 {
		callID = callID[:40]
	}
	userID := sk.userID
	action, _ := msg["action"].(string)

	if action == "ring" {
		chatID, _ := msg["chatId"].(string)
		if s.activeMember(chatID, userID) == nil {
			return
		}
		if _, busy := s.calls[callID]; busy {
			return
		}
		chat := s.getChat(chatID)
		if adminPosts(chat.Type) {
			sendJSON(sk, map[string]any{"type": "call", "action": "error", "callId": callID, "message": "Calls aren't available in channels and community announcements"})
			return
		}
		var others []string
		for _, u := range s.activeMemberIDs(chatID) {
			if s.isRadioUser(u) {
				sendJSON(sk, map[string]any{"type": "call", "action": "error", "callId": callID, "message": "Radio contacts can't take calls: the radio only carries text"})
				return
			}
			if u != userID && !s.eitherBlocked(u, userID) {
				others = append(others, u)
			}
		}
		if chat.Type == "group" && len(others) > 7 {
			sendJSON(sk, map[string]any{"type": "call", "action": "error", "callId": callID, "message": "Group calls are limited to 8 people"})
			return
		}
		video := truthy(msg["video"])
		c := &call{chatID: chatID, video: video, participants: map[string]string{}, invited: map[string]bool{}}
		c.join(sk.deviceID, userID)
		for _, u := range others {
			c.invited[u] = true
		}
		s.calls[callID] = c
		s.db.run("INSERT INTO calls (id, chat_id, caller_id, video, started_at, participants) VALUES (?, ?, ?, ?, ?, ?)",
			callID, chatID, userID, video, now(), mustJSON([]string{userID}))
		s.hub.sendToUsers(others, map[string]any{"type": "call", "action": "ring", "callId": callID, "chatId": chatID, "from": userID, "video": video}, sendOpts{})
		// Unanswered calls stop ringing after 45 seconds.
		time.AfterFunc(45*time.Second, func() {
			s.locked(func() {
				c := s.calls[callID]
				var answered *int64
				found := s.db.scalar(&answered, "SELECT answered_at FROM calls WHERE id = ?", callID)
				if c != nil && len(c.participants) <= 1 && found && answered == nil {
					s.endCall(callID, "missed")
				}
			})
		})
		return
	}

	c := s.calls[callID]
	if c == nil {
		sendJSON(sk, map[string]any{"type": "call", "action": "ended", "callId": callID})
		return
	}

	switch action {
	case "accept":
		inCall := false
		for _, u := range c.participants {
			if u == userID {
				inCall = true
			}
		}
		if !c.invited[userID] && !inCall {
			return
		}
		existing := c.peers()
		c.join(sk.deviceID, userID)
		var people []string
		var raw string
		s.db.scalar(&raw, "SELECT participants FROM calls WHERE id = ?", callID)
		people = uniq(append(jsonList(raw), userID))
		s.db.run("UPDATE calls SET answered_at = COALESCE(answered_at, ?), participants = ? WHERE id = ?", now(), mustJSON(people), callID)
		// The newcomer sends an offer to everyone already in the call.
		sendJSON(sk, map[string]any{"type": "call", "action": "joined", "callId": callID, "peers": existing, "video": c.video})
		for _, p := range existing {
			s.hub.sendToUser(p["userId"], map[string]any{"type": "call", "action": "peer-joined", "callId": callID, "userId": userID, "deviceId": sk.deviceID},
				sendOpts{onlyDevice: p["deviceId"]})
		}
		// Stop ringing on the account's other browsers.
		s.hub.sendToUser(userID, map[string]any{"type": "call", "action": "answered-elsewhere", "callId": callID}, sendOpts{exceptDevice: sk.deviceID})
		return
	case "decline":
		delete(c.invited, userID)
		s.hub.sendToUser(userID, map[string]any{"type": "call", "action": "answered-elsewhere", "callId": callID}, sendOpts{exceptDevice: sk.deviceID})
		for _, p := range c.peers() {
			s.hub.sendToUser(p["userId"], map[string]any{"type": "call", "action": "declined", "callId": callID, "userId": userID}, sendOpts{onlyDevice: p["deviceId"]})
		}
		if len(c.invited) == 0 && len(c.participants) <= 1 {
			s.endCall(callID, "declined")
		}
		return
	}

	if _, in := c.participants[sk.deviceID]; !in {
		return
	}
	switch action {
	case "signal":
		toDevice, _ := msg["toDevice"].(string)
		target, ok := c.participants[toDevice]
		data, isObj := msg["data"].(map[string]any)
		if !ok || !isObj {
			return
		}
		s.hub.sendToUser(target, map[string]any{"type": "call", "action": "signal", "callId": callID, "fromDevice": sk.deviceID, "from": userID, "data": data},
			sendOpts{onlyDevice: toDevice})
	case "hangup":
		s.leaveCall(callID, sk.deviceID)
	}
}

func (s *Server) leaveCall(callID, deviceID string) {
	c := s.calls[callID]
	if c == nil {
		return
	}
	c.leave(deviceID)
	for _, p := range c.peers() {
		s.hub.sendToUser(p["userId"], map[string]any{"type": "call", "action": "peer-left", "callId": callID, "deviceId": deviceID}, sendOpts{onlyDevice: p["deviceId"]})
	}
	if len(c.participants) <= 1 {
		s.endCall(callID, "ended")
	}
}

func (s *Server) endCall(callID, reason string) {
	c := s.calls[callID]
	if c == nil {
		return
	}
	delete(s.calls, callID)
	s.db.run("UPDATE calls SET ended_at = ? WHERE id = ?", now(), callID)
	s.hub.sendToUsers(s.activeMemberIDs(c.chatID), map[string]any{"type": "call", "action": "ended", "callId": callID, "reason": reason}, sendOpts{})
}

func (s *Server) dropDeviceFromCalls(deviceID string) {
	for id, c := range s.calls {
		if _, in := c.participants[deviceID]; in {
			s.leaveCall(id, deviceID)
		}
	}
}
