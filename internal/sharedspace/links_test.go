package sharedspace

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func (e *testEnv) createLink(user string, in LinkInput) Link {
	e.t.Helper()
	rec := e.doJSON(user, http.MethodPost, BasePath+"links", in)
	wantStatus(e.t, rec, http.StatusCreated)
	link := decode[Link](e.t, rec)
	if link.Token == "" || link.URL != PublicPrefix+link.Token {
		e.t.Fatalf("link response lacks token/url: %+v", link)
	}
	return link
}

func unlockCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, unlockCookiePrefix) {
			return cookie
		}
	}
	return nil
}

func passwordForm(password string) *strings.Reader {
	return strings.NewReader(url.Values{"password": {password}}.Encode())
}

func TestLinkServesFileUntilItExpires(t *testing.T) {
	env := newTestEnv(t)
	link := env.createLink("writer", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: 2})
	if got := link.ExpiresAt.Sub(env.clock()); got != 2*time.Hour {
		t.Fatalf("link lifetime = %v, want 2h", got)
	}

	page := env.public(http.MethodGet, link.URL, nil)
	wantStatus(t, page, http.StatusOK)
	if !strings.Contains(page.Body.String(), "film.mkv") {
		t.Fatalf("landing page does not name the file: %s", page.Body.String())
	}
	download := env.public(http.MethodGet, link.URL+"/dl", nil)
	wantStatus(t, download, http.StatusOK)
	if download.Body.String() != "film-icerigi" {
		t.Fatalf("download body = %q", download.Body.String())
	}

	// One minute before expiry it still works; at expiry it is gone.
	env.advance(2*time.Hour - time.Minute)
	wantStatus(t, env.public(http.MethodGet, link.URL+"/dl", nil), http.StatusOK)
	env.advance(time.Minute)
	for _, target := range []string{link.URL, link.URL + "/dl"} {
		rec := env.public(http.MethodGet, target, nil)
		wantStatus(t, rec, http.StatusGone)
		if strings.Contains(rec.Body.String(), "film-icerigi") {
			t.Fatal("expired link still served the file")
		}
	}
	// And it no longer shows up in the owner's list.
	links := decode[[]Link](t, env.do("writer", http.MethodGet, BasePath+"links", nil))
	if len(links) != 0 {
		t.Fatalf("expired link still listed: %+v", links)
	}
}

func TestLinkExpiryIsMandatoryAndBounded(t *testing.T) {
	env := newTestEnv(t)
	// No lifetime given: a default applies; there is no "never expires".
	link := env.createLink("writer", LinkInput{Path: "Filmler/film.mkv"})
	if got := link.ExpiresAt.Sub(env.clock()); got != defaultLinkTTL {
		t.Fatalf("default lifetime = %v, want %v", got, defaultLinkTTL)
	}
	for _, hours := range []float64{-1, 0.001, 30*24 + 1, 1e9} {
		rec := env.doJSON("writer", http.MethodPost, BasePath+"links", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: hours})
		wantStatus(t, rec, http.StatusBadRequest)
	}
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"links", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: 30 * 24}), http.StatusCreated)
}

func TestLinkPasswordProtection(t *testing.T) {
	env := newTestEnv(t)
	const password = "dogru-sifre-123"
	link := env.createLink("writer", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: 24, Password: password})
	if !link.HasPassword {
		t.Fatal("link does not report password protection")
	}

	// Without the password: a form, and no content or file details.
	page := env.public(http.MethodGet, link.URL, nil)
	wantStatus(t, page, http.StatusOK)
	if !strings.Contains(page.Body.String(), `type="password"`) || strings.Contains(page.Body.String(), "film.mkv") {
		t.Fatalf("locked page should be only a password form: %s", page.Body.String())
	}
	locked := env.public(http.MethodGet, link.URL+"/dl", nil)
	wantStatus(t, locked, http.StatusUnauthorized)
	if strings.Contains(locked.Body.String(), "film-icerigi") {
		t.Fatal("password-protected file served without the password")
	}

	// Wrong passwords are refused and set no cookie.
	for _, wrong := range []string{"", "yanlis", password + "x", strings.ToUpper(password), strings.Repeat("a", 500)} {
		rec := env.public(http.MethodPost, link.URL, passwordForm(wrong))
		wantStatus(t, rec, http.StatusUnauthorized)
		if unlockCookie(rec) != nil {
			t.Fatalf("wrong password %q produced an unlock cookie", wrong)
		}
	}
	// A forged or foreign cookie does not unlock either.
	forged := &http.Cookie{Name: unlockCookiePrefix + link.ID, Value: strings.Repeat("0", 64)}
	wantStatus(t, env.public(http.MethodGet, link.URL+"/dl", nil, forged), http.StatusUnauthorized)

	// The right password unlocks: redirect, HttpOnly cookie, then the file.
	ok := env.public(http.MethodPost, link.URL, passwordForm(password))
	wantStatus(t, ok, http.StatusSeeOther)
	cookie := unlockCookie(ok)
	if cookie == nil || !cookie.HttpOnly || cookie.Path != PublicPrefix {
		t.Fatalf("unlock cookie = %+v", cookie)
	}
	if strings.Contains(cookie.Value, password) {
		t.Fatal("unlock cookie contains the password")
	}
	download := env.public(http.MethodGet, link.URL+"/dl", nil, cookie)
	wantStatus(t, download, http.StatusOK)
	if download.Body.String() != "film-icerigi" {
		t.Fatalf("download body = %q", download.Body.String())
	}

	// The cookie of one link does not open another protected link.
	other := env.createLink("writer", LinkInput{Path: "Filmler/Alt/derin.txt", ExpiresInHours: 24, Password: "baska-sifre"})
	transplanted := &http.Cookie{Name: unlockCookiePrefix + other.ID, Value: cookie.Value}
	wantStatus(t, env.public(http.MethodGet, other.URL+"/dl", nil, transplanted), http.StatusUnauthorized)

	// Too short and overlong passwords are rejected at creation.
	for _, bad := range []string{"abc", strings.Repeat("x", maxLinkPassword+1)} {
		rec := env.doJSON("writer", http.MethodPost, BasePath+"links", LinkInput{Path: "Filmler/film.mkv", Password: bad})
		wantStatus(t, rec, http.StatusBadRequest)
	}
}

func TestLinkPasswordAttemptsAreRateLimited(t *testing.T) {
	env := newTestEnv(t)
	const password = "dogru-sifre-123"
	link := env.createLink("writer", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: 24, Password: password})
	limited := false
	for i := 0; i < 20; i++ {
		rec := env.public(http.MethodPost, link.URL, passwordForm("tahmin"))
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
		wantStatus(t, rec, http.StatusUnauthorized)
	}
	if !limited {
		t.Fatal("20 wrong passwords in a row were never rate limited")
	}
	// Once limited, even the correct password is not evaluated.
	rec := env.public(http.MethodPost, link.URL, passwordForm(password))
	wantStatus(t, rec, http.StatusTooManyRequests)
	if unlockCookie(rec) != nil {
		t.Fatal("rate-limited attempt still unlocked the link")
	}
}

func TestLinkSecretsAreStoredOnlyAsHashes(t *testing.T) {
	env := newTestEnv(t)
	const password = "dogru-sifre-123"
	link := env.createLink("writer", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: 24, Password: password})
	rows := env.store.rawLinks()
	if len(rows) != 1 {
		t.Fatalf("stored links = %d", len(rows))
	}
	row := rows[0]
	if row.TokenHash == link.Token || strings.Contains(row.TokenHash, link.Token) || row.TokenHash != hashLinkToken(link.Token) {
		t.Fatal("the link token is stored in a recoverable form")
	}
	if !strings.HasPrefix(row.PasswordHash, "argon2id$") || strings.Contains(row.PasswordHash, password) {
		t.Fatalf("password is not stored as an argon2id hash: %q", row.PasswordHash)
	}
	// Listing links never returns the token again.
	listed := env.do("writer", http.MethodGet, BasePath+"links", nil)
	wantStatus(t, listed, http.StatusOK)
	if strings.Contains(listed.Body.String(), link.Token) || strings.Contains(listed.Body.String(), password) || strings.Contains(listed.Body.String(), "argon2id") {
		t.Fatalf("link list leaks a secret: %s", listed.Body.String())
	}
}

func TestLinkSecretsAreNotLogged(t *testing.T) {
	env := newTestEnv(t)
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	const password = "LOG-TEST-sifre-9471"
	link := env.createLink("writer", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: 24, Password: password})
	env.public(http.MethodPost, link.URL, passwordForm("LOG-TEST-yanlis-5512"))
	ok := env.public(http.MethodPost, link.URL, passwordForm(password))
	env.public(http.MethodGet, link.URL+"/dl", nil, unlockCookie(ok))
	env.public(http.MethodGet, PublicPrefix+strings.Repeat("A", 43), nil)
	env.public(http.MethodGet, link.URL+"/dl?path="+url.QueryEscape("../x"), nil, unlockCookie(ok))

	for _, secret := range []string{password, "LOG-TEST-yanlis-5512", link.Token, unlockCookie(ok).Value} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log output contains a secret (%q):\n%s", secret, logs.String())
		}
	}
}

func TestLinkDownloadLimitRevocationAndUnknownTokens(t *testing.T) {
	env := newTestEnv(t)
	one := int64(1)
	limited := env.createLink("writer", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: 24, MaxDownloads: &one})
	wantStatus(t, env.public(http.MethodGet, limited.URL+"/dl", nil), http.StatusOK)
	wantStatus(t, env.public(http.MethodGet, limited.URL+"/dl", nil), http.StatusGone)

	revoked := env.createLink("writer", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: 24})
	// Someone else cannot revoke it; the owner can.
	wantStatus(t, env.do("reader", http.MethodDelete, BasePath+"links/"+revoked.ID, nil), http.StatusNotFound)
	wantStatus(t, env.public(http.MethodGet, revoked.URL+"/dl", nil), http.StatusOK)
	wantStatus(t, env.do("writer", http.MethodDelete, BasePath+"links/"+revoked.ID, nil), http.StatusOK)
	wantStatus(t, env.public(http.MethodGet, revoked.URL+"/dl", nil), http.StatusGone)

	for _, token := range []string{strings.Repeat("A", 43), "kisa", strings.Repeat("B", 200), "..", "%2e%2e"} {
		rec := env.public(http.MethodGet, PublicPrefix+token, nil)
		wantStatus(t, rec, http.StatusNotFound)
	}
	// Deleting the shared file kills its links.
	doomed := env.createLink("writer", LinkInput{Path: "Filmler/Alt/derin.txt", ExpiresInHours: 24})
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"delete", map[string]string{"path": "Filmler/Alt"}), http.StatusOK)
	wantStatus(t, env.public(http.MethodGet, doomed.URL, nil), http.StatusGone)
}

func TestFolderLinkStaysInsideTheFolder(t *testing.T) {
	env := newTestEnv(t)
	link := env.createLink("writer", LinkInput{Path: "Filmler", ExpiresInHours: 24})

	page := env.public(http.MethodGet, link.URL, nil)
	wantStatus(t, page, http.StatusOK)
	body := page.Body.String()
	if !strings.Contains(body, "film.mkv") || !strings.Contains(body, "Alt") {
		t.Fatalf("folder page does not list its content: %s", body)
	}
	if strings.Contains(body, "Gizli") || strings.Contains(body, "kok.txt") {
		t.Fatal("folder page lists entries outside the linked folder")
	}
	wantStatus(t, env.public(http.MethodGet, link.URL+"/dl?path=film.mkv", nil), http.StatusOK)
	deep := env.public(http.MethodGet, link.URL+"/dl?path=Alt/derin.txt", nil)
	wantStatus(t, deep, http.StatusOK)
	if deep.Body.String() != "derin" {
		t.Fatalf("nested download = %q", deep.Body.String())
	}

	for _, escape := range []string{"../Gizli/sir.txt", "..", "Alt/../../Gizli/sir.txt", "/../kok.txt", `..\Gizli\sir.txt`, "../../disarida.txt"} {
		for _, target := range []string{link.URL + "/dl?path=", link.URL + "?path="} {
			rec := env.public(http.MethodGet, target+url.QueryEscape(escape), nil)
			wantStatus(t, rec, http.StatusNotFound)
			if strings.Contains(rec.Body.String(), "gizli") || strings.Contains(rec.Body.String(), "SIR") {
				t.Fatalf("link escaped its folder via %q", escape)
			}
		}
	}
	// A file link has nothing below it.
	file := env.createLink("writer", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: 24})
	wantStatus(t, env.public(http.MethodGet, file.URL+"/dl?path=Alt/derin.txt", nil), http.StatusNotFound)
}

func TestLinkPageEscapesFileNames(t *testing.T) {
	env := newTestEnv(t)
	env.write(`Filmler/<img src=x onerror=alert(1)>.txt`, "x")
	link := env.createLink("writer", LinkInput{Path: "Filmler", ExpiresInHours: 24})
	page := env.public(http.MethodGet, link.URL, nil)
	wantStatus(t, page, http.StatusOK)
	if strings.Contains(page.Body.String(), "<img src=x") {
		t.Fatal("file name was written into the page unescaped")
	}
	csp := page.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Fatalf("public page CSP = %q", csp)
	}
}

func TestOnlyWritersMayCreateLinksAndLosingAccessDisablesThem(t *testing.T) {
	env := newTestEnv(t)
	// Read access is not enough to publish content.
	wantStatus(t, env.doJSON("reader", http.MethodPost, BasePath+"links", LinkInput{Path: "Filmler/film.mkv"}), http.StatusForbidden)
	// Nor is write access elsewhere.
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"links", LinkInput{Path: "Gizli/sir.txt"}), http.StatusForbidden)
	// The whole shared space cannot be published in one link.
	wantStatus(t, env.doJSON("admin", http.MethodPost, BasePath+"links", LinkInput{Path: ""}), http.StatusBadRequest)
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"links", LinkInput{Path: "Filmler/yok.txt"}), http.StatusNotFound)

	link := env.createLink("writer", LinkInput{Path: "Filmler/film.mkv", ExpiresInHours: 24})
	wantStatus(t, env.public(http.MethodGet, link.URL+"/dl", nil), http.StatusOK)

	// Downgrading the creator to read-only turns the link off...
	ctx := context.Background()
	admin := env.actor("admin")
	if _, err := env.service.PutGrant(ctx, admin, GrantInput{Path: "Filmler", SubjectType: SubjectUser, UserID: "writer", Permission: "read"}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, env.public(http.MethodGet, link.URL+"/dl", nil), http.StatusGone)
	// ...restoring it turns the link back on...
	if _, err := env.service.PutGrant(ctx, admin, GrantInput{Path: "Filmler", SubjectType: SubjectUser, UserID: "writer", Permission: "write"}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, env.public(http.MethodGet, link.URL+"/dl", nil), http.StatusOK)
	// ...and a deleted account takes its links with it.
	env.store.removeUser("writer")
	wantStatus(t, env.public(http.MethodGet, link.URL+"/dl", nil), http.StatusGone)
}

func TestPublicPagesAreRateLimitedPerAddress(t *testing.T) {
	env := newTestEnv(t)
	limited := false
	for i := 0; i < 200; i++ {
		rec := env.public(http.MethodGet, PublicPrefix+strings.Repeat("A", 43), nil)
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("200 rapid requests to public links were never rate limited")
	}
}
