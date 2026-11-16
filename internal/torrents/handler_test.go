package torrents

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"necipdrive/internal/auth"
	"necipdrive/internal/domain"
)

func call(t *testing.T, h http.Handler, user *domain.User, method, target, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if user != nil {
		req = req.WithContext(auth.WithUser(req.Context(), user))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func jsonBody(t *testing.T, value any) io.Reader {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(data)
}

func TestTorrentAPIIsAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	handler := NewHandler(env.service)
	source := newSeeder(t)
	data, hash := source.share(t, "api.bin", map[string][]byte{"": randomBytes(t, 2000)})
	adminUser := &domain.User{ID: admin.ID, Role: "admin"}
	plainUser := &domain.User{ID: regular.ID, Role: "user"}

	created := call(t, handler, adminUser, http.MethodPost, BasePath, "application/x-bittorrent", bytes.NewReader(data))
	if created.Code != http.StatusCreated {
		t.Fatalf("admin upload = %d: %s", created.Code, created.Body)
	}
	var view View
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil || view.InfoHash != hash {
		t.Fatalf("created view = %s (%v)", created.Body, err)
	}

	requests := []struct {
		method, target, contentType string
		body                        func() io.Reader
	}{
		{http.MethodGet, BasePath, "", nil},
		{http.MethodPost, BasePath, "application/json", func() io.Reader { return jsonBody(t, map[string]string{"magnet": magnetFor(hash, "x")}) }},
		{http.MethodPost, BasePath, "application/x-bittorrent", func() io.Reader { return bytes.NewReader(data) }},
		{http.MethodPut, BasePath + "/limits", "application/json", func() io.Reader { return jsonBody(t, Limits{DownloadLimitKBps: 1}) }},
		{http.MethodGet, BasePath + "/" + view.ID, "", nil},
		{http.MethodPut, BasePath + "/" + view.ID + "/limits", "application/json", func() io.Reader {
			return jsonBody(t, ItemLimits{DownloadLimitKBps: 1})
		}},
		{http.MethodPut, BasePath + "/" + view.ID + "/files", "application/json", func() io.Reader {
			return jsonBody(t, map[string]string{"path": "x.bin", "priority": PrioritySkip})
		}},
		{http.MethodPost, BasePath + "/" + view.ID + "/stopseeding", "", nil},
		{http.MethodPost, BasePath + "/" + view.ID + "/pause", "", nil},
		{http.MethodPost, BasePath + "/" + view.ID + "/resume", "", nil},
		{http.MethodDelete, BasePath + "/" + view.ID + "?deleteData=1", "", nil},
	}
	for _, req := range requests {
		for label, user := range map[string]*domain.User{"regular user": plainUser, "user without role": {ID: "u2"}, "anonymous": nil} {
			var body io.Reader
			if req.body != nil {
				body = req.body()
			}
			rec := call(t, handler, user, req.method, req.target, req.contentType, body)
			want := http.StatusForbidden
			if user == nil {
				want = http.StatusUnauthorized
			}
			if rec.Code != want {
				t.Errorf("%s %s as %s = %d, want %d", req.method, req.target, label, rec.Code, want)
			}
			if strings.Contains(rec.Body.String(), hash) || strings.Contains(rec.Body.String(), "api.bin") {
				t.Errorf("%s %s as %s leaked torrent details: %s", req.method, req.target, label, rec.Body)
			}
		}
	}
	// Nothing a non-admin sent had any effect.
	if got, ok := env.view(view.ID); !ok || got.Status == StatusPaused {
		t.Fatalf("torrent after non-admin requests = %+v (%v)", got, ok)
	}
	if env.service.limits != (Limits{}) {
		t.Fatalf("limits changed by a non-admin: %+v", env.service.limits)
	}
}

func TestTorrentAPIRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	handler := NewHandler(env.service)
	source := newSeeder(t)
	content := randomBytes(t, 120_000)
	data, hash := source.share(t, "api-indir.bin", map[string][]byte{"": content})
	user := &domain.User{ID: admin.ID, Role: "admin"}

	// Status mapping for bad input.
	for label, tc := range map[string]struct {
		contentType string
		body        io.Reader
		want        int
	}{
		"junk file":      {"application/x-bittorrent", strings.NewReader("merhaba"), http.StatusBadRequest},
		"empty file":     {"application/octet-stream", strings.NewReader(""), http.StatusBadRequest},
		"oversized file": {"application/x-bittorrent", bytes.NewReader(make([]byte, MaxMetainfoBytes+1)), http.StatusRequestEntityTooLarge},
		"bad magnet":     {"application/json", jsonBody(t, map[string]string{"magnet": "http://example.com"}), http.StatusBadRequest},
		"bad json":       {"application/json", strings.NewReader("{"), http.StatusBadRequest},
	} {
		if rec := call(t, handler, user, http.MethodPost, BasePath, tc.contentType, tc.body); rec.Code != tc.want {
			t.Errorf("%s = %d, want %d: %s", label, rec.Code, tc.want, rec.Body)
		}
	}

	created := call(t, handler, user, http.MethodPost, BasePath, "application/json", jsonBody(t, map[string]string{"magnet": magnetFor(hash, "api indir")}))
	if created.Code != http.StatusCreated {
		t.Fatalf("add magnet = %d: %s", created.Code, created.Body)
	}
	var view View
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if rec := call(t, handler, user, http.MethodPost, BasePath, "application/x-bittorrent", bytes.NewReader(data)); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409", rec.Code)
	}

	if rec := call(t, handler, user, http.MethodPut, BasePath+"/limits", "application/json", jsonBody(t, Limits{DownloadLimitKBps: -3})); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative limit = %d, want 400", rec.Code)
	}
	if rec := call(t, handler, user, http.MethodPut, BasePath+"/limits", "application/json", jsonBody(t, Limits{DownloadLimitKBps: 4096, UploadLimitKBps: 64})); rec.Code != http.StatusOK {
		t.Fatalf("set limits = %d: %s", rec.Code, rec.Body)
	}

	if rec := call(t, handler, user, http.MethodPost, BasePath+"/"+view.ID+"/pause", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rec.Code, rec.Body)
	}
	if rec := call(t, handler, user, http.MethodPost, BasePath+"/"+view.ID+"/pause", "", nil); rec.Code != http.StatusConflict {
		t.Fatalf("pause twice = %d, want 409", rec.Code)
	}
	if rec := call(t, handler, user, http.MethodPost, BasePath+"/"+view.ID+"/resume", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body)
	}
	if rec := call(t, handler, user, http.MethodPost, BasePath+"/yok/pause", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("pause unknown = %d, want 404", rec.Code)
	}
	if rec := call(t, handler, user, http.MethodGet, BasePath+"/"+view.ID+"/pause", "", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET pause = %d, want 405", rec.Code)
	}
	if rec := call(t, handler, user, http.MethodPost, BasePath+"/"+view.ID+"/sil", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action = %d, want 404", rec.Code)
	}

	env.connect(view.ID, source)
	env.waitStatus(view.ID, downloadTimeout, StatusCompleted)

	listed := call(t, handler, user, http.MethodGet, BasePath, "", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list = %d", listed.Code)
	}
	var overview Overview
	if err := json.Unmarshal(listed.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if !overview.Enabled || !overview.Available || len(overview.Torrents) != 1 || overview.Torrents[0].Status != StatusCompleted ||
		overview.Limits != (Limits{DownloadLimitKBps: 4096, UploadLimitKBps: 64}) {
		t.Fatalf("overview = %+v", overview)
	}

	// Without deleteData the files stay. (A fresh handler: this test has used
	// up one administrator's burst of changes.)
	handler = NewHandler(env.service)
	if rec := call(t, handler, user, http.MethodDelete, BasePath+"/"+view.ID, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body)
	}
	if got, err := env.sharedFile("api-indir.bin"); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("removing the torrent touched the downloaded file (%v)", err)
	}
	if rec := call(t, handler, user, http.MethodDelete, BasePath+"/"+view.ID, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("remove twice = %d, want 404", rec.Code)
	}
}

func TestTorrentAPIRateLimit(t *testing.T) {
	env := newTestEnv(t)
	handler := NewHandler(env.service)
	user := &domain.User{ID: admin.ID, Role: "admin"}
	limited := 0
	for i := 0; i < 40; i++ {
		rec := call(t, handler, user, http.MethodPut, BasePath+"/limits", "application/json", jsonBody(t, Limits{}))
		if rec.Code == http.StatusTooManyRequests {
			limited++
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
		}
	}
	if limited == 0 {
		t.Fatal("40 rapid changes were never rate limited")
	}
	// Another administrator has their own budget.
	other := &domain.User{ID: "admin-2", Role: "admin"}
	if rec := call(t, handler, other, http.MethodGet, BasePath, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("second admin = %d", rec.Code)
	}
}

func TestDisabledClientAnswers503(t *testing.T) {
	env := newTestEnv(t, func(o *Options) { o.Enabled = false })
	handler := NewHandler(env.service)
	user := &domain.User{ID: admin.ID, Role: "admin"}
	if rec := call(t, handler, user, http.MethodGet, BasePath, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("overview on a disabled client = %d", rec.Code)
	}
	rec := call(t, handler, user, http.MethodPost, BasePath, "application/json", jsonBody(t, map[string]string{"magnet": magnetFor(strings.Repeat("cd", 20), "x")}))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("add on a disabled client = %d, want 503", rec.Code)
	}
}

// The drawer needs tracker information, but a private tracker's passkey lives
// in the announce URL: only scheme and host may appear in any response.
func TestTorrentDetailRedactsTrackerSecrets(t *testing.T) {
	env := newTestEnv(t)
	handler := NewHandler(env.service)
	adminUser := &domain.User{ID: admin.ID, Role: "admin"}
	seeder := newSeeder(t)
	data, _ := seeder.share(t, "detail.bin", map[string][]byte{"": randomBytes(t, 40000)})
	created := call(t, handler, adminUser, http.MethodPost, BasePath, "application/x-bittorrent", bytes.NewReader(data))
	if created.Code != http.StatusCreated {
		t.Fatalf("add = %d: %s", created.Code, created.Body)
	}
	var view View
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	env.connect(view.ID, seeder)
	env.waitStatus(view.ID, downloadTimeout, StatusCompleted, StatusSeeding)

	rec := call(t, handler, adminUser, http.MethodGet, BasePath+"/"+view.ID, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, secret := range []string{trackerPasskey, "magnet:", "/announce", "passkey="} {
		if strings.Contains(body, secret) {
			t.Fatalf("detail leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "tracker.example.com") {
		t.Fatalf("detail lost the tracker host (needed for administration): %s", body)
	}
	var detail TorrentDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || len(detail.Files) != 1 {
		t.Fatalf("detail decode: files=%d err=%v body=%s", len(detail.Files), err, body)
	}
	// The overview still carries no tracker at all.
	overview := call(t, handler, adminUser, http.MethodGet, BasePath, "", nil)
	for _, secret := range []string{trackerPasskey, "magnet:", "announce"} {
		if strings.Contains(overview.Body.String(), secret) {
			t.Fatalf("overview leaked %q", secret)
		}
	}
}
