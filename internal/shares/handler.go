package shares

import (
	"encoding/json"
	"html"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"necipdrive/internal/auth"
	"necipdrive/internal/config"
	"necipdrive/internal/files"
	"necipdrive/internal/httpx"
	"necipdrive/internal/mailer"
)

type Handler struct {
	service     *Service
	fileService *files.Service
	mail        *mailer.Service
	cfg         config.Config
}

func NewHandler(service *Service, fileService *files.Service, mail *mailer.Service, cfg config.Config) *Handler {
	return &Handler{service: service, fileService: fileService, mail: mail, cfg: cfg}
}

func (h *Handler) API(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	switch r.Method {
	case http.MethodGet:
		items, err := h.service.ListMine(r.Context(), user.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "failed to list shares")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req struct {
			FileID        string `json:"fileId"`
			EntryID       string `json:"entryId"`
			Password      string `json:"password"`
			ExpiresAt     string `json:"expiresAt"`
			MaxDownloads  *int64 `json:"maxDownloads"`
			Permission    string `json:"permission"`
			ExpiresInDays *int   `json:"expiresInDays"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		entryID := req.EntryID
		if entryID == "" {
			entryID = req.FileID
		}
		var expiresAt *time.Time
		if req.ExpiresAt != "" {
			parsed, err := time.Parse(time.RFC3339, req.ExpiresAt)
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, "invalid expiresAt")
				return
			}
			expiresAt = &parsed
		} else if req.ExpiresInDays != nil && *req.ExpiresInDays > 0 {
			t := time.Now().Add(time.Duration(*req.ExpiresInDays) * 24 * time.Hour)
			expiresAt = &t
		}
		link, err := h.service.Create(r.Context(), user.ID, user.Role, CreateOpts{
			EntryID: entryID, Password: req.Password, ExpiresAt: expiresAt,
			MaxDownloads: req.MaxDownloads, Permission: req.Permission,
		})
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{
			"id": link.ID, "url": "/s/" + link.Token, "token": link.Token,
			"permission": link.Permission, "expiresAt": link.ExpiresAt, "maxDownloads": link.MaxDownloads,
		})
	case http.MethodDelete:
		var req struct {
			ID string `json:"id"`
		}
		_ = httpx.ReadJSON(r, &req)
		if err := h.service.Revoke(r.Context(), user.ID, req.ID); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Create kept for backward compatibility.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) { h.API(w, r) }

func (h *Handler) EmailLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.mail == nil {
		httpx.Error(w, http.StatusBadRequest, "mail not configured")
		return
	}
	st, err := h.mail.Get(r.Context())
	if err != nil || !st.Enabled {
		httpx.Error(w, http.StatusBadRequest, "mail disabled")
		return
	}
	user := auth.UserFromContext(r.Context())
	var req struct {
		To      string `json:"to"`
		URL     string `json:"url"`
		Subject string `json:"subject"`
		Message string `json:"message"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	to := strings.TrimSpace(req.To)
	link := strings.TrimSpace(req.URL)
	if to == "" || link == "" {
		httpx.Error(w, http.StatusBadRequest, "to and url required")
		return
	}
	if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
		if strings.HasPrefix(link, "/") {
			link = strings.TrimRight(h.cfg.PublicBaseURL, "/") + link
		} else {
			httpx.Error(w, http.StatusBadRequest, "invalid url")
			return
		}
	}
	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		subject = "TR Driver paylaşım bağlantısı"
	}
	body := strings.TrimSpace(req.Message)
	if body == "" {
		body = user.DisplayName + " sizinle bir dosya paylaştı:\n\n" + link + "\n"
	} else if !strings.Contains(body, link) {
		body = body + "\n\n" + link + "\n"
	}
	if err := h.mail.Send(r.Context(), to, subject, body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

func (h *Handler) DownloadPublic(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/s/")
	token = strings.Split(token, "/")[0]
	password := r.URL.Query().Get("password")
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		if password == "" {
			password = r.FormValue("password")
		}
	}
	if cookie, err := r.Cookie(shareCookieName(token)); err == nil && password == "" {
		password = cookie.Value
	}

	setShareHeaders(w)

	// Public meta: minimal fields until unlocked when password-protected.
	if r.URL.Query().Get("meta") == "1" {
		meta, err := h.service.Meta(r.Context(), token, password)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, meta)
		return
	}

	if r.URL.Query().Get("list") == "1" {
		link, err := h.service.Resolve(r.Context(), token, password, false)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		entry, err := h.fileService.EntryForShare(r.Context(), link.EntryID)
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "not found")
			return
		}
		if entry.Kind != "folder" {
			httpx.WriteJSON(w, http.StatusOK, []any{})
			return
		}
		parentID := r.URL.Query().Get("parentId")
		if parentID == "" {
			parentID = entry.ID
		}
		ok, err := h.fileService.IsDescendantOrSelf(r.Context(), parentID, link.EntryID)
		if err != nil || !ok {
			httpx.Error(w, http.StatusNotFound, "not found")
			return
		}
		items, err := h.fileService.ListPublicChildren(r.Context(), parentID)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "failed to list")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, items)
		return
	}

	wantHTML := strings.Contains(r.Header.Get("Accept"), "text/html") && r.URL.Query().Get("download") != "1"
	link, err := h.service.Resolve(r.Context(), token, password, !wantHTML || r.URL.Query().Get("download") == "1")
	if err != nil {
		if wantHTML && (err.Error() == "invalid share password" || strings.Contains(err.Error(), "password")) {
			writeSharePasswordPage(w, token, err.Error())
			return
		}
		if wantHTML {
			writeSharePasswordPage(w, token, err.Error())
			return
		}
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	if password != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     shareCookieName(token),
			Value:    password,
			Path:     "/s/" + token,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   isHTTPS(r),
			MaxAge:   3600,
		})
	}

	entryID := link.EntryID
	if child := r.URL.Query().Get("fileId"); child != "" {
		ok, err := h.fileService.IsDescendantOrSelf(r.Context(), child, link.EntryID)
		if err != nil || !ok {
			httpx.Error(w, http.StatusNotFound, "not found")
			return
		}
		entryID = child
	}
	entry, err := h.fileService.EntryForShare(r.Context(), entryID)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}

	if wantHTML && entry.Kind == "folder" {
		writeShareBrowsePage(w, token, entry.Name)
		return
	}
	if wantHTML && link.Permission == "view" && entry.Kind == "file" {
		writeShareViewPage(w, token, entry.Name)
		return
	}
	if link.Permission == "view" && r.URL.Query().Get("download") != "1" && !wantHTML {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": entry.Name, "kind": entry.Kind, "permission": "view"})
		return
	}

	if entry.Kind != "file" {
		httpx.Error(w, http.StatusBadRequest, "not a file")
		return
	}
	reader, err := h.fileService.OpenStorage(entry.StorageKey)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	cd := mime.FormatMediaType("attachment", map[string]string{"filename": entry.Name})
	if cd == "" {
		cd = `attachment; filename="download"`
	}
	w.Header().Set("Content-Disposition", cd)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, reader)
}

func shareCookieName(token string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, token)
	if len(safe) > 40 {
		safe = safe[:40]
	}
	return "share_pw_" + safe
}

func setShareHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'self'")
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func writeSharePasswordPage(w http.ResponseWriter, token, msg string) {
	setShareHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="tr"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>TR Driver Paylaşım</title>
<style>body{font-family:Segoe UI,system-ui,sans-serif;background:#0b1220;color:#e8eefc;display:grid;place-items:center;min-height:100vh;margin:0}
form{background:#141c2e;border:1px solid #243049;border-radius:16px;padding:28px;width:min(420px,92vw);display:grid;gap:12px}
input,button{height:42px;border-radius:10px;border:1px solid #243049;background:#0b1220;color:#e8eefc;padding:0 12px}
button{background:#4f6df5;border:0;font-weight:600;cursor:pointer}.err{color:#fecaca;font-size:14px}</style></head><body>
<form method="POST" action="/s/` + html.EscapeString(token) + `"><h1>Paylaşılan içerik</h1>
<p>Bu bağlantı şifre korumalı olabilir.</p>` +
		func() string {
			if msg != "" && msg != "invalid share password" {
				b, _ := json.Marshal(msg)
				return `<div class="err">` + html.EscapeString(strings.Trim(string(b), `"`)) + `</div>`
			}
			if msg == "invalid share password" {
				return `<div class="err">Şifre hatalı</div>`
			}
			return ""
		}() +
		`<input type="password" name="password" placeholder="Şifre" autofocus>
<button type="submit">Devam</button></form></body></html>`))
}

func writeShareBrowsePage(w http.ResponseWriter, token, name string) {
	setShareHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="tr"><head><meta charset="utf-8"><title>` + html.EscapeString(name) + `</title>
<style>body{font-family:Segoe UI,system-ui,sans-serif;background:#0b1220;color:#e8eefc;margin:0;padding:24px}a{color:#93b0ff}</style>
</head><body><h1>` + html.EscapeString(name) + `</h1><ul id="list"></ul>
<script>
const token=` + "`" + html.EscapeString(token) + "`" + `;
fetch('/s/'+token+'?list=1',{credentials:'same-origin'}).then(r=>r.json()).then(items=>{
  const ul=document.getElementById('list');
  (items||[]).forEach(it=>{
    const li=document.createElement('li');
    if(it.kind==='file'){
      const a=document.createElement('a');
      a.href='/s/'+token+'?download=1&fileId='+encodeURIComponent(it.id);
      a.textContent=it.name;
      li.appendChild(a);
    } else {
      li.textContent=it.name+' (klasör)';
    }
    ul.appendChild(li);
  });
});
</script></body></html>`))
}

func writeShareViewPage(w http.ResponseWriter, token, name string) {
	setShareHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="tr"><head><meta charset="utf-8"><title>` + html.EscapeString(name) + `</title>
<style>body{font-family:Segoe UI,system-ui,sans-serif;background:#0b1220;color:#e8eefc;margin:0;padding:24px}a{color:#93b0ff}</style>
</head><body><h1>` + html.EscapeString(name) + `</h1>
<p><a href="/s/` + html.EscapeString(token) + `?download=1">İndir</a></p>
</body></html>`))
}
