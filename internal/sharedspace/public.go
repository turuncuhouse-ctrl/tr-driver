package sharedspace

import (
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"

	"necipdrive/internal/httpx"
)

// unlockCookiePrefix names the cookie set after a correct link password. The
// cookie holds an HMAC, never the password.
const unlockCookiePrefix = "spl_"

type publicPage struct {
	Title    string
	Message  string
	Error    string
	Password bool // show the password form
	Action   string

	Name      string
	IsDir     bool
	Size      string
	Expires   string
	Download  string
	Crumbs    []publicCrumb
	Entries   []publicRow
	EmptyNote string
}

type publicCrumb struct {
	Name string
	URL  string
}

type publicRow struct {
	Name     string
	IsDir    bool
	Size     string
	Modified string
	URL      string
}

// html/template escapes every dynamic value for its context: file names from
// torrents are attacker-controlled and are shown on this page.
var publicTemplate = template.Must(template.New("public").Parse(`<!doctype html>
<html lang="tr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>{{.Title}} — TR Driver</title>
<style>
:root{color-scheme:light dark}
body{margin:0;font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;background:#f4f5f7;color:#1c1f24}
main{max-width:760px;margin:6vh auto;padding:28px;background:#fff;border-radius:14px;box-shadow:0 8px 30px rgba(0,0,0,.08)}
h1{font-size:1.25rem;margin:0 0 6px;word-break:break-word}
p{margin:6px 0;color:#566070}
.err{color:#b3261e;font-weight:600}
form{margin-top:18px;display:flex;gap:10px;flex-wrap:wrap}
input[type=password]{flex:1;min-width:200px;padding:10px 12px;border:1px solid #c5cbd3;border-radius:8px;font-size:1rem}
button,.btn{display:inline-block;padding:10px 18px;border:0;border-radius:8px;background:#f26b1d;color:#fff;font-size:1rem;font-weight:600;text-decoration:none;cursor:pointer}
table{width:100%;border-collapse:collapse;margin-top:16px}
td,th{padding:9px 6px;border-bottom:1px solid #e6e9ed;text-align:left;font-size:.95rem}
td.n{word-break:break-word}
td.s,td.m,th.s,th.m{white-space:nowrap;color:#566070}
a{color:#c4500c}
nav{margin-top:14px;font-size:.95rem;word-break:break-word}
@media (prefers-color-scheme:dark){body{background:#15171a;color:#e8eaed}main{background:#1f2226;box-shadow:none}p,td.s,td.m,th.s,th.m{color:#a3abb6}td,th{border-color:#30353b}input[type=password]{background:#15171a;color:#e8eaed;border-color:#3a4047}a{color:#ff9a5c}}
@media (max-width:560px){main{margin:0;border-radius:0;padding:20px}td.m,th.m{display:none}}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
{{if .Message}}<p>{{.Message}}</p>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{if .Password}}
<form method="post" action="{{.Action}}" autocomplete="off">
<input type="password" name="password" placeholder="Şifre" maxlength="128" required autofocus>
<button type="submit">Aç</button>
</form>
{{else if .Name}}
{{if .Expires}}<p>Bağlantı {{.Expires}} tarihine kadar geçerli.</p>{{end}}
{{if .IsDir}}
<nav>{{range $i, $c := .Crumbs}}{{if $i}} / {{end}}<a href="{{$c.URL}}">{{$c.Name}}</a>{{end}}</nav>
{{if .Entries}}
<table>
<thead><tr><th>Ad</th><th class="s">Boyut</th><th class="m">Değiştirilme</th></tr></thead>
<tbody>
{{range .Entries}}<tr><td class="n">{{if .IsDir}}📁 {{end}}<a href="{{.URL}}">{{.Name}}</a></td><td class="s">{{.Size}}</td><td class="m">{{.Modified}}</td></tr>
{{end}}</tbody>
</table>
{{else}}<p>{{.EmptyNote}}</p>{{end}}
{{else}}
<p>Boyut: {{.Size}}</p>
<p style="margin-top:18px"><a class="btn" href="{{.Download}}">İndir</a></p>
{{end}}
{{end}}
</main>
</body>
</html>`))

func (h *Handler) renderPublic(w http.ResponseWriter, status int, page publicPage) {
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Robots-Tag", "noindex, nofollow")
	header.Set("Referrer-Policy", "no-referrer")
	// No scripts, no external resources, forms only back to this server.
	header.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	if err := publicTemplate.Execute(w, page); err != nil {
		log.Printf("shared space public page: %v", err)
	}
}

func (h *Handler) publicGone(w http.ResponseWriter, status int) {
	h.renderPublic(w, status, publicPage{
		Title:   "Bağlantı kullanılamıyor",
		Message: "Bu paylaşım bağlantısı geçersiz, süresi dolmuş veya kaldırılmış.",
	})
}

// ServePublic serves public links:
//
//	GET  /p/{token}            landing page (password form, file or folder view)
//	POST /p/{token}            password submission
//	GET  /p/{token}/dl?path=   file download
//
// There is no session here, so it is limited per client address, and password
// attempts are limited much harder per address and link.
func (h *Handler) ServePublic(w http.ResponseWriter, r *http.Request) {
	ip := httpx.ClientIP(r, h.trusted)
	if !h.publicLimit.Allow(ip) {
		h.publicLimit.Reject(w)
		h.renderPublic(w, http.StatusTooManyRequests, publicPage{Title: "Çok fazla istek", Message: "Lütfen biraz bekleyip yeniden deneyin."})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, PublicPrefix)
	token, action, _ := strings.Cut(rest, "/")
	if token == "" || (action != "" && action != "dl") {
		h.publicGone(w, http.StatusNotFound)
		return
	}
	link, err := h.service.ResolveLink(r.Context(), token)
	if err != nil {
		switch {
		case errors.Is(err, ErrLinkNotFound):
			h.publicGone(w, http.StatusNotFound)
		case errors.Is(err, ErrLinkGone):
			h.publicGone(w, http.StatusGone)
		default:
			log.Printf("shared space public link: %v", err)
			h.renderPublic(w, http.StatusInternalServerError, publicPage{Title: "Bir sorun oluştu", Message: "Lütfen daha sonra yeniden deneyin."})
		}
		return
	}
	base := PublicPrefix + url.PathEscape(token)

	if r.Method == http.MethodPost && action == "" {
		h.publicUnlock(w, r, link, base, ip)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, POST")
		h.renderPublic(w, http.StatusMethodNotAllowed, publicPage{Title: "Desteklenmeyen istek"})
		return
	}

	proof := ""
	if cookie, err := r.Cookie(unlockCookiePrefix + link.ID()); err == nil {
		proof = cookie.Value
	}
	if !h.service.Unlocked(link, proof) {
		status := http.StatusOK
		if action == "dl" {
			// A direct download request without the password is refused, not
			// answered with a form that a download client cannot use.
			status = http.StatusUnauthorized
		}
		h.renderPublic(w, status, publicPage{
			Title:    "Şifre korumalı paylaşım",
			Message:  "Bu içeriğe erişmek için paylaşım şifresini girin.",
			Password: true,
			Action:   base,
		})
		return
	}

	sub := r.URL.Query().Get("path")
	if action == "dl" {
		h.publicDownload(w, r, link, sub)
		return
	}
	h.publicView(w, r, link, base, sub)
}

func (h *Handler) publicUnlock(w http.ResponseWriter, r *http.Request, link PublicLink, base, ip string) {
	if !link.NeedsPassword {
		http.Redirect(w, r, base, http.StatusSeeOther)
		return
	}
	form := publicPage{Title: "Şifre korumalı paylaşım", Password: true, Action: base}
	// Charged before the password is looked at, so wrong and right guesses
	// cost the same and the limit cannot be probed for free.
	if !h.unlockLimit.Allow(ip + "|" + link.ID()) {
		h.unlockLimit.Reject(w)
		form.Error = "Çok fazla deneme yapıldı. Lütfen birkaç dakika sonra yeniden deneyin."
		h.renderPublic(w, http.StatusTooManyRequests, form)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		form.Error = "Geçersiz istek."
		h.renderPublic(w, http.StatusBadRequest, form)
		return
	}
	// The password is read from the POST body only, never from the URL, so it
	// cannot end up in access logs, browser history or a Referer header.
	if !h.service.CheckLinkPassword(link, r.PostForm.Get("password")) {
		form.Error = "Şifre hatalı."
		h.renderPublic(w, http.StatusUnauthorized, form)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     unlockCookiePrefix + link.ID(),
		Value:    h.service.UnlockProof(link),
		Path:     PublicPrefix,
		Expires:  link.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   httpx.IsHTTPS(r, h.trusted),
	})
	http.Redirect(w, r, base, http.StatusSeeOther)
}

func (h *Handler) publicDownload(w http.ResponseWriter, r *http.Request, link PublicLink, sub string) {
	// Players and download managers fetch one file in many ranged requests;
	// only the request for the beginning counts against the allowance.
	rangeHeader := strings.ReplaceAll(strings.ToLower(r.Header.Get("Range")), " ", "")
	count := r.Method == http.MethodGet && (rangeHeader == "" || strings.HasPrefix(rangeHeader, "bytes=0-"))
	file, info, err := h.service.PublicOpen(r.Context(), link, sub, count)
	if err != nil {
		h.publicError(w, err)
		return
	}
	defer file.Close()
	serveFile(w, r, info.Name(), file)
}

func (h *Handler) publicError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrLinkGone):
		h.publicGone(w, http.StatusGone)
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrInvalidPath), errors.Is(err, ErrIsDir), errors.Is(err, ErrNotDir):
		h.renderPublic(w, http.StatusNotFound, publicPage{Title: "Bulunamadı", Message: "İstenen dosya veya klasör bu paylaşımda yok."})
	default:
		log.Printf("shared space public link: %v", err)
		h.renderPublic(w, http.StatusInternalServerError, publicPage{Title: "Bir sorun oluştu", Message: "Lütfen daha sonra yeniden deneyin."})
	}
}

func (h *Handler) publicView(w http.ResponseWriter, r *http.Request, link PublicLink, base, rawSub string) {
	page := publicPage{
		Title:   link.Name,
		Name:    link.Name,
		IsDir:   link.IsDir,
		Expires: link.ExpiresAt.Local().Format("02.01.2006 15:04"),
	}
	if !link.IsDir {
		info, err := h.service.PublicStat(link, "")
		if err != nil {
			h.publicError(w, err)
			return
		}
		page.Size = formatBytes(info.Size())
		page.Download = base + "/dl"
		h.renderPublic(w, http.StatusOK, page)
		return
	}
	sub, entries, err := h.service.PublicList(link, rawSub)
	if err != nil {
		h.publicError(w, err)
		return
	}
	page.Crumbs = []publicCrumb{{Name: link.Name, URL: base}}
	if sub != "" {
		walked := ""
		for _, part := range strings.Split(sub, "/") {
			walked = joinRel(walked, part)
			page.Crumbs = append(page.Crumbs, publicCrumb{Name: part, URL: base + "?path=" + url.QueryEscape(walked)})
		}
	}
	page.EmptyNote = "Bu klasör boş."
	for _, entry := range entries {
		row := publicRow{Name: entry.Name, IsDir: entry.IsDir, Modified: entry.ModifiedAt.Local().Format("02.01.2006 15:04")}
		if entry.IsDir {
			row.URL = base + "?path=" + url.QueryEscape(entry.Path)
		} else {
			row.Size = formatBytes(entry.Size)
			row.URL = base + "/dl?path=" + url.QueryEscape(entry.Path)
		}
		page.Entries = append(page.Entries, row)
	}
	h.renderPublic(w, http.StatusOK, page)
}
