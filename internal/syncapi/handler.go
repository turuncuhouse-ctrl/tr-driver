package syncapi

import (
	"net/http"
	"strconv"

	"necipdrive/internal/auth"
	"necipdrive/internal/httpx"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) Snapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user := auth.UserFromContext(r.Context())
	cursor, entries, err := h.service.Snapshot(r.Context(), user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"cursor": cursor, "entries": entries})
}

func (h *Handler) Changes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	user := auth.UserFromContext(r.Context())
	changes, next, err := h.service.Changes(r.Context(), user.ID, cursor, limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"changes": changes, "nextCursor": next})
}

func (h *Handler) Folder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	user := auth.UserFromContext(r.Context())
	if req.ParentID == "" {
		req.ParentID = user.StorageRootID
	}
	entry, err := h.service.CreateFolder(r.Context(), user.ID, user.Role, req.ParentID, req.Name, auth.DeviceIDFromContext(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, entry)
}

func (h *Handler) Rename(w http.ResponseWriter, r *http.Request) {
	h.mutation(w, r, func(userID, role, deviceID string, req mutationRequest) error {
		return h.service.Rename(r.Context(), userID, role, req.FileID, req.Name, deviceID, req.ExpectedVersion)
	})
}
func (h *Handler) Move(w http.ResponseWriter, r *http.Request) {
	h.mutation(w, r, func(userID, role, deviceID string, req mutationRequest) error {
		return h.service.Move(r.Context(), userID, role, req.FileID, req.ParentID, deviceID, req.ExpectedVersion)
	})
}
func (h *Handler) Trash(w http.ResponseWriter, r *http.Request) {
	h.mutation(w, r, func(userID, role, deviceID string, req mutationRequest) error {
		return h.service.Trash(r.Context(), userID, role, req.FileID, deviceID)
	})
}
func (h *Handler) Restore(w http.ResponseWriter, r *http.Request) {
	h.mutation(w, r, func(userID, role, deviceID string, req mutationRequest) error {
		return h.service.Restore(r.Context(), userID, role, req.FileID, deviceID)
	})
}
func (h *Handler) Purge(w http.ResponseWriter, r *http.Request) {
	h.mutation(w, r, func(userID, role, deviceID string, req mutationRequest) error {
		return h.service.Purge(r.Context(), userID, req.FileID, deviceID)
	})
}
func (h *Handler) TrashList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	items, err := h.service.ListTrash(r.Context(), auth.UserFromContext(r.Context()).ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, items)
}

type mutationRequest struct {
	FileID          string `json:"fileId"`
	ParentID        string `json:"parentId"`
	Name            string `json:"name"`
	ExpectedVersion *int64 `json:"expectedVersion,omitempty"`
}

func (h *Handler) mutation(w http.ResponseWriter, r *http.Request, fn func(userID, role, deviceID string, req mutationRequest) error) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req mutationRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	user := auth.UserFromContext(r.Context())
	if err := fn(user.ID, user.Role, auth.DeviceIDFromContext(r.Context()), req); err != nil {
		status := http.StatusBadRequest
		if err.Error() == "version conflict" {
			status = http.StatusConflict
		}
		httpx.Error(w, status, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
