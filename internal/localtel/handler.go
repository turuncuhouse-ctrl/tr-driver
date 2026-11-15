package localtel

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
	"necipdrive/internal/auth"
	"necipdrive/internal/httpx"
)

// BasePath is mounted behind RequireAuth in internal/app/router.go.
const BasePath = "/api/localtel/"

type Handler struct {
	service        *Service
	hub            *Hub
	limiter        *rateLimiter
	trustedProxies []netip.Prefix
}

func NewHandler(service *Service, hub *Hub, trustedProxies ...[]netip.Prefix) *Handler {
	if hub == nil {
		hub = NewHub()
	}
	var trusted []netip.Prefix
	if len(trustedProxies) > 0 {
		trusted = trustedProxies[0]
	}
	return &Handler{
		service:        service,
		hub:            hub,
		limiter:        newRateLimiter(120, 20),
		trustedProxies: trusted,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil || user.ID == "" {
		httpx.Error(w, http.StatusUnauthorized, "login required")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, BasePath), "/")
	switch rest {
	case "config":
		h.handleConfig(w, r, user.ID)
	case "config/provision":
		if r.Method != http.MethodGet {
			httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleProvision(w, r, user.ID)
	case "phone":
		h.handlePhone(w, r, user.ID)
	case "signal":
		if r.Method != http.MethodPost {
			httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleSignal(w, r, user.ID, auth.DeviceIDFromContext(r.Context()))
	case "ws":
		if r.Method != http.MethodGet {
			httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleWS(w, r, user.ID, auth.DeviceIDFromContext(r.Context()))
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request, userID string) {
	switch r.Method {
	case http.MethodGet:
		if !h.limiter.allow("cfg:" + userID) {
			w.Header().Set("Retry-After", "60")
			httpx.Error(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		w.Header().Set("Cache-Control", "no-store, private")
		w.Header().Set("Pragma", "no-cache")
		view, err := h.service.GetGatewayConfig(r.Context(), userID, r.Host)
		if err != nil {
			h.fail(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	case http.MethodPut:
		if !h.limiter.allow(userID) {
			w.Header().Set("Retry-After", "60")
			httpx.Error(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		if r.ContentLength > maxConfigBodyBytes {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxConfigBodyBytes)
		var in ConfigInput
		if err := httpx.ReadJSON(r, &in); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				httpx.Error(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		view, err := h.service.PutConfig(r.Context(), userID, in)
		if err != nil {
			h.fail(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	case http.MethodDelete:
		if !h.limiter.allow(userID) {
			w.Header().Set("Retry-After", "60")
			httpx.Error(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		if err := h.service.DeleteConfig(r.Context(), userID); err != nil {
			h.fail(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handlePhone(w http.ResponseWriter, r *http.Request, userID string) {
	if !h.limiter.allow("phone:" + userID) {
		w.Header().Set("Retry-After", "60")
		httpx.Error(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	switch r.Method {
	case http.MethodGet:
		view, err := h.service.GetPhone(r.Context(), userID)
		if err != nil {
			h.fail(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var in struct {
			Phone string `json:"phone"`
		}
		if err := httpx.ReadJSON(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		view, err := h.service.SetPhone(r.Context(), userID, in.Phone)
		if err != nil {
			if errors.Is(err, ErrInvalidPhone) {
				httpx.Error(w, http.StatusBadRequest, "geçersiz telefon numarası")
				return
			}
			h.fail(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleProvision(w http.ResponseWriter, r *http.Request, userID string) {
	w.Header().Set("Pragma", "no-cache")
	prov, err := h.service.GetProvision(r.Context(), userID)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, prov)
}

func (h *Handler) handleSignal(w http.ResponseWriter, r *http.Request, userID, deviceID string) {
	if !h.limiter.allow("sig:" + userID) {
		w.Header().Set("Retry-After", "60")
		httpx.Error(w, http.StatusTooManyRequests, "too many signaling requests")
		return
	}
	if r.ContentLength > maxSignalPayloadBytes+2048 {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "signal too large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSignalPayloadBytes+2048)
	var msg SignalMessage
	if err := httpx.ReadJSON(r, &msg); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid signal")
		return
	}
	if msg.DeviceID == "" {
		msg.DeviceID = deviceID
	}
	if err := ValidateSignal(&msg); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid signal")
		return
	}
	delivered := h.routeSignal(r, userID, "", &msg)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":    "dispatched",
		"delivered": delivered,
	})
}

func (h *Handler) handleWS(w http.ResponseWriter, r *http.Request, userID, deviceID string) {
	srv := websocket.Server{
		Handshake: func(_ *websocket.Config, req *http.Request) error {
			origin := strings.TrimSpace(req.Header.Get("Origin"))
			if origin == "" {
				return nil
			}
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, req.Host) {
				return errors.New("origin mismatch")
			}
			return nil
		},
		Handler: func(ws *websocket.Conn) {
			ws.MaxPayloadBytes = maxSignalPayloadBytes + 4096
			// No per-write deadline: x/net/websocket answers protocol pings with a
			// pong write on this same connection, and an already-expired deadline
			// would kill the socket (~50s) and force constant reconnects. A stuck
			// peer is dropped by closing the conn when the read loop ends.
			client := h.hub.Register(userID, deviceID)
			defer h.hub.Unregister(client)

			done := make(chan struct{})
			go func() {
				defer close(done)
				for raw := range client.SendChannel() {
					if err := websocket.Message.Send(ws, string(raw)); err != nil {
						return
					}
				}
			}()

			for {
				var text string
				if err := websocket.Message.Receive(ws, &text); err != nil {
					break
				}
				var msg SignalMessage
				if err := json.Unmarshal([]byte(text), &msg); err != nil {
					continue
				}
				if msg.DeviceID == "" {
					msg.DeviceID = deviceID
				}
				if err := ValidateSignal(&msg); err != nil {
					continue
				}
				if msg.Type == "ping" {
					pong, _ := json.Marshal(SignalMessage{
						Type:        "pong",
						CallID:      msg.CallID,
						TimestampMs: time.Now().UTC().UnixMilli(),
					})
					if err := websocket.Message.Send(ws, string(pong)); err != nil {
						break
					}
					continue
				}
				h.routeSignal(r, userID, client.id, &msg)
			}
			_ = ws.Close()
			h.hub.Unregister(client)
			<-done
		},
	}
	srv.ServeHTTP(w, r)
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		httpx.Error(w, http.StatusBadRequest, "invalid localtel configuration")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "localtel configuration not found")
	default:
		httpx.Error(w, http.StatusInternalServerError, "localtel operation failed")
	}
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
	now     func() time.Time
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

func newRateLimiter(perMinute, burst int) *rateLimiter {
	return &rateLimiter{
		buckets: make(map[string]*bucket),
		rate:    float64(perMinute) / 60.0,
		burst:   float64(burst),
		now:     time.Now,
	}
}

func (l *rateLimiter) allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= 4096 {
			for k, existing := range l.buckets {
				if now.Sub(existing.lastSeen) > 10*time.Minute {
					delete(l.buckets, k)
				}
			}
		}
		b = &bucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.lastSeen).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
	}
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
