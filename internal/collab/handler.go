package collab

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

func (h *Handler) Permissions(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	entryID := r.URL.Query().Get("entryId")
	switch r.Method {
	case http.MethodGet:
		if entryID == "" {
			httpx.Error(w, http.StatusBadRequest, "entryId required")
			return
		}
		items, err := h.service.ListPermissions(r.Context(), user.ID, user.Role, entryID)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req struct {
			EntryID string `json:"entryId"`
			Email   string `json:"email"`
			Role    string `json:"role"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		p, err := h.service.Grant(r.Context(), user.ID, user.Role, req.EntryID, req.Email, req.Role)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, p)
	case http.MethodDelete:
		var req struct {
			EntryID       string `json:"entryId"`
			GranteeUserID string `json:"granteeUserId"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		if err := h.service.Revoke(r.Context(), user.ID, user.Role, req.EntryID, req.GranteeUserID); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) SharedWithMe(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	items, err := h.service.SharedWithMe(r.Context(), user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) Starred(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if r.Method == http.MethodGet {
		items, err := h.service.ListStarred(r.Context(), user.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			EntryID string `json:"entryId"`
			Starred bool   `json:"starred"`
		}
		_ = httpx.ReadJSON(r, &req)
		if err := h.service.SetStar(r.Context(), user.ID, user.Role, req.EntryID, req.Starred); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) Recent(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	items, err := h.service.ListRecent(r.Context(), user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	items, err := h.service.Search(r.Context(), user.ID, user.Role, r.URL.Query().Get("q"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) Comments(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	entryID := r.URL.Query().Get("entryId")
	switch r.Method {
	case http.MethodGet:
		items, err := h.service.ListComments(r.Context(), user.ID, user.Role, entryID)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req struct {
			EntryID string `json:"entryId"`
			Body    string `json:"body"`
		}
		_ = httpx.ReadJSON(r, &req)
		c, err := h.service.AddComment(r.Context(), *user, req.EntryID, req.Body)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, c)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) Versions(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	path := strings.TrimPrefix(r.URL.Path, "/api/files/versions")
	path = strings.Trim(path, "/")
	if r.Method == http.MethodGet {
		entryID := r.URL.Query().Get("entryId")
		items, err := h.service.ListVersions(r.Context(), user.ID, user.Role, entryID)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(path, "/restore") {
		var req struct {
			EntryID   string `json:"entryId"`
			VersionID string `json:"versionId"`
		}
		_ = httpx.ReadJSON(r, &req)
		if err := h.service.RestoreVersion(r.Context(), *user, req.EntryID, req.VersionID); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) Activities(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	items, err := h.service.ListActivities(r.Context(), user.ID, 50)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) Notifications(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	switch r.Method {
	case http.MethodGet:
		unread := r.URL.Query().Get("unread") == "1"
		items, err := h.service.ListNotifications(r.Context(), user.ID, unread)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req struct {
			IDs []string `json:"ids"`
		}
		_ = httpx.ReadJSON(r, &req)
		if err := h.service.MarkNotificationsRead(r.Context(), user.ID, req.IDs); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
