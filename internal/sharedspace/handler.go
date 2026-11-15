package sharedspace

import (
	"errors"
	"log"
	"mime"
	"net/http"
	"net/netip"
	"path"
	"strings"

	"necipdrive/internal/auth"
	"necipdrive/internal/httpx"
	"necipdrive/internal/ratelimit"
)

const (
	// BasePath serves the authenticated user API.
	BasePath = "/api/shared/"
	// AdminBasePath serves grant, group and quota management.
	AdminBasePath = "/api/admin/shared/"
	// PublicPrefix serves public links. It is the only unauthenticated surface.
	PublicPrefix = "/p/"
)

// Handler exposes the shared space over HTTP. Every route except the public
// link pages expects an authenticated user in the request context, and every
// route is rate limited.
type Handler struct {
	service *Service
	trusted []netip.Prefix

	userLimit   *ratelimit.Limiter // browsing and file operations, per user
	writeLimit  *ratelimit.Limiter // uploads and other changes, per user
	linkLimit   *ratelimit.Limiter // creating links, per user
	adminLimit  *ratelimit.Limiter // management API, per admin
	publicLimit *ratelimit.Limiter // public pages, per client address
	unlockLimit *ratelimit.Limiter // password attempts, per address and link
}

func NewHandler(service *Service, trusted []netip.Prefix) *Handler {
	return &Handler{
		service:     service,
		trusted:     trusted,
		userLimit:   ratelimit.New(600, 200),
		writeLimit:  ratelimit.New(300, 120),
		linkLimit:   ratelimit.New(20, 10),
		adminLimit:  ratelimit.New(240, 80),
		publicLimit: ratelimit.New(120, 60),
		unlockLimit: ratelimit.New(1, 8),
	}
}

func actorFrom(r *http.Request) (Actor, bool) {
	user := auth.UserFromContext(r.Context())
	if user == nil || user.ID == "" {
		return Actor{}, false
	}
	return Actor{ID: user.ID, Role: user.Role}, true
}

func tooMany(w http.ResponseWriter, limiter *ratelimit.Limiter) {
	limiter.Reject(w)
	httpx.Error(w, http.StatusTooManyRequests, "çok fazla istek; lütfen biraz bekleyin")
}

// writeError maps service errors to responses. Unexpected errors are logged
// and answered generically, so file-system details do not reach clients.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, http.StatusForbidden, ErrForbidden.Error())
	case errors.Is(err, ErrInvalidPath), errors.Is(err, ErrInvalidName), errors.Is(err, ErrInvalidInput),
		errors.Is(err, ErrIsDir), errors.Is(err, ErrNotDir):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrGrantNotFound), errors.Is(err, ErrGroupNotFound),
		errors.Is(err, ErrUserNotFound), errors.Is(err, ErrLinkNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrExists), errors.Is(err, ErrGroupExists), errors.Is(err, ErrBusy):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrQuotaFull):
		httpx.Error(w, http.StatusInsufficientStorage, err.Error())
	case errors.Is(err, ErrTooLarge):
		httpx.Error(w, http.StatusRequestEntityTooLarge, err.Error())
	default:
		var tooLong *http.MaxBytesError
		if errors.As(err, &tooLong) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, ErrTooLarge.Error())
			return
		}
		log.Printf("shared space: %v", err)
		httpx.Error(w, http.StatusInternalServerError, "işlem tamamlanamadı")
	}
}

// ServeHTTP routes the authenticated user API under BasePath.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "oturum gerekli")
		return
	}
	route := strings.TrimPrefix(r.URL.Path, BasePath)
	limiter := h.userLimit
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		limiter = h.writeLimit
	}
	if !limiter.Allow(actor.ID) {
		tooMany(w, limiter)
		return
	}
	switch {
	case route == "status":
		h.status(w, r, actor)
	case route == "list":
		h.list(w, r, actor)
	case route == "download":
		h.download(w, r, actor)
	case route == "upload":
		h.upload(w, r, actor)
	case route == "folder":
		h.folder(w, r, actor)
	case route == "rename":
		h.rename(w, r, actor)
	case route == "delete":
		h.remove(w, r, actor)
	case route == "links":
		h.links(w, r, actor)
	case strings.HasPrefix(route, "links/"):
		h.link(w, r, actor, strings.TrimPrefix(route, "links/"))
	default:
		httpx.Error(w, http.StatusNotFound, "bulunamadı")
	}
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request, actor Actor) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	access, err := h.service.Access(r.Context(), actor)
	if err != nil {
		writeError(w, err)
		return
	}
	out := map[string]any{"access": access}
	// How full the folder is concerns only people who can use it.
	if access.Available {
		quota, err := h.service.Quota(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		out["quota"] = quota
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, actor Actor) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	listing, err := h.service.List(r.Context(), actor, r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, listing)
}

// serveFile streams a file as a download. Content from the shared folder is
// untrusted (it comes from torrents and other users), so it is never rendered
// in the application's origin: always an attachment, with a sandboxing policy
// for browsers that would display it anyway.
func serveFile(w http.ResponseWriter, r *http.Request, name string, file http.File) {
	info, err := file.Stat()
	if err != nil {
		writeError(w, err)
		return
	}
	contentType := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	if contentType == "" || isActiveContent(contentType) {
		contentType = "application/octet-stream"
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	header.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", info.ModTime(), file)
}

// isActiveContent reports types a browser may execute or render as a document.
func isActiveContent(contentType string) bool {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return true
	}
	switch media {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "text/xml", "application/xml",
		"text/javascript", "application/javascript", "application/x-javascript", "text/css":
		return true
	}
	return false
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request, actor Actor) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	file, info, err := h.service.Open(r.Context(), actor, r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer file.Close()
	serveFile(w, r, info.Name(), file)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request, actor Actor) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		methodNotAllowed(w, "POST, PUT")
		return
	}
	query := r.URL.Query()
	overwrite := query.Get("overwrite") == "1" || strings.EqualFold(query.Get("overwrite"), "true")
	entry, err := h.service.Upload(r.Context(), actor, query.Get("path"), query.Get("name"), r.Body, r.ContentLength, overwrite)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, entry)
}

func (h *Handler) folder(w http.ResponseWriter, r *http.Request, actor Actor) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := readSmallJSON(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
		return
	}
	entry, err := h.service.CreateFolder(r.Context(), actor, req.Path, req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, entry)
}

func (h *Handler) rename(w http.ResponseWriter, r *http.Request, actor Actor) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := readSmallJSON(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
		return
	}
	entry, err := h.service.Rename(r.Context(), actor, req.Path, req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, entry)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request, actor Actor) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := readSmallJSON(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
		return
	}
	if err := h.service.Delete(r.Context(), actor, req.Path); err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) links(w http.ResponseWriter, r *http.Request, actor Actor) {
	switch r.Method {
	case http.MethodGet:
		items, err := h.service.ListLinks(r.Context(), actor)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
	case http.MethodPost:
		if !h.linkLimit.Allow(actor.ID) {
			tooMany(w, h.linkLimit)
			return
		}
		var req LinkInput
		if err := readSmallJSON(w, r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
			return
		}
		link, err := h.service.CreateLink(r.Context(), actor, req)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, link)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (h *Handler) link(w http.ResponseWriter, r *http.Request, actor Actor, id string) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, http.MethodDelete)
		return
	}
	if id == "" || strings.Contains(id, "/") {
		httpx.Error(w, http.StatusNotFound, "bulunamadı")
		return
	}
	if err := h.service.RevokeLink(r.Context(), actor, id); err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// readSmallJSON reads a bounded JSON body; none of these requests is large.
func readSmallJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	return httpx.ReadJSON(r, dst)
}

// ServeAdmin routes the management API under AdminBasePath. The router mounts
// it behind the administrator check; the service checks the role again.
func (h *Handler) ServeAdmin(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "oturum gerekli")
		return
	}
	if !actor.IsAdmin() {
		httpx.Error(w, http.StatusForbidden, "admin role required")
		return
	}
	if !h.adminLimit.Allow(actor.ID) {
		tooMany(w, h.adminLimit)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, AdminBasePath), "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "grants":
		h.adminGrants(w, r, actor)
	case len(parts) == 2 && parts[0] == "grants":
		if r.Method != http.MethodDelete {
			methodNotAllowed(w, http.MethodDelete)
			return
		}
		if err := h.service.DeleteGrant(r.Context(), actor, parts[1]); err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	case len(parts) == 1 && parts[0] == "groups":
		h.adminGroups(w, r, actor)
	case len(parts) == 2 && parts[0] == "groups":
		if r.Method != http.MethodDelete {
			methodNotAllowed(w, http.MethodDelete)
			return
		}
		if err := h.service.DeleteGroup(r.Context(), actor, parts[1]); err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	case len(parts) == 3 && parts[0] == "groups" && parts[2] == "members":
		h.adminGroupMembers(w, r, actor, parts[1])
	case len(parts) == 1 && parts[0] == "settings":
		h.adminSettings(w, r, actor)
	default:
		httpx.Error(w, http.StatusNotFound, "bulunamadı")
	}
}

func (h *Handler) adminGrants(w http.ResponseWriter, r *http.Request, actor Actor) {
	switch r.Method {
	case http.MethodGet:
		items, err := h.service.ListGrants(r.Context(), actor)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req GrantInput
		if err := readSmallJSON(w, r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
			return
		}
		grant, err := h.service.PutGrant(r.Context(), actor, req)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, grant)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (h *Handler) adminGroups(w http.ResponseWriter, r *http.Request, actor Actor) {
	switch r.Method {
	case http.MethodGet:
		items, err := h.service.ListGroups(r.Context(), actor)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := readSmallJSON(w, r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
			return
		}
		group, err := h.service.CreateGroup(r.Context(), actor, req.Name)
		if err != nil {
			writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, group)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (h *Handler) adminGroupMembers(w http.ResponseWriter, r *http.Request, actor Actor, groupID string) {
	var req struct {
		UserID string `json:"userId"`
	}
	// DELETE names the member in the query string: request bodies on DELETE
	// are dropped by some proxies.
	if id := r.URL.Query().Get("userId"); r.Method == http.MethodDelete && id != "" {
		req.UserID = id
	} else if err := readSmallJSON(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
		return
	}
	var err error
	switch r.Method {
	case http.MethodPost:
		err = h.service.AddGroupMember(r.Context(), actor, groupID, req.UserID)
	case http.MethodDelete:
		err = h.service.RemoveGroupMember(r.Context(), actor, groupID, req.UserID)
	default:
		methodNotAllowed(w, "POST, DELETE")
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) adminSettings(w http.ResponseWriter, r *http.Request, actor Actor) {
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut, http.MethodPost:
		var req QuotaSettings
		if err := readSmallJSON(w, r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
			return
		}
		if _, err := h.service.SaveSettings(r.Context(), actor, req); err != nil {
			writeError(w, err)
			return
		}
	default:
		methodNotAllowed(w, "GET, PUT")
		return
	}
	settings, err := h.service.Settings(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	quota, err := h.service.Quota(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"settings": settings, "quota": quota, "directory": h.service.Dir()})
}

// UnavailableHandler answers for the shared space when its folder could not be
// opened at startup. The status route still reports "not available" so clients
// simply hide the feature.
func UnavailableHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == BasePath+"status" {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{
				"access":      Access{RootLevel: LevelNone.String(), MaxLevel: LevelNone.String()},
				"unavailable": true,
			})
			return
		}
		httpx.Error(w, http.StatusServiceUnavailable, "Ortak Paylaşım bu sunucuda kullanılamıyor (SHARED_DIR açılamadı)")
	})
}
