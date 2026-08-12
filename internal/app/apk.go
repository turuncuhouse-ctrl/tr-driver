package app

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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

func findAndroidAPK() (path string, size int64, mod time.Time, err error) {
	wd, _ := os.Getwd()
	exe, _ := os.Executable()
	exeDir := ""
	if exe != "" {
		exeDir = filepath.Dir(exe)
	}

	rel := []string{
		filepath.Join("web", "dist", "apps", "TRDriver.apk"),
		filepath.Join("web", "public", "apps", "TRDriver.apk"),
		filepath.Join("dist", "android", "TRDriver.apk"),
	}
	var candidates []string
	for _, r := range rel {
		candidates = append(candidates, r)
		if wd != "" {
			candidates = append(candidates, filepath.Join(wd, r))
		}
		if exeDir != "" {
			candidates = append(candidates, filepath.Join(exeDir, r))
			// Binary often lives in /tmp while sources are /app
			candidates = append(candidates, filepath.Join(filepath.Dir(exeDir), r))
		}
	}
	// Portainer easy stack: /app is the repo root
	candidates = append(candidates,
		"/app/web/dist/apps/TRDriver.apk",
		"/app/web/public/apps/TRDriver.apk",
		"/app/dist/android/TRDriver.apk",
	)

	seen := map[string]bool{}
	for _, c := range candidates {
		c = filepath.Clean(c)
		if seen[c] {
			continue
		}
		seen[c] = true
		st, statErr := os.Stat(c)
		if statErr != nil || st.IsDir() || st.Size() <= 0 {
			continue
		}
		return c, st.Size(), st.ModTime(), nil
	}
	return "", 0, time.Time{}, fmt.Errorf("TRDriver.apk not found (cwd=%s exe=%s)", wd, exe)
}

func isAPKRequest(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".apk") ||
		p == "/download/trdriver.apk" ||
		p == "/apps/trdriver.apk"
}
