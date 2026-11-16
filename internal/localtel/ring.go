package localtel

import (
	"encoding/json"
	"log"
	"time"
)

// Ring behaviour mirrors a GSM tower: the invite is re-sent to the callee's
// devices while the phone should be ringing, and after the deadline the call
// is over for both sides — the caller learns "no answer" and the callee gets
// a missed-call signal (delivered live and queued for later reconnects).
const (
	defaultRingRetryInterval = 6 * time.Second
	defaultRingTimeout       = 45 * time.Second

	maxMissedPerUser  = 20
	missedEntryTTL    = 24 * time.Hour
	maxMissedUsers    = 2000
	maxMissedPerFlush = 10
)

type missedEntry struct {
	msg SignalMessage
	at  time.Time
}

// StartRing arms the retry ticker and the no-answer deadline for an invite
// that was handed to the callee. Safe to call once per call; later signals
// (ringing/accept/reject…) stop or short-circuit the timers.
func (h *Hub) StartRing(callID, senderClientID string, invite SignalMessage) {
	if callID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	route, ok := h.calls[callID]
	if !ok || route.accepted {
		return
	}
	stored := invite
	route.senderClientID = senderClientID
	route.invite = &stored
	retryEvery := h.ringRetryInterval
	if retryEvery <= 0 {
		retryEvery = defaultRingRetryInterval
	}
	route.retryTimer = time.AfterFunc(retryEvery, func() { h.ringRetry(callID) })
	route.deadlineTimer = time.AfterFunc(h.ringTimeoutDuration(), func() { h.handleRingTimeout(callID) })
	h.calls[callID] = route
}

func (h *Hub) ringTimeoutDuration() time.Duration {
	if h.ringTimeout > 0 {
		return h.ringTimeout
	}
	return defaultRingTimeout
}

// ringRetry re-sends the stored invite to every device of the callee. It runs
// on its own timer goroutine and only ever carries the same sanitized signal
// the first dispatch carried (caller identity, no credentials).
func (h *Hub) ringRetry(callID string) {
	h.mu.Lock()
	route, ok := h.calls[callID]
	if !ok || route.accepted || route.invite == nil {
		h.mu.Unlock()
		return
	}
	raw, err := json.Marshal(*route.invite)
	if err != nil {
		h.mu.Unlock()
		return
	}
	// Deliver to all of the callee's devices (the sender id belongs to the
	// caller and would never match here anyway).
	delivered := dispatchLocked(h.users[route.calleeUserID], "", raw)
	route.retryAttempt++
	attempt := route.retryAttempt
	retryEvery := h.ringRetryInterval
	if retryEvery <= 0 {
		retryEvery = defaultRingRetryInterval
	}
	// Re-arm; the deadline timer stops us once the call times out.
	route.retryTimer = time.AfterFunc(retryEvery, func() { h.ringRetry(callID) })
	h.calls[callID] = route
	h.mu.Unlock()
	// call ids are client-generated random strings; no number or name here.
	log.Printf("localtel ring retry call=%s attempt=%d delivered=%d", anon(callID), attempt, delivered)
}

// ringTimeout ends an unanswered call: the caller receives reject/no_answer,
// every callee device receives a missed signal (live if connected) and the
// missed call is queued so a callee that connects later still learns about it.
func (h *Hub) handleRingTimeout(callID string) {
	h.mu.Lock()
	route, ok := h.calls[callID]
	if !ok || route.accepted {
		h.mu.Unlock()
		return
	}
	delete(h.calls, callID)
	route.stopRingTimersLocked()
	now := nowMs()
	callerRaw, _ := json.Marshal(SignalMessage{
		Type:        "reject",
		CallID:      callID,
		Payload:     "no_answer",
		TimestampMs: now,
	})
	dispatchLocked(h.users[route.callerUserID], "", callerRaw)

	number, name := "", ""
	if route.invite != nil {
		number, name = route.invite.Number, route.invite.DisplayName
	}
	missed := SignalMessage{
		Type:        "missed",
		CallID:      callID,
		Number:      number,
		DisplayName: name,
		TimestampMs: now,
	}
	if missedRaw, err := json.Marshal(missed); err == nil {
		dispatchLocked(h.users[route.calleeUserID], "", missedRaw)
	}
	h.queueMissedLocked(route.calleeUserID, missed)
	callerConnected := len(h.users[route.callerUserID])
	calleeConnected := len(h.users[route.calleeUserID])
	h.mu.Unlock()
	log.Printf("localtel ring timeout call=%s caller_connected=%d callee_connected=%d",
		anon(callID), callerConnected, calleeConnected)
}

// queueMissedLocked keeps a bounded, per-user missed-call backlog so a phone
// that was offline at ring time still shows the call when it reconnects.
func (h *Hub) queueMissedLocked(userID string, msg SignalMessage) {
	if userID == "" {
		return
	}
	if h.missed == nil {
		h.missed = make(map[string][]missedEntry)
	}
	if len(h.missed) >= maxMissedUsers {
		if _, ok := h.missed[userID]; !ok {
			// Capacity reached with other users queued: drop the oldest user
			// wholesale rather than growing without bound.
			for old := range h.missed {
				delete(h.missed, old)
				break
			}
		}
	}
	now := time.Now()
	entries := h.missed[userID]
	fresh := entries[:0]
	for _, entry := range entries {
		if now.Sub(entry.at) < missedEntryTTL {
			fresh = append(fresh, entry)
		}
	}
	fresh = append(fresh, missedEntry{msg: msg, at: now})
	if len(fresh) > maxMissedPerUser {
		fresh = fresh[len(fresh)-maxMissedPerUser:]
	}
	h.missed[userID] = fresh
}

// flushMissedLocked hands the queued missed calls to a freshly connected
// client and clears them (at-most-once; the Android notification id is stable
// per call, so a duplicate would only refresh the same notification).
func (h *Hub) flushMissedLocked(userID string, client *Client) []SignalMessage {
	entries := h.missed[userID]
	if len(entries) == 0 {
		return nil
	}
	sent := make([]SignalMessage, 0, len(entries))
	for i, entry := range entries {
		if i >= maxMissedPerFlush {
			break
		}
		raw, err := json.Marshal(entry.msg)
		if err != nil {
			continue
		}
		select {
		case client.send <- raw:
			sent = append(sent, entry.msg)
		default:
			// Client buffer full: keep the remainder queued.
			h.missed[userID] = entries[i:]
			return sent
		}
	}
	delete(h.missed, userID)
	return sent
}

// stopRingTimersLocked cancels pending retry/deadline timers of a route.
// Timer callbacks that already started will find the route gone or accepted.
func (r *callRoute) stopRingTimersLocked() {
	if r.retryTimer != nil {
		r.retryTimer.Stop()
		r.retryTimer = nil
	}
	if r.deadlineTimer != nil {
		r.deadlineTimer.Stop()
		r.deadlineTimer = nil
	}
}
