package sharedspace

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The torrent client marks the folders it is still writing to; the shared
// space must leave those alone and report what it removes.
func TestPathsInUseByADownloadAreProtected(t *testing.T) {
	env := newTestEnv(t)
	env.write("Indiriliyor/parca.bin", "yarim")
	env.write("Serbest/not.txt", "tamam")

	var mu sync.Mutex
	var changed []string
	env.service.SetHooks(Hooks{
		Busy: func(rel string) bool {
			return rel == "Indiriliyor" || strings.HasPrefix(rel, "Indiriliyor/")
		},
		PathChanged: func(rel string) {
			mu.Lock()
			changed = append(changed, rel)
			mu.Unlock()
		},
	})

	wantStatus(t, env.doJSON("admin", http.MethodPost, BasePath+"delete", map[string]string{"path": "Indiriliyor"}), http.StatusConflict)
	wantStatus(t, env.doJSON("admin", http.MethodPost, BasePath+"delete", map[string]string{"path": "Indiriliyor/parca.bin"}), http.StatusConflict)
	wantStatus(t, env.doJSON("admin", http.MethodPost, BasePath+"rename", map[string]string{"path": "Indiriliyor", "name": "Baska"}), http.StatusConflict)
	wantStatus(t, env.do("admin", http.MethodPost, BasePath+"upload?path=Indiriliyor&name=parca.bin&overwrite=1", strings.NewReader("ustune")), http.StatusConflict)
	if !env.exists("Indiriliyor/parca.bin") || env.exists("Baska") {
		t.Fatal("a path in use by a download was changed")
	}
	// Reading stays possible.
	wantStatus(t, env.do("admin", http.MethodGet, BasePath+"download?path=Indiriliyor/parca.bin", nil), http.StatusOK)
	mu.Lock()
	if len(changed) != 0 {
		t.Fatalf("PathChanged called for refused operations: %v", changed)
	}
	mu.Unlock()

	wantStatus(t, env.doJSON("admin", http.MethodPost, BasePath+"rename", map[string]string{"path": "Serbest", "name": "Tasindi"}), http.StatusOK)
	wantStatus(t, env.doJSON("admin", http.MethodPost, BasePath+"delete", map[string]string{"path": "Tasindi/not.txt"}), http.StatusOK)
	mu.Lock()
	defer mu.Unlock()
	if len(changed) != 2 || changed[0] != "Serbest" || changed[1] != "Tasindi/not.txt" {
		t.Fatalf("PathChanged calls = %v", changed)
	}
}

func TestUnavailableHandler(t *testing.T) {
	handler := UnavailableHandler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, BasePath+"status", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"unavailable":true`) || !strings.Contains(rec.Body.String(), `"rootLevel":"none"`) {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	for _, target := range []string{BasePath + "list", BasePath + "download?path=x", AdminBasePath + "grants", PublicPrefix + "abc"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s = %d, want 503", target, rec.Code)
		}
	}
}
