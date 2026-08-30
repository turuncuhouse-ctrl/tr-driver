package admin

import (
	"net/http"

	"necipdrive/internal/auth"
	"necipdrive/internal/httpx"
	"necipdrive/internal/loadpace"
)

type Handler struct {
	service      *Service
	pace         *loadpace.Controller
	defaultBatch int64
	chunkBytes   int64
}

func NewHandler(service *Service, pace *loadpace.Controller, defaultBatch, chunkBytes int64) *Handler {
	return &Handler{service: service, pace: pace, defaultBatch: defaultBatch, chunkBytes: chunkBytes}
}

func (h *Handler) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.DeviceIDFromContext(r.Context()) != "" {
			httpx.Error(w, http.StatusForbidden, "admin actions require browser session")
			return
		}
		user := auth.UserFromContext(r.Context())
		if user == nil || user.Role != "admin" {
			httpx.Error(w, http.StatusForbidden, "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	result, err := h.service.Summary(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	result, err := h.service.Users(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) SetPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		UserID   string `json:"userId"`
		PlanCode string `json:"planCode"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := h.service.SetPlan(r.Context(), req.UserID, req.PlanCode); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) SetQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		UserID     string `json:"userId"`
		QuotaBytes int64  `json:"quotaBytes"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := h.service.SetQuota(r.Context(), req.UserID, req.QuotaBytes); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) SetBonusQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		UserID          string `json:"userId"`
		BonusQuotaBytes int64  `json:"bonusQuotaBytes"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := h.service.SetBonusQuota(r.Context(), req.UserID, req.BonusQuotaBytes); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) SetRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		UserID string `json:"userId"`
		Role   string `json:"role"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	actor := auth.UserFromContext(r.Context())
	if err := h.service.SetRole(r.Context(), actor.ID, req.UserID, req.Role); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		result, err := h.service.Settings(r.Context(), h.defaultBatch, h.chunkBytes)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, result)
	case http.MethodPost:
		var req struct {
			MaxUploadBatchBytes *int64 `json:"maxUploadBatchBytes"`
			DefaultQuotaBytes   *int64 `json:"defaultQuotaBytes"`
			MatchDiskCapacity   bool   `json:"matchDiskCapacity"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		if req.MatchDiskCapacity {
			n, err := h.service.MatchDefaultQuotaToDisk(r.Context())
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "updated", "defaultQuotaBytes": n})
			return
		}
		if req.MaxUploadBatchBytes != nil {
			if err := h.service.SetMaxUploadBatchBytes(r.Context(), *req.MaxUploadBatchBytes); err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if req.DefaultQuotaBytes != nil {
			if err := h.service.SetDefaultQuotaBytes(r.Context(), *req.DefaultQuotaBytes); err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) UploadPace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.pace == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "upload pace unavailable")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.pace.Snapshot())
}

func (h *Handler) Devices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	result, err := h.service.Devices(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}
