package files

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"necipdrive/internal/auth"
	"necipdrive/internal/domain"
	"necipdrive/internal/httpx"
	"necipdrive/internal/loadpace"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) ListOrCreateFolder(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	switch r.Method {
	case http.MethodGet:
		parentID := r.URL.Query().Get("parentId")
		if parentID == "" {
			parentID = user.StorageRootID
		}
		items, err := h.service.List(r.Context(), user.ID, user.Role, parentID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req struct {
			ParentID string `json:"parentId"`
			Name     string `json:"name"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		if req.ParentID == "" {
			req.ParentID = user.StorageRootID
		}
		entry, err := h.service.CreateFolder(r.Context(), user.ID, user.Role, req.ParentID, req.Name, auth.DeviceIDFromContext(r.Context()))
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, entry)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user := auth.UserFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, h.service.cfg.MaxUploadBytes+(2<<20))
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid form or upload is too large")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "file is required")
		return
	}
	parentID := r.FormValue("parentId")
	if parentID == "" {
		parentID = user.StorageRootID
	}
	entry, err := h.service.Upload(r.Context(), *user, parentID, auth.DeviceIDFromContext(r.Context()), file, header)
	if err != nil {
		if errors.Is(err, loadpace.ErrOverloaded) {
			snap := h.service.UploadPace()
			retry := snap.RetryAfterSec
			if retry < 1 {
				retry = 5
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			httpx.Error(w, http.StatusTooManyRequests, "Sunucu yoğun, lütfen biraz bekleyin")
			return
		}
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, entry)
}

func (h *Handler) UploadPace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.service.UploadPace())
}

func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user := auth.UserFromContext(r.Context())
	fileID := strings.TrimPrefix(r.URL.Path, "/api/files/download/")
	fileID = strings.Trim(fileID, "/")
	if fileID == "" || strings.Contains(fileID, "/") {
		httpx.Error(w, http.StatusBadRequest, "file id required")
		return
	}

	if r.URL.Query().Get("zip") == "1" {
		h.downloadFolderZip(w, r, user, fileID)
		return
	}

	entry, reader, err := h.service.Download(r.Context(), user.ID, user.Role, fileID)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, err.Error())
		return
	}
	defer reader.Close()

	contentType := resolveContentType(entry.Name, entry.MimeType)
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" && canInlinePreview(entry.Name, contentType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": entry.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if file, ok := reader.(*os.File); ok {
		http.ServeContent(w, r, entry.Name, entry.UpdatedAt, file)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(entry.SizeBytes, 10))
	_, _ = io.Copy(w, reader)
}

func (h *Handler) downloadFolderZip(w http.ResponseWriter, r *http.Request, user *domain.User, folderID string) {
	if err := h.service.WriteFolderZip(r.Context(), user.ID, user.Role, folderID, w); err != nil {
		status := http.StatusBadRequest
		msg := err.Error()
		if strings.Contains(msg, "not found") || strings.Contains(msg, "denied") || strings.Contains(msg, "permission") {
			status = http.StatusNotFound
		}
		httpx.Error(w, status, msg)
	}
}

func (h *Handler) Move(w http.ResponseWriter, r *http.Request) {
	h.writeMutation(w, r, func(user *domain.User, req moveRequest) error {
		if req.ParentID == "" {
			req.ParentID = user.StorageRootID
		}
		return h.service.Move(r.Context(), user.ID, user.Role, req.FileID, req.ParentID, auth.DeviceIDFromContext(r.Context()))
	})
}

func (h *Handler) Rename(w http.ResponseWriter, r *http.Request) {
	h.writeMutation(w, r, func(user *domain.User, req moveRequest) error {
		return h.service.Rename(r.Context(), user.ID, user.Role, req.FileID, req.Name, auth.DeviceIDFromContext(r.Context()))
	})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	h.writeMutation(w, r, func(user *domain.User, req moveRequest) error {
		return h.service.Delete(r.Context(), user.ID, user.Role, req.FileID, auth.DeviceIDFromContext(r.Context()))
	})
}

type moveRequest struct {
	FileID   string `json:"fileId"`
	ParentID string `json:"parentId"`
	Name     string `json:"name"`
}

func (h *Handler) writeMutation(w http.ResponseWriter, r *http.Request, fn func(user *domain.User, req moveRequest) error) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req moveRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	user := auth.UserFromContext(r.Context())
	if err := fn(user, req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
