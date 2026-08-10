package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"necipdrive/internal/syncclient"
	"necipdrive/internal/syncengine"
	"necipdrive/internal/syncstore"

	"github.com/google/uuid"
)

const credTarget = "TRDriver/DeviceToken"

type Config struct {
	DataDir    string
	ServerURL  string
	Email      string
	Password   string
	DeviceName string
	Folder     string
	Headless   bool
}

type Snapshot struct {
	Connected  bool                 `json:"connected"`
	ServerURL  string               `json:"serverUrl"`
	Email      string               `json:"email"`
	DeviceName string               `json:"deviceName"`
	Autostart  bool                 `json:"autostart"`
	DataDir    string               `json:"dataDir"`
	Status     syncengine.Status    `json:"status"`
	Folders    []syncstore.Root     `json:"folders"`
	Activities []syncstore.Activity `json:"activities"`
	PendingJobs int64               `json:"pendingJobs"`
}

type App struct {
	cfg    Config
	store  *syncstore.Store
	client *syncclient.Client
	engine *syncengine.Engine
	mu     sync.Mutex
	cancel context.CancelFunc
}

func DefaultDataDir() string {
	if runtime.GOOS == "windows" {
		if p := os.Getenv("LOCALAPPDATA"); p != "" {
			return filepath.Join(p, "TRDriver")
		}
	}
	if p, err := os.UserConfigDir(); err == nil {
		return filepath.Join(p, "TRDriver")
	}
	return filepath.Join(".", "data", "sync")
}

func New(cfg Config) (*App, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	if cfg.DeviceName == "" {
		host, _ := os.Hostname()
		cfg.DeviceName = "Windows-" + host
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	store, err := syncstore.Open(filepath.Join(cfg.DataDir, "sync.db"))
	if err != nil {
		return nil, err
	}
	app := &App{cfg: cfg, store: store, client: &syncclient.Client{}}
	if err := app.loadSettings(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return app, nil
}

func (a *App) Close() error {
	a.Stop()
	return a.store.Close()
}

func (a *App) DataDir() string { return a.cfg.DataDir }

func (a *App) loadSettings() error {
	if a.cfg.ServerURL == "" {
		a.cfg.ServerURL, _ = a.store.GetMeta("server_url")
	}
	if a.cfg.Email == "" {
		a.cfg.Email, _ = a.store.GetMeta("email")
	}
	token, _ := ReadCredential(credTarget)
	if token == "" {
		token, _ = a.store.GetMeta("device_token")
	}
	a.client.BaseURL = a.cfg.ServerURL
	a.client.Token = token
	return nil
}

func (a *App) SaveConnection(serverURL, email, password string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.ServerURL = serverURL
	a.cfg.Email = email
	a.client.BaseURL = serverURL
	token, err := a.client.DeviceLogin(context.Background(), email, password, a.cfg.DeviceName)
	if err != nil {
		return err
	}
	if err := WriteCredential(credTarget, email, token); err != nil {
		_ = a.store.SetMeta("device_token", token)
	} else {
		_ = a.store.SetMeta("device_token", "")
	}
	_ = a.store.SetMeta("server_url", serverURL)
	_ = a.store.SetMeta("email", email)
	a.client.Token = token
	a.cfg.Password = ""
	return nil
}

func (a *App) Logout() error {
	a.Stop()
	a.mu.Lock()
	client := a.client
	a.mu.Unlock()
	if client != nil && client.Token != "" {
		_ = client.DeviceLogout(context.Background())
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.client.Token = ""
	_ = DeleteCredential(credTarget)
	_ = a.store.SetMeta("device_token", "")
	return nil
}

func (a *App) AddFolder(localPath string) error {
	info, err := os.Stat(localPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("klasör bulunamadı: %s", localPath)
	}
	abs, err := filepath.Abs(localPath)
	if err != nil {
		return err
	}
	roots, _ := a.store.ListRoots()
	for _, r := range roots {
		if filepath.Clean(r.LocalPath) == filepath.Clean(abs) {
			return fmt.Errorf("klasör zaten ekli")
		}
	}
	me, err := a.client.Me(context.Background())
	if err != nil {
		return err
	}
	remoteRoot, _ := me["storageRootId"].(string)
	root := syncstore.Root{ID: uuid.NewString(), LocalPath: abs, RemoteParentID: remoteRoot}
	if err := a.store.AddRoot(root); err != nil {
		return err
	}
	a.mu.Lock()
	eng := a.engine
	a.mu.Unlock()
	if eng != nil {
		return eng.AddRootWatch(context.Background(), root)
	}
	return nil
}

func (a *App) RemoveFolder(id string) error {
	a.mu.Lock()
	eng := a.engine
	a.mu.Unlock()
	if eng != nil {
		return eng.RemoveRootWatch(id)
	}
	return a.store.RemoveRoot(id)
}

func (a *App) PauseFolder(id string) error {
	a.mu.Lock()
	eng := a.engine
	a.mu.Unlock()
	if eng != nil {
		return eng.Pause(id)
	}
	return a.store.SetPaused(id, true)
}

func (a *App) ResumeFolder(id string) error {
	a.mu.Lock()
	eng := a.engine
	ctxCancel := a.cancel
	a.mu.Unlock()
	if eng == nil {
		return a.Start()
	}
	ctx := context.Background()
	if ctxCancel != nil {
		// Resume uses engine root ctx internally when available
		_ = ctx
	}
	return eng.Resume(context.Background(), id)
}

func (a *App) ListFolders() ([]syncstore.Root, error) { return a.store.ListRoots() }

func (a *App) Status() syncengine.Status {
	a.mu.Lock()
	eng := a.engine
	a.mu.Unlock()
	if eng == nil {
		return syncengine.Status{State: "idle", Message: "Hazır"}
	}
	return eng.Status()
}

func (a *App) Subscribe(buffer int) (<-chan syncengine.Event, func(), bool) {
	a.mu.Lock()
	eng := a.engine
	a.mu.Unlock()
	if eng == nil {
		return nil, func() {}, false
	}
	ch, unsub := eng.Subscribe(buffer)
	return ch, unsub, true
}

func (a *App) Snapshot() Snapshot {
	st := a.Status()
	folders, _ := a.store.ListRoots()
	acts, _ := a.store.ListActivities(30)
	pending, _ := a.store.CountPendingJobs()
	cfg := a.Settings()
	return Snapshot{
		Connected:   cfg.ServerURL != "" && a.client.Token != "",
		ServerURL:   cfg.ServerURL,
		Email:       cfg.Email,
		DeviceName:  cfg.DeviceName,
		Autostart:   a.AutostartEnabled(),
		DataDir:     cfg.DataDir,
		Status:      st,
		Folders:     folders,
		Activities:  acts,
		PendingJobs: pending,
	}
}

func (a *App) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.engine != nil {
		return nil
	}
	if a.client.BaseURL == "" || a.client.Token == "" {
		return fmt.Errorf("önce sunucu girişi yapın")
	}
	ctx, cancel := context.WithCancel(context.Background())
	eng := syncengine.New(a.store, a.client, syncengine.Config{
		ServerURL:  a.cfg.ServerURL,
		Email:      a.cfg.Email,
		Token:      a.client.Token,
		DeviceName: a.cfg.DeviceName,
	})
	if err := eng.Start(ctx); err != nil {
		cancel()
		return err
	}
	a.engine = eng
	a.cancel = cancel
	return nil
}

func (a *App) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	if a.engine != nil {
		a.engine.Stop()
		a.engine = nil
	}
}

func (a *App) PauseAll() error {
	roots, err := a.store.ListRoots()
	if err != nil {
		return err
	}
	a.mu.Lock()
	eng := a.engine
	a.mu.Unlock()
	for _, r := range roots {
		if eng != nil {
			_ = eng.Pause(r.ID)
		} else {
			_ = a.store.SetPaused(r.ID, true)
		}
	}
	return nil
}

func (a *App) ResumeAll() error {
	roots, err := a.store.ListRoots()
	if err != nil {
		return err
	}
	a.mu.Lock()
	eng := a.engine
	a.mu.Unlock()
	if eng == nil {
		return a.Start()
	}
	for _, r := range roots {
		_ = eng.Resume(context.Background(), r.ID)
	}
	return nil
}

func (a *App) SetAutostart(enabled bool) error { return SetAutostart(enabled) }
func (a *App) AutostartEnabled() bool           { return IsAutostartEnabled() }
func (a *App) Settings() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}
func (a *App) ClientToken() string { return a.client.Token }
