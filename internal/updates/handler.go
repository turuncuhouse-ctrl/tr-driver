package updates

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"necipdrive/internal/httpx"
	"necipdrive/internal/version"
)

type Config struct {
	ManifestURL string
	Channel     string
}

type Manifest struct {
	LatestVersion string            `json:"latestVersion"`
	DownloadURL   string            `json:"downloadURL"`
	ReleaseNotes  string            `json:"releaseNotes"`
	PublishedAt   string            `json:"publishedAt"`
	Channels      map[string]string `json:"channels"`
}

type Handler struct {
	cfg    Config
	client *http.Client
}

func NewHandler(cfg Config) *Handler {
	return &Handler{
		cfg: cfg,
		client: &http.Client{
			Timeout: 8 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
}

func (h *Handler) Check(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	current := version.Version
	out := map[string]any{
		"product":          "TR Driver",
		"currentVersion":   current,
		"updateAvailable":  false,
		"latestVersion":    current,
		"downloadURL":      "",
		"releaseNotes":     "",
		"channel":          h.cfg.Channel,
		"centralUpdates":   strings.TrimSpace(h.cfg.ManifestURL) != "",
	}
	if strings.TrimSpace(h.cfg.ManifestURL) == "" {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, h.cfg.ManifestURL, nil)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	req.Header.Set("User-Agent", "TR-Driver/"+current)
	resp, err := h.client.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		if resp != nil {
			_ = resp.Body.Close()
		}
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	defer resp.Body.Close()
	var man Manifest
	if err := json.NewDecoder(resp.Body).Decode(&man); err != nil {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	latest := man.LatestVersion
	if ch := h.cfg.Channel; ch != "" && man.Channels != nil {
		if v, ok := man.Channels[ch]; ok && v != "" {
			latest = v
		}
	}
	if latest == "" {
		latest = current
	}
	out["latestVersion"] = latest
	out["downloadURL"] = man.DownloadURL
	out["releaseNotes"] = man.ReleaseNotes
	out["publishedAt"] = man.PublishedAt
	out["updateAvailable"] = latest != "" && latest != current
	httpx.WriteJSON(w, http.StatusOK, out)
}
