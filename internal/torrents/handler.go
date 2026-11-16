package torrents

import (
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"

	"necipdrive/internal/auth"
	"necipdrive/internal/httpx"
	"necipdrive/internal/ratelimit"
	"necipdrive/internal/sharedspace"
)

// BasePath is where the admin API is mounted. The router puts it behind the
// session and administrator checks; the handler and the service check the
// role again so a routing mistake cannot expose it.
const BasePath = "/api/admin/torrents"

type Handler struct {
	service     *Service
	readLimit   *ratelimit.Limiter
	changeLimit *ratelimit.Limiter
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
		// The panel polls every few seconds while it is open.
		readLimit:   ratelimit.New(120, 60),
		changeLimit: ratelimit.New(30, 15),
	}
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, http.StatusForbidden, ErrForbidden.Error())
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrFileNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrUnsafeTorrent), errors.Is(err, ErrInvalidLimit):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrDuplicate), errors.Is(err, ErrNameTaken), errors.Is(err, ErrInvalidState),
		errors.Is(err, sharedspace.ErrBusy):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNoSpace):
		httpx.Error(w, http.StatusInsufficientStorage, err.Error())
	case errors.Is(err, ErrTooMany):
		httpx.Error(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, ErrUnavailable):
		httpx.Error(w, http.StatusServiceUnavailable, err.Error())
	default:
		log.Printf("torrents: %v", err)
		httpx.Error(w, http.StatusInternalServerError, "işlem tamamlanamadı")
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil || user.ID == "" {
		httpx.Error(w, http.StatusUnauthorized, "oturum gerekli")
		return
	}
	actor := Actor{ID: user.ID, Role: user.Role}
	if !actor.IsAdmin() {
		httpx.Error(w, http.StatusForbidden, ErrForbidden.Error())
		return
	}
	limiter := h.readLimit
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		limiter = h.changeLimit
	}
	if !limiter.Allow(actor.ID) {
		limiter.Reject(w)
		httpx.Error(w, http.StatusTooManyRequests, "çok fazla istek; lütfen biraz bekleyin")
		return
	}

	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, BasePath), "/")
	switch {
	case rest == "":
		h.collection(w, r, actor)
	case rest == "limits":
		h.limits(w, r, actor)
	default:
		id, action, _ := strings.Cut(rest, "/")
		h.item(w, r, actor, id, action)
	}
}

func (h *Handler) collection(w http.ResponseWriter, r *http.Request, actor Actor) {
	switch r.Method {
	case http.MethodGet:
		overview, err := h.service.Overview(r.Context(), actor)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, overview)
	case http.MethodPost:
		h.add(w, r, actor)
	default:
		w.Header().Set("Allow", "GET, POST")
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// add accepts either a JSON body {"magnet": "..."} or the raw bytes of a
// .torrent file (Content-Type application/x-bittorrent).
func (h *Handler) add(w http.ResponseWriter, r *http.Request, actor Actor) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var (
		view View
		err  error
	)
	if mediaType == "application/json" {
		var req struct {
			Magnet string `json:"magnet"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxMagnetBytes+1024)
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
			return
		}
		view, err = h.service.AddMagnet(r.Context(), actor, req.Magnet)
	} else {
		data, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxMetainfoBytes+1))
		if readErr != nil || len(data) > MaxMetainfoBytes {
			httpx.Error(w, http.StatusRequestEntityTooLarge, ".torrent dosyası çok büyük")
			return
		}
		view, err = h.service.AddFile(r.Context(), actor, data)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, view)
}

func (h *Handler) limits(w http.ResponseWriter, r *http.Request, actor Actor) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		w.Header().Set("Allow", "PUT")
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req Limits
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
		return
	}
	limits, err := h.service.SetLimits(r.Context(), actor, req)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, limits)
}

func (h *Handler) item(w http.ResponseWriter, r *http.Request, actor Actor, id, action string) {
	switch {
	case action == "" && r.Method == http.MethodGet:
		detail, err := h.service.Detail(r.Context(), actor, id)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, detail)
	case action == "" && r.Method == http.MethodDelete:
		query := r.URL.Query().Get("deleteData")
		deleteData := query == "1" || strings.EqualFold(query, "true")
		if err := h.service.Remove(r.Context(), actor, id, deleteData); err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "removed"})
	case action == "pause" && r.Method == http.MethodPost:
		view, err := h.service.Pause(r.Context(), actor, id)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	case action == "resume" && r.Method == http.MethodPost:
		view, err := h.service.Resume(r.Context(), actor, id)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	case action == "stopseeding" && r.Method == http.MethodPost:
		view, err := h.service.StopSeeding(r.Context(), actor, id)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	case action == "limits" && (r.Method == http.MethodPut || r.Method == http.MethodPost):
		var req ItemLimits
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
			return
		}
		view, err := h.service.SetItemLimits(r.Context(), actor, id, req)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	case action == "files" && r.Method == http.MethodPut:
		var req struct {
			Path     string `json:"path"`
			Priority string `json:"priority"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
			return
		}
		view, err := h.service.SetFilePriority(r.Context(), actor, id, req.Path, req.Priority)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	case action == "" || action == "pause" || action == "resume" ||
		action == "stopseeding" || action == "limits" || action == "files":
		w.Header().Set("Allow", "GET, DELETE, POST, PUT")
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	default:
		httpx.Error(w, http.StatusNotFound, "bulunamadı")
	}
}
