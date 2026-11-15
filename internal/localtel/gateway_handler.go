package localtel

import (
	"net"
	"net/http"
	"strings"

	"necipdrive/internal/httpx"
)

// ServeGatewayWS authenticates a short-lived LocalTel token on the same-origin
// WSS path. Long-lived device tokens remain accepted only on the authenticated
// /api/localtel/ws endpoint.
func (h *Handler) ServeGatewayWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limiter.allow("gateway-ws-ip:" + clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		httpx.Error(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authHeader) < len("Bearer ") || !strings.EqualFold(authHeader[:len("Bearer ")], "Bearer ") {
		httpx.Error(w, http.StatusUnauthorized, "login required")
		return
	}
	userID, _, err := h.service.ValidateToken(r.Context(), strings.TrimSpace(authHeader[len("Bearer "):]))
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid or expired gateway token")
		return
	}
	if !h.limiter.allow("gateway-ws-user:" + userID) {
		w.Header().Set("Retry-After", "60")
		httpx.Error(w, http.StatusTooManyRequests, "too many gateway connections")
		return
	}
	h.handleWS(w, r, userID, "")
}

func clientIP(r *http.Request) string {
	// Do not trust X-Forwarded-For here: it could let clients bypass the
	// connection throttle. Per-user signaling has its own limiter as well.
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
