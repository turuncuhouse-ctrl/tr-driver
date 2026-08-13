package app

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"necipdrive/internal/httpx"
)

type androidVersionInfo struct {
	VersionCode      int    `json:"versionCode"`
	VersionName      string `json:"versionName"`
	MinSupportedCode int    `json:"minSupportedCode"`
	ReleaseNotes     string `json:"releaseNotes"`
	ApkPath          string `json:"apkPath"`
	DownloadURL      string `json:"downloadURL"`
	ApkAvailable     bool   `json:"apkAvailable"`
}

func serveAndroidAPK(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path, size, modTime, err := findAndroidAPK()
	if err != nil {
		log.Printf("android apk: %v", err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "TRDriver.apk bulunamadı. VPS'te git pull + Redeploy yapın; web/public/apps/TRDriver.apk olmalı.", http.StatusNotFound)
		return
	}
	// Reject tiny placeholders (HTML/LFS/error pages saved as .apk).
	if size < 512*1024 {
		log.Printf("android apk too small (%d bytes) at %s", size, path)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.Error(w, fmt.Sprintf("TRDriver.apk bozuk/çok küçük (%d bayt). Yeniden derleyip web/public/apps/ altına kopyalayın.", size), http.StatusInternalServerError)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	head := make([]byte, 4)
	if n, _ := io.ReadFull(f, head); n < 4 || string(head) != "PK\x03\x04" {
		log.Printf("android apk not a zip at %s", path)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.Error(w, "TRDriver.apk ZIP değil (bozuk dosya).", http.StatusInternalServerError)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="TRDriver.apk"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "TRDriver.apk", modTime, f)
	log.Printf("android apk served %s (%d bytes)", path, size)
}

func serveAndroidVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")

	info := androidVersionInfo{
		VersionCode:      0,
		VersionName:      "",
		MinSupportedCode: 1,
		ApkPath:          "/download/TRDriver.apk",
		DownloadURL:      "/download/TRDriver.apk",
	}

	if parsed, path, ok := loadAndroidVersionJSON(); ok {
		info = parsed
		log.Printf("android version json loaded from %s (code=%d name=%s)", path, info.VersionCode, info.VersionName)
	} else {
		log.Printf("android version json not found or invalid")
	}

	if info.ApkPath == "" {
		info.ApkPath = "/download/TRDriver.apk"
	}
	if info.DownloadURL == "" {
		info.DownloadURL = info.ApkPath
	}
	if !strings.HasPrefix(info.DownloadURL, "http://") && !strings.HasPrefix(info.DownloadURL, "https://") {
		if !strings.HasPrefix(info.DownloadURL, "/") {
			info.DownloadURL = "/" + info.DownloadURL
		}
	}

	if _, size, _, err := findAndroidAPK(); err == nil && size >= 512*1024 {
		info.ApkAvailable = true
	}

	// Sensible defaults if JSON missing but APK exists.
	if info.VersionCode <= 0 && info.ApkAvailable {
		info.VersionCode = 1
		if info.VersionName == "" {
			info.VersionName = "unknown"
		}
	}

	httpx.WriteJSON(w, http.StatusOK, info)
}

func loadAndroidVersionJSON() (androidVersionInfo, string, bool) {
	for _, path := range androidAppsCandidates("android-version.json") {
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			continue
		}
		raw = stripUTF8BOM(raw)
		var parsed androidVersionInfo
		if err := json.Unmarshal(raw, &parsed); err != nil {
			log.Printf("android version json parse failed at %s: %v", path, err)
			continue
		}
		if parsed.VersionCode <= 0 && strings.TrimSpace(parsed.VersionName) == "" {
			continue
		}
		return parsed, path, true
	}
	return androidVersionInfo{}, "", false
}

func stripUTF8BOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

func findAndroidAPK() (path string, size int64, mod time.Time, err error) {
	path, err = findAndroidAppsFile("TRDriver.apk")
	if err != nil {
		return "", 0, time.Time{}, err
	}
	st, statErr := os.Stat(path)
	if statErr != nil {
		return "", 0, time.Time{}, statErr
	}
	return path, st.Size(), st.ModTime(), nil
}

func findAndroidAppsFile(name string) (string, error) {
	for _, c := range androidAppsCandidates(name) {
		st, statErr := os.Stat(c)
		if statErr != nil || st.IsDir() || st.Size() <= 0 {
			continue
		}
		return c, nil
	}
	wd, _ := os.Getwd()
	exe, _ := os.Executable()
	return "", fmt.Errorf("%s not found (cwd=%s exe=%s)", name, wd, exe)
}

// Prefer public/apps (sideload drop folder) over dist/apps (npm build copy).
func androidAppsCandidates(name string) []string {
	wd, _ := os.Getwd()
	exe, _ := os.Executable()
	exeDir := ""
	if exe != "" {
		exeDir = filepath.Dir(exe)
	}

	rel := []string{
		filepath.Join("web", "public", "apps", name),
		filepath.Join("web", "dist", "apps", name),
		filepath.Join("dist", "android", name),
	}
	var candidates []string
	for _, r := range rel {
		candidates = append(candidates, r)
		if wd != "" {
			candidates = append(candidates, filepath.Join(wd, r))
		}
		if exeDir != "" {
			candidates = append(candidates, filepath.Join(exeDir, r))
			candidates = append(candidates, filepath.Join(filepath.Dir(exeDir), r))
		}
	}
	candidates = append(candidates,
		"/app/web/public/apps/"+name,
		"/app/web/dist/apps/"+name,
		"/app/dist/android/"+name,
	)

	seen := map[string]bool{}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = filepath.Clean(c)
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

func isAPKRequest(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".apk") ||
		p == "/download/trdriver.apk" ||
		p == "/apps/trdriver.apk"
}
