package localtel

import (
	"encoding/json"
	"time"
)

const (
	callRouteTTL        = 2 * time.Hour
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
	delivered := dispatchLocked(h.users[targetUserID], senderClientID, raw)
	if msg.Type == "reject" || msg.Type == "hangup" || msg.Type == "cancel" {
		delete(h.calls, msg.CallID)
	}
	return delivered
}

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
