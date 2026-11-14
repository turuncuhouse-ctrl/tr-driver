package backup

import (
	"errors"
	"net/http"
	"strconv"

	"necipdrive/internal/httpx"
)

// Handler serves the admin backup API. Router wiring wraps every method with
// RequireAuth + RequireAdmin (browser session, admin role).
type Handler struct {
	service *Service
}

// NewHandler builds the HTTP handler. svc must not be nil.
func NewHandler(svc *Service) *Handler {
	return &Handler{service: svc}
}

// Status answers GET /api/admin/backup/status.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	status, err := h.service.Status(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, status)
}

// Runs answers GET /api/admin/backup/runs?limit=20.
func (h *Handler) Runs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	runs, err := ListRuns(r.Context(), h.service.db, limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, runs)
}

// Run answers POST /api/admin/backup/run. It queues a full backup and returns
// 202 immediately; progress is visible via /status and /runs.
func (h *Handler) Run(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := h.service.RunNow(r.Context(), KindManual)
	if err != nil {
		h.writeRunError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"runId": id, "status": "started"})
}

// Verify answers POST /api/admin/backup/verify. It re-checks the latest
// snapshots (restic check + dump/probe restore) without taking a new backup.
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := h.service.VerifyNow(r.Context())
	if err != nil {
		h.writeRunError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"runId": id, "status": "started"})
}

// Snapshots answers GET /api/admin/backup/snapshots?repo=local|offsite.
func (h *Handler) Snapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	repo := h.service.localRepo()
	switch r.URL.Query().Get("repo") {
	case "", "local":
	case "offsite":
		offsite, ok := h.service.offsiteRepo()
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "offsite replication is not configured")
			return
		}
		repo = offsite
	default:
		httpx.Error(w, http.StatusBadRequest, "repo must be local or offsite")
		return
	}
	snapshots, err := h.service.restic.Snapshots(r.Context(), repo)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	if snapshots == nil {
		snapshots = []Snapshot{}
	}
	httpx.WriteJSON(w, http.StatusOK, snapshots)
}

func (h *Handler) writeRunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrAlreadyRunning):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotConfigured):
		httpx.Error(w, http.StatusServiceUnavailable, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, err.Error())
	}
}
