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
		httpx.Error(w, http.StatusBadRequest, "E-posta servisi yapılandırılmamış")
		return
	}
	st, err := h.mail.Get(r.Context())
	if err != nil || !st.Enabled {
		httpx.Error(w, http.StatusBadRequest, "E-posta gönderimi kapalı. Admin → Mail ayarlarından SMTP’yi açın.")
		return
	}
	if strings.TrimSpace(st.Host) == "" || strings.TrimSpace(st.From) == "" {
		httpx.Error(w, http.StatusBadRequest, "SMTP ayarları eksik (host / gönderen adresi)")
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
		httpx.Error(w, http.StatusBadRequest, "geçersiz istek")
		return
	}
	to := strings.TrimSpace(req.To)
	link := strings.TrimSpace(req.URL)
	if to == "" || link == "" {
		httpx.Error(w, http.StatusBadRequest, "alıcı e-posta ve paylaşım linki gerekli")
		return
	}
	if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
		if strings.HasPrefix(link, "/") {
			link = strings.TrimRight(h.cfg.PublicBaseURL, "/") + link
		} else {
			httpx.Error(w, http.StatusBadRequest, "geçersiz link")
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
		msg := err.Error()
		low := strings.ToLower(msg)
		switch {
		case strings.Contains(low, "mail disabled"):
			msg = "E-posta gönderimi kapalı. Admin → Mail ayarlarından SMTP’yi açın."
		case strings.Contains(low, "incomplete"):
			msg = "SMTP ayarları eksik (host / gönderen adresi)"
		case strings.Contains(low, "smtp connect"), strings.Contains(low, "smtp auth"), strings.Contains(low, "smtp tls"):
			msg = "E-posta sunucusuna bağlanılamadı: " + err.Error()
		}
		httpx.Error(w, http.StatusBadRequest, msg)
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
	contentType := files.ResolveContentType(entry.Name, entry.MimeType)
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" && files.CanInlinePreview(entry.Name, entry.MimeType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", contentType)
	cd := mime.FormatMediaType(disposition, map[string]string{"filename": entry.Name})
	if cd == "" {
		if disposition == "inline" {
			cd = `inline; filename="download"`
		} else {
			cd = `attachment; filename="download"`
		}
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
	w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src 'self' data:; frame-src 'self'; base-uri 'none'; form-action 'self'")
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
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="tr"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + html.EscapeString(name) + ` · Paylaşım</title>
<style>
body{font-family:Segoe UI,system-ui,sans-serif;background:#0b1220;color:#e8eefc;margin:0;padding:20px;max-width:920px}
h1{font-size:1.35rem;margin:0 0 4px}
.muted{color:#94a3b8;font-size:14px;margin:0 0 16px}
nav{margin-bottom:14px;font-size:14px}
nav a{color:#93b0ff;text-decoration:none}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(148px,1fr));gap:12px}
.card{background:#141c2e;border:1px solid #243049;border-radius:12px;padding:10px;text-decoration:none;color:inherit;display:block}
.card:hover{border-color:#4f6df5}
.thumb{aspect-ratio:1;border-radius:8px;background:#1e293b;display:grid;place-items:center;overflow:hidden;margin-bottom:8px;font-size:28px}
.thumb img{width:100%;height:100%;object-fit:cover;display:block}
.thumb.folder{background:#0b5cad;color:#fff}
.thumb.pdf{background:#3f1d1d;color:#fca5a5}
.name{font-size:13px;font-weight:600;word-break:break-word;line-height:1.3}
.meta{font-size:11px;color:#94a3b8;margin-top:4px}
.list{margin:0;padding:0;list-style:none;display:grid;gap:8px}
.list a{color:#93b0ff;text-decoration:none}
.err{color:#fecaca;padding:12px;border:1px solid #7f1d1d;border-radius:10px;background:#450a0a}
.empty{color:#94a3b8;padding:24px;text-align:center;border:1px dashed #334155;border-radius:12px}
#status{margin-bottom:12px;color:#94a3b8;font-size:14px}
</style></head><body>
<h1>` + html.EscapeString(name) + `</h1>
<p class="muted">Paylaşılan klasör · dosyaları indirebilir veya önizleyebilirsiniz</p>
<nav><a href="?" id="rootLink">Kök</a><span id="crumb"></span></nav>
<p id="status">Yükleniyor…</p>
<div id="grid" class="grid" hidden></div>
<ul id="list" class="list" hidden></ul>
<div id="err" class="err" hidden></div>
<div id="empty" class="empty" hidden>Bu klasör boş.</div>
<script>
const token=` + "`" + html.EscapeString(token) + "`" + `;
const params=new URLSearchParams(location.search);
const parentId=params.get('parentId')||'';
function fmtBytes(n){if(!n||n<1024)return(n||0)+' B';const u=['KB','MB','GB'];let v=n,i=0;while(v>=1024&&i<u.length-1){v/=1024;i++}return v.toFixed(1)+' '+u[i]}
function fileUrl(id,inline){const q=new URLSearchParams({download:'1',fileId:id});if(inline)q.set('inline','1');return '/s/'+token+'?'+q}
function isImage(it){const m=(it.mimeType||'').toLowerCase();return m.startsWith('image/')&&m!=='image/svg+xml'||/\.(jpe?g|png|gif|webp|bmp|heic|heif)$/i.test(it.name||'')}
function isPdf(it){return (it.mimeType||'').toLowerCase()==='application/pdf'||/\.pdf$/i.test(it.name||'')}
function icon(it){if(it.kind==='folder')return '▰';if(isPdf(it))return '▤';if(isImage(it))return '▣';return '▤'}
async function load(){
  const status=document.getElementById('status');
  const grid=document.getElementById('grid');
  const list=document.getElementById('list');
  const err=document.getElementById('err');
  const empty=document.getElementById('empty');
  grid.hidden=list.hidden=err.hidden=empty.hidden=true;
  status.textContent='Yükleniyor…';
  const url='/s/'+token+'?list=1'+(parentId?'&parentId='+encodeURIComponent(parentId):'');
  try{
    const r=await fetch(url,{credentials:'same-origin'});
    const data=await r.json();
    if(!r.ok) throw new Error((data&&data.error)||('HTTP '+r.status));
    if(!Array.isArray(data)) throw new Error('Beklenmeyen yanıt');
    status.textContent=data.length+' öğe';
    if(data.length===0){empty.hidden=false;return}
    grid.hidden=false;
    data.forEach(it=>{
      const card=document.createElement('a');
      card.className='card';
      if(it.kind==='folder'){
        card.href='?parentId='+encodeURIComponent(it.id);
      }else if(isPdf(it)){
        card.href=fileUrl(it.id,true);
        card.target='_blank';
      }else{
        card.href=fileUrl(it.id,false);
      }
      const thumb=document.createElement('div');
      thumb.className='thumb'+(it.kind==='folder'?' folder':isPdf(it)?' pdf':'');
      if(it.kind==='file'&&isImage(it)){
        const img=document.createElement('img');
        img.src=fileUrl(it.id,true);
        img.alt='';
        img.loading='lazy';
        img.onerror=()=>{img.remove();thumb.textContent=icon(it)};
        thumb.appendChild(img);
      }else{
        thumb.textContent=icon(it);
      }
      const nm=document.createElement('div');nm.className='name';nm.textContent=it.name||'Dosya';
      const meta=document.createElement('div');meta.className='meta';
      meta.textContent=it.kind==='folder'?'Klasör · aç':fmtBytes(it.sizeBytes||0);
      card.appendChild(thumb);card.appendChild(nm);card.appendChild(meta);
      grid.appendChild(card);
    });
  }catch(e){
    status.textContent='';
    err.hidden=false;
    err.textContent='Liste yüklenemedi: '+(e&&e.message?e.message:e);
  }
}
if(parentId){document.getElementById('crumb').textContent=' · alt klasör';}
load();
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
