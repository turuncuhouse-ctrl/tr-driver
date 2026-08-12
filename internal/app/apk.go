package app

import (
	"net/http"
	"os"
	"path/filepath"
)

func serveAndroidAPK(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	candidates := []string{
		filepath.Join("web", "dist", "apps", "TRDriver.apk"),
		filepath.Join("web", "public", "apps", "TRDriver.apk"),
		filepath.Join("dist", "android", "TRDriver.apk"),
	}
	var path string
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Size() > 0 {
			path = c
			break
		}
	}
	if path == "" {
		http.Error(w, "TRDriver.apk not found — rebuild Android release and copy to web/public/apps/", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="TRDriver.apk"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}
