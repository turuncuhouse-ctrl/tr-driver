package desktop

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"necipdrive/internal/syncengine"
)

type Host struct {
	app            *App
	ln             net.Listener
	url            string
	mu             sync.Mutex
	onShowSettings func()
	onShowFlyout   func()
	onHide         func()
	onQuit         func()
}

func NewHost(app *App) (*Host, error) {
	assets, err := UIAssets()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	h := &Host{app: app, ln: ln, url: "http://" + ln.Addr().String()}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", h.handleState)
	mux.HandleFunc("/api/events", h.handleEvents)
	mux.HandleFunc("/api/login", h.handleLogin)
	mux.HandleFunc("/api/logout", h.handleLogout)
	mux.HandleFunc("/api/pause", func(w http.ResponseWriter, r *http.Request) {
		_ = app.PauseAll()
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/resume", func(w http.ResponseWriter, r *http.Request) {
		if err := app.ResumeAll(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/autostart", h.handleAutostart)
	mux.HandleFunc("/api/pick-folder", h.handlePickFolder)
	mux.HandleFunc("/api/remove-folder", h.handleRemoveFolder)
	mux.HandleFunc("/api/pause-folder", h.handlePauseFolder)
	mux.HandleFunc("/api/resume-folder", h.handleResumeFolder)
	mux.HandleFunc("/api/open-logs", func(w http.ResponseWriter, r *http.Request) {
		_ = OpenLogDir(app.DataDir())
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/hide", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		fn := h.onHide
		h.mu.Unlock()
		if fn != nil {
			fn()
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/show-settings", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		fn := h.onShowSettings
		h.mu.Unlock()
		if fn != nil {
			fn()
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/show-flyout", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		fn := h.onShowFlyout
		h.mu.Unlock()
		if fn != nil {
			fn()
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		fn := h.onQuit
		h.mu.Unlock()
		writeJSON(w, map[string]string{"status": "ok"})
		if fn != nil {
			go fn()
		}
	})
	mux.Handle("/", http.FileServer(http.FS(assets)))
	go http.Serve(ln, withCacheBust(mux))
	return h, nil
}

func (h *Host) URL() string { return h.url }
func (h *Host) Close() error {
	if h.ln != nil {
		return h.ln.Close()
	}
	return nil
}

func (h *Host) SetCallbacks(showFlyout, showSettings, quit func()) {
	h.SetWindowCallbacks(showFlyout, showSettings, nil, quit)
}

func (h *Host) SetWindowCallbacks(showFlyout, showSettings, hide, quit func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onShowFlyout = showFlyout
	h.onShowSettings = showSettings
	h.onHide = hide
	h.onQuit = quit
}

func (h *Host) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.app.Snapshot())
}

func (h *Host) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	writeSnapshot := func() {
		b, err := json.Marshal(h.app.Snapshot())
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", b)
		flusher.Flush()
	}
	writeSnapshot()

	var (
		eventCh <-chan syncengine.Event
		unsub   func()
		ready   bool
	)
	for {
		eventCh, unsub, ready = h.app.Subscribe(32)
		if ready {
			break
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			writeSnapshot()
		}
	}
	defer unsub()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case _, ok := <-eventCh:
			if !ok {
				return
			}
			writeSnapshot()
		}
	}
}

func (h *Host) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerURL string `json:"serverUrl"`
		Email     string `json:"email"`
		Password  string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := h.app.SaveConnection(req.ServerURL, req.Email, req.Password); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_ = h.app.Start()
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Host) handleLogout(w http.ResponseWriter, r *http.Request) {
	_ = h.app.Logout()
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Host) handleAutostart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := h.app.SetAutostart(req.Enabled); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Host) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	path, err := PickFolder()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if path == "" {
		writeJSON(w, map[string]string{"status": "cancelled"})
		return
	}
	if err := h.app.AddFolder(path); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_ = h.app.Start()
	writeJSON(w, map[string]string{"status": "ok", "path": path})
}

func (h *Host) handleRemoveFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "id required", 400)
		return
	}
	if err := h.app.RemoveFolder(req.ID); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Host) handlePauseFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := h.app.PauseFolder(req.ID); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Host) handleResumeFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := h.app.ResumeFolder(req.ID); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func withCacheBust(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
