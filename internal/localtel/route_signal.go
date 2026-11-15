package localtel

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
)

// refuseInvite answers an undeliverable invite straight to the calling client
// (not to the caller's other devices) with a reject, which every released
// client already understands as "call ended".
func (h *Handler) refuseInvite(senderUserID, senderClientID string, msg *SignalMessage, reason string) {
	if msg == nil || senderClientID == "" {
		return
	}
	h.hub.SendToClient(senderUserID, senderClientID, SignalMessage{
		Type:        "reject",
		CallID:      msg.CallID,
		Payload:     reason,
		TimestampMs: nowMs(),
	})
}

func anon(id string) string {
	if id == "" {
		return "-"
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:4])
}

// logInvite records the outcome of one invite without any number, name or
// credential: only short hashes of the participants.
func logInvite(callerID, calleeID string, resolved bool, connected, delivered int) {
	log.Printf("localtel invite caller=%s callee=%s resolved=%t callee_connected=%d delivered=%d",
		anon(callerID), anon(calleeID), resolved, connected, delivered)
}

func (h *Handler) routeSignal(r *http.Request, senderUserID, senderClientID string, msg *SignalMessage) int {
	if msg == nil {
		return 0
	}
	if msg.Type == "invite" {
		if !h.limiter.allow("invite:" + senderUserID) {
			h.refuseInvite(senderUserID, senderClientID, msg, "rate_limited")
			return 0
		}
		targetUserID, ok := h.service.ResolveCallTarget(r.Context(), msg.Number)
		if !ok {
			// The dialed address maps to no TR Driver account. Tell the caller
			// instead of silently dropping the call.
			logInvite(senderUserID, "", false, 0, 0)
			if senderClientID == "" {
				// Legacy HTTP /signal path keeps same-account device fan-out.
				return h.hub.Dispatch(senderUserID, senderClientID, *msg)
			}
			h.refuseInvite(senderUserID, senderClientID, msg, "unknown_target")
			return 0
		}
		if targetUserID == senderUserID {
			logInvite(senderUserID, targetUserID, true, 0, 0)
			h.refuseInvite(senderUserID, senderClientID, msg, "self")
			return 0
		}
		if h.hub.IsBusy(targetUserID) {
			logInvite(senderUserID, targetUserID, true, h.hub.ConnectedCount(targetUserID), 0)
			h.refuseInvite(senderUserID, senderClientID, msg, "busy")
			return 0
		}
		callerAddress, displayName, ok := h.service.CallerIdentity(r.Context(), senderUserID)
		if !ok {
			h.refuseInvite(senderUserID, senderClientID, msg, "caller_unprovisioned")
			return 0
		}
		if !h.hub.BeginCall(msg.CallID, senderUserID, targetUserID) {
			h.refuseInvite(senderUserID, senderClientID, msg, "call_conflict")
			return 0
		}
		// The recipient sees the caller, not the dialed destination. Identity
		// comes from the server-provisioned account rather than client input.
		msg.Number = callerAddress
		msg.From = callerAddress
		msg.DisplayName = displayName
		callID := msg.CallID
		connected := h.hub.ConnectedCount(targetUserID)
		delivered := h.hub.DispatchCall(senderUserID, senderClientID, *msg)
		logInvite(senderUserID, targetUserID, true, connected, delivered)
		if delivered == 0 {
			// Callee has no live gateway connection: end the call for the
			// caller right away rather than ringing into the void.
			h.hub.EndCall(callID)
			h.refuseInvite(senderUserID, senderClientID, msg, "callee_offline")
		}
		return delivered
	}
	if msg.Type == "ping" || msg.Type == "pong" {
		return h.hub.Dispatch(senderUserID, senderClientID, *msg)
	}
	// Call control and media are only routed when this server has an active
	// call mapping and the authenticated sender is one of its participants.
	if msg.Type == "audio" || msg.Type == "accept" || msg.Type == "ringing" || msg.Type == "reject" || msg.Type == "hangup" || msg.Type == "cancel" {
		return h.hub.DispatchCall(senderUserID, senderClientID, *msg)
	}
	return 0
}
