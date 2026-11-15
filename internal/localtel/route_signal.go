package localtel

import "net/http"

func (h *Handler) routeSignal(r *http.Request, senderUserID, senderClientID string, msg *SignalMessage) int {
	if msg == nil {
		return 0
	}
	if msg.Type == "invite" {
		if !h.limiter.allow("invite:" + senderUserID) {
			return 0
		}
		targetUserID, ok := h.service.ResolveCallTarget(r.Context(), msg.Number)
		if !ok {
			// Preserve same-account device fan-out for legacy external/SIP
			// destinations that are not provisioned TR Driver users.
			return h.hub.Dispatch(senderUserID, senderClientID, *msg)
		}
		callerAddress, displayName, ok := h.service.CallerIdentity(r.Context(), senderUserID)
		if !ok {
			return 0
		}
		if !h.hub.BeginCall(msg.CallID, senderUserID, targetUserID) {
			return 0
		}
		// The recipient sees the caller, not the dialed destination. Identity
		// comes from the server-provisioned account rather than client input.
		msg.Number = callerAddress
		msg.From = callerAddress
		msg.DisplayName = displayName
		return h.hub.DispatchCall(senderUserID, senderClientID, *msg)
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
