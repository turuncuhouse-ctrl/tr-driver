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
	// Public: pricing + whether registration is open (no key material).
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
