package license

import (
	"net/http"
	"strings"

	"necipdrive/internal/auth"
	"necipdrive/internal/httpx"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) PublicStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	st, err := h.service.Status(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "license status unavailable")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"tier":              st.Tier,
		"maxUsers":          st.MaxUsers,
		"userCount":         st.UserCount,
		"seatsRemaining":    st.SeatsRemaining,
		"canRegisterPublic": st.CanRegisterPublic,
		"catalog":           st.Catalog,
		"product":           "TR Driver",
	})
}

// Admin handles GET status and POST actions on /api/admin/license
// action: activate | request | issue  (default activate for backward compat with {key})
func (h *Handler) Admin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st, err := h.service.Status(r.Context())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "license status unavailable")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, st)
	case http.MethodPost:
		h.adminPost(w, r)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) adminPost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action      string `json:"action"`
		Key         string `json:"key"`
		Tier        string `json:"tier"`
		RequestCode string `json:"requestCode"`
		Years       int    `json:"years"`
		Customer    string `json:"customer"`
		Note        string `json:"note"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		if req.RequestCode != "" {
			action = "issue"
		} else if req.Tier != "" && req.Key == "" {
			action = "request"
		} else {
			action = "activate"
		}
	}

	switch action {
	case "request":
		if req.Tier == "" {
			httpx.Error(w, http.StatusBadRequest, "tier required")
			return
		}
		code, payload, err := h.service.CreateRequest(r.Context(), req.Tier)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"requestCode":  code,
			"request":      payload,
			"instructions": "Bu talep kodunu TR Driver satıcısına gönderin. Size TRD1... yanıt anahtarı iletecek; buraya yapıştırıp etkinleştirin.",
		})
	case "issue":
		if req.RequestCode == "" {
			httpx.Error(w, http.StatusBadRequest, "requestCode required")
			return
		}
		years := req.Years
		if years == 0 {
			years = 1
		}
		key, payload, err := h.service.IssueFromRequest(r.Context(), req.RequestCode, req.Tier, years, req.Customer, req.Note)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"licenseKey": key,
			"request":    payload,
		})
	case "activate":
		if req.Key == "" {
			httpx.Error(w, http.StatusBadRequest, "key required")
			return
		}
		st, err := h.service.Activate(r.Context(), req.Key)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, st)
	default:
		httpx.Error(w, http.StatusBadRequest, "unknown action (use request, activate, or issue)")
	}
}

// AdminPath handles /api/admin/license/{request|issue} for older frontends / bookmarks.
func (h *Handler) AdminPath(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/api/admin/license/")
	sub = strings.Trim(sub, "/")
	switch sub {
	case "", "status":
		h.Admin(w, r)
	case "request":
		if r.Method != http.MethodPost {
			httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Tier string `json:"tier"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil || req.Tier == "" {
			httpx.Error(w, http.StatusBadRequest, "tier required")
			return
		}
		code, payload, err := h.service.CreateRequest(r.Context(), req.Tier)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"requestCode":  code,
			"request":      payload,
			"instructions": "Bu talep kodunu TR Driver satıcısına gönderin.",
		})
	case "issue":
		if r.Method != http.MethodPost {
			httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			RequestCode string `json:"requestCode"`
			Tier        string `json:"tier"`
			Years       int    `json:"years"`
			Customer    string `json:"customer"`
			Note        string `json:"note"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil || req.RequestCode == "" {
			httpx.Error(w, http.StatusBadRequest, "requestCode required")
			return
		}
		years := req.Years
		if years == 0 {
			years = 1
		}
		key, payload, err := h.service.IssueFromRequest(r.Context(), req.RequestCode, req.Tier, years, req.Customer, req.Note)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"licenseKey": key, "request": payload})
	default:
		httpx.Error(w, http.StatusNotFound, "unknown license path")
	}
}

func (h *Handler) RequireAdminSession(next http.Handler) http.Handler {
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
