package personalbackup

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"necipdrive/internal/auth"
	"necipdrive/internal/httpx"
)

// BasePath is registered as a prefix route; every endpoint lives below it.
const BasePath = "/api/personal-backup/"

// Rate limit for writes: a burst of 20 batches, then one batch per second.
const (
	writeBurstPerMinute = 240
	writeBurst          = 20
)

type Handler struct {
	service *Service
	limiter *RateLimiter
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service, limiter: NewRateLimiter(writeBurstPerMinute, writeBurst)}
}

// ServeHTTP makes the handler usable directly behind auth middleware.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.Root(w, r)
}

// Root dispatches /api/personal-backup/{status,contacts,sms}.
func (h *Handler) Root(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil || user.ID == "" {
		httpx.Error(w, http.StatusUnauthorized, "login required")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, BasePath)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	if parts[0] == "status" {
		if len(parts) != 1 || r.Method != http.MethodGet {
			httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.status(w, r, user.ID)
		return
	}
	kind, err := ParseKind(parts[0])
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "unknown backup kind")
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.list(w, r, user.ID, kind)
	case http.MethodPost:
		if !h.limiter.Allow(user.ID) {
			w.Header().Set("Retry-After", "60")
			httpx.Error(w, http.StatusTooManyRequests, "too many backup requests, retry later")
			return
		}
		h.sync(w, r, user.ID, kind)
	case http.MethodDelete:
		if !h.limiter.Allow(user.ID) {
			w.Header().Set("Retry-After", "60")
			httpx.Error(w, http.StatusTooManyRequests, "too many backup requests, retry later")
			return
		}
		h.purge(w, r, user.ID, kind)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request, userID string) {
	status, err := h.service.Status(r.Context(), userID)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, status)
}

func (h *Handler) sync(w http.ResponseWriter, r *http.Request, userID string, kind Kind) {
	if r.ContentLength > MaxRequestBytes {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	var req SyncRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		// Never echo the decoder error: it can quote the offending JSON.
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	deviceID := auth.DeviceIDFromContext(r.Context())
	if strings.TrimSpace(req.DeviceID) == "" {
		req.DeviceID = deviceID
	}
	result, err := h.service.Sync(r.Context(), userID, kind, &req)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, userID string, kind Kind) {
	query := r.URL.Query()
	opts := ListOptions{After: query.Get("after")}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid limit")
			return
		}
		opts.Limit = limit
	}
	if raw := strings.TrimSpace(query.Get("since")); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid since (RFC3339 required)")
			return
		}
		opts.Since = &since
	}
	page, err := h.service.List(r.Context(), userID, kind, opts)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

func (h *Handler) purge(w http.ResponseWriter, r *http.Request, userID string, kind Kind) {
	deleted, err := h.service.Purge(r.Context(), userID, kind)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "deleted": deleted})
}

// fail maps service errors to responses without leaking SQL text or row
// contents. Only the validation marker is treated as a client error.
func (h *Handler) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrInvalidInput) {
		httpx.Error(w, http.StatusBadRequest, "invalid backup payload")
		return
	}
	httpx.Error(w, http.StatusInternalServerError, "backup operation failed")
}
