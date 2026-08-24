package uploads

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"necipdrive/internal/auth"
	"necipdrive/internal/httpx"
	"necipdrive/internal/loadpace"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Batches(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	switch r.Method {
	case http.MethodGet:
		items, err := h.service.ListOpenBatches(r.Context(), user.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req struct {
			ParentID string         `json:"parentId"`
			Files    []ManifestFile `json:"files"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		deviceID := auth.DeviceIDFromContext(r.Context())
		for i := range req.Files {
			if deviceID == "" {
				req.Files[i].DeviceID = nil
			} else {
				req.Files[i].DeviceID = &deviceID
			}
		}
		batch, err := h.service.CreateBatch(r.Context(), *user, req.ParentID, req.Files)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, batch)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) AbortBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user := auth.UserFromContext(r.Context())
	batchID := strings.TrimPrefix(r.URL.Path, "/api/uploads/batches/")
	batchID = strings.TrimSuffix(batchID, "/abort")
	if batchID == "" || strings.Contains(batchID, "/") {
		httpx.Error(w, http.StatusBadRequest, "batch id required")
		return
	}
	if err := h.service.AbortBatch(r.Context(), user.ID, batchID); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}

func (h *Handler) AppendChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user := auth.UserFromContext(r.Context())
	sessionID := strings.TrimPrefix(r.URL.Path, "/api/uploads/files/")
	if sessionID == "" || strings.Contains(sessionID, "/") {
		httpx.Error(w, http.StatusBadRequest, "file upload id required")
		return
	}
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "Upload-Offset header required")
		return
	}
	length := r.ContentLength
	if length < 0 {
		httpx.Error(w, http.StatusBadRequest, "Content-Length required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.service.cfg.UploadChunkBytes+1)
	defer r.Body.Close()

	newOffset, err := h.service.AppendChunk(r.Context(), user.ID, sessionID, offset, r.Body, length)
	if err != nil {
		if errors.Is(err, loadpace.ErrOverloaded) {
			w.Header().Set("Retry-After", "5")
			httpx.Error(w, http.StatusTooManyRequests, "Sunucu yoğun, lütfen biraz bekleyin")
			return
		}
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "unexpected offset") {
			w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))
			status = http.StatusConflict
		}
		httpx.Error(w, status, err.Error())
		return
	}
	// Drain any leftover to keep connection reusable.
	_, _ = io.Copy(io.Discard, r.Body)
	w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"offset": newOffset})
}

func (h *Handler) CompleteFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user := auth.UserFromContext(r.Context())
	path := strings.TrimPrefix(r.URL.Path, "/api/uploads/files/")
	sessionID := strings.TrimSuffix(path, "/complete")
	if sessionID == "" || strings.Contains(sessionID, "/") {
		httpx.Error(w, http.StatusBadRequest, "file upload id required")
		return
	}
	entry, err := h.service.CompleteFile(r.Context(), user.ID, sessionID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, entry)
}
