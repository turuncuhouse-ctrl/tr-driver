package drives

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

func (h *Handler) ListOrCreate(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	switch r.Method {
	case http.MethodGet:
		items, err := h.service.List(r.Context(), user.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		drive, err := h.service.CreateShared(r.Context(), *user, req.Name)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, drive)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) Drive(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	path := strings.TrimPrefix(r.URL.Path, "/api/drives/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httpx.Error(w, http.StatusBadRequest, "drive id required")
		return
	}
	driveID := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			d, err := h.service.Get(r.Context(), user.ID, user.Role, driveID)
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			httpx.WriteJSON(w, http.StatusOK, d)
		case http.MethodPatch:
			var req struct {
				Name string `json:"name"`
			}
			_ = httpx.ReadJSON(r, &req)
			if err := h.service.Rename(r.Context(), user.ID, user.Role, driveID, req.Name); err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		case http.MethodDelete:
			if err := h.service.Delete(r.Context(), user.ID, user.Role, driveID); err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	if parts[1] == "members" {
		h.members(w, r, user.ID, user.Role, driveID, parts[2:])
		return
	}
	http.NotFound(w, r)
}

func (h *Handler) members(w http.ResponseWriter, r *http.Request, userID, role, driveID string, rest []string) {
	if len(rest) == 0 || rest[0] == "" {
		switch r.Method {
		case http.MethodGet:
			items, err := h.service.ListMembers(r.Context(), userID, role, driveID)
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			httpx.WriteJSON(w, http.StatusOK, items)
		case http.MethodPost:
			var req struct {
				Email string `json:"email"`
				Role  string `json:"role"`
			}
			if err := httpx.ReadJSON(r, &req); err != nil {
				httpx.Error(w, http.StatusBadRequest, "invalid request")
				return
			}
			if err := h.service.AddMember(r.Context(), userID, role, driveID, req.Email, req.Role); err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			httpx.WriteJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
		default:
			httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	memberID := rest[0]
	if r.Method != http.MethodDelete {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := h.service.RemoveMember(r.Context(), userID, role, driveID, memberID); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
