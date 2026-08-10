package license

import (
	"net/http"

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

func (h *Handler) AdminStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	st, err := h.service.Status(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "license status unavailable")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (h *Handler) Activate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil || req.Key == "" {
		httpx.Error(w, http.StatusBadRequest, "key required")
		return
	}
	st, err := h.service.Activate(r.Context(), req.Key)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (h *Handler) CreateRequest(w http.ResponseWriter, r *http.Request) {
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
		"requestCode": code,
		"request":     payload,
		"instructions": "Bu talep kodunu TR Driver satıcısına gönderin. Size TRD1... yanıt anahtarı iletecek; buraya yapıştırıp etkinleştirin.",
	})
}

func (h *Handler) Issue(w http.ResponseWriter, r *http.Request) {
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
	if req.Years == 0 {
		req.Years = 1
	}
	key, payload, err := h.service.IssueFromRequest(r.Context(), req.RequestCode, req.Tier, req.Years, req.Customer, req.Note)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"licenseKey": key,
		"request":    payload,
	})
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
