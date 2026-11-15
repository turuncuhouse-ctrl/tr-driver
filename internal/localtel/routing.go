package localtel

import (
	"encoding/json"
	"time"
)

const (
	callRouteTTL        = 4 * time.Hour
	ringingWindow       = 90 * time.Second
	maxActiveCallRoutes = 10_000
)

// BeginCall records a short-lived, per-call route. A call ID is globally unique
// in the hub; a conflicting ID cannot be rebound to different users.
func (h *Hub) BeginCall(callID, callerUserID, calleeUserID string) bool {
	if callID == "" || callerUserID == "" || calleeUserID == "" {
		return false
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, route := range h.calls {
		if now.After(route.expiresAt) {
			delete(h.calls, id)
		}
	}
	if existing, ok := h.calls[callID]; ok {
		return existing.callerUserID == callerUserID && existing.calleeUserID == calleeUserID
	}
	if len(h.calls) >= maxActiveCallRoutes {
		return false
	}
	h.calls[callID] = callRoute{
		callerUserID: callerUserID,
		calleeUserID: calleeUserID,
		expiresAt:    now.Add(callRouteTTL),
		ringingUntil: now.Add(ringingWindow),
	}
	return true
}

// DispatchCall authorizes sender as one of the two call participants and
// forwards the signal only to the other participant. Terminal signals expire
// the route after delivery.
func (h *Hub) DispatchCall(senderUserID, senderClientID string, msg SignalMessage) int {
	raw, err := json.Marshal(msg)
	if err != nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	route, ok := h.calls[msg.CallID]
	if !ok || time.Now().After(route.expiresAt) {
		delete(h.calls, msg.CallID)
		return 0
	}
	targetUserID := ""
	switch senderUserID {
	case route.callerUserID:
		targetUserID = route.calleeUserID
	case route.calleeUserID:
		targetUserID = route.callerUserID
	default:
		return 0
	}
	if msg.Type == "accept" && senderUserID == route.calleeUserID {
		route.accepted = true
		h.calls[msg.CallID] = route
	}
	delivered := dispatchLocked(h.users[targetUserID], senderClientID, raw)
	if msg.Type == "reject" || msg.Type == "hangup" || msg.Type == "cancel" {
		delete(h.calls, msg.CallID)
	}
	return delivered
}

// IsBusy reports whether userID is in an answered call or has a fresh,
// still-ringing one. Stale routes (expired, or unanswered past the ringing
// window) never count.
func (h *Hub) IsBusy(userID string) bool {
	now := time.Now()
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, r := range h.calls {
		if r.callerUserID != userID && r.calleeUserID != userID {
			continue
		}
		if now.After(r.expiresAt) {
			continue
		}
		if r.accepted || now.Before(r.ringingUntil) {
			return true
		}
	}
	return false
}

// dropRoutesOfLocked removes every call route of a user whose last gateway
// connection just closed and tells the other party the call is over.
func (h *Hub) dropRoutesOfLocked(userID string) {
	for id, r := range h.calls {
		if r.callerUserID != userID && r.calleeUserID != userID {
			continue
		}
		other := r.calleeUserID
		if other == userID {
			other = r.callerUserID
		}
		if raw, err := json.Marshal(SignalMessage{Type: "hangup", CallID: id, Payload: "peer_disconnected", TimestampMs: nowMs()}); err == nil {
			dispatchLocked(h.users[other], "", raw)
		}
		delete(h.calls, id)
	}
}

// EndCall drops a call route (e.g. when the invite could not be delivered).
func (h *Hub) EndCall(callID string) {
	h.mu.Lock()
	delete(h.calls, callID)
	h.mu.Unlock()
}

// SendToClient delivers msg to one specific connected client of userID.
func (h *Hub) SendToClient(userID, clientID string, msg SignalMessage) bool {
	raw, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.users[userID][clientID]
	if !ok {
		return false
	}
	select {
	case c.send <- raw:
		return true
	default:
		return false
	}
}

func nowMs() int64 { return time.Now().UTC().UnixMilli() }

func dispatchLocked(byUser map[string]*Client, senderClientID string, raw []byte) int {
	delivered := 0
	for id, client := range byUser {
		if senderClientID != "" && id == senderClientID {
			continue
		}
		select {
		case client.send <- raw:
			delivered++
		default:
		}
	}
	return delivered
}
