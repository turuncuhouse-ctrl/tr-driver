package app

import (
	"io/fs"
	"net/http"
	"strings"

	"necipdrive/internal/admin"
	"necipdrive/internal/auth"
	"necipdrive/internal/collab"
	"necipdrive/internal/config"
	"necipdrive/internal/drives"
	"necipdrive/internal/files"
	"necipdrive/internal/httpx"
	"necipdrive/internal/license"
	"necipdrive/internal/plans"
	"necipdrive/internal/shares"
	"necipdrive/internal/syncapi"
	"necipdrive/internal/updates"
	"necipdrive/internal/uploads"
	"necipdrive/internal/version"
)

func NewRouter(
	cfg config.Config,
	authService *auth.Service,
	adminService *admin.Service,
	fileService *files.Service,
	planService *plans.Service,
	shareService *shares.Service,
	uploadService *uploads.Service,
	syncService *syncapi.Service,
	driveService *drives.Service,
	collabService *collab.Service,
	licenseService *license.Service,
) (http.Handler, error) {
	mux := http.NewServeMux()

	authHandler := auth.NewHandler(authService)
	adminHandler := admin.NewHandler(adminService, cfg.MaxUploadBatchBytes, cfg.UploadChunkBytes)
	fileHandler := files.NewHandler(fileService)
	planHandler := plans.NewHandler(planService)
	shareHandler := shares.NewHandler(shareService, fileService)
	uploadHandler := uploads.NewHandler(uploadService)
	syncHandler := syncapi.NewHandler(syncService)
	driveHandler := drives.NewHandler(driveService)
	collabHandler := collab.NewHandler(collabService)
	licenseHandler := license.NewHandler(licenseService)
	updateHandler := updates.NewHandler(updates.Config{ManifestURL: cfg.UpdateManifestURL, Channel: cfg.UpdateChannel})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "product": version.ProductName, "version": version.Version})
	})
	mux.Handle("/api/license", http.HandlerFunc(licenseHandler.PublicStatus))
	mux.Handle("/api/updates/check", http.HandlerFunc(updateHandler.Check))
	mux.Handle("/api/admin/license", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(licenseHandler.Admin))))
	mux.Handle("/api/auth/register", http.HandlerFunc(authHandler.Register))
	mux.Handle("/api/auth/login", http.HandlerFunc(authHandler.Login))
	mux.Handle("/api/auth/device-login", http.HandlerFunc(authHandler.DeviceLogin))
	mux.Handle("/api/auth/device-logout", authHandler.RequireAuth(http.HandlerFunc(authHandler.DeviceLogout)))
	mux.Handle("/api/auth/logout", authHandler.RequireAuth(http.HandlerFunc(authHandler.Logout)))
	mux.Handle("/api/auth/me", authHandler.RequireAuth(http.HandlerFunc(authHandler.Me)))
	mux.Handle("/api/auth/devices", authHandler.RequireSession(http.HandlerFunc(authHandler.Devices)))
	mux.Handle("/api/auth/devices/", authHandler.RequireSession(http.HandlerFunc(authHandler.Device)))
	mux.Handle("/api/plans", authHandler.RequireAuth(http.HandlerFunc(planHandler.List)))
	mux.Handle("/api/plans/assign", authHandler.RequireAuth(http.HandlerFunc(planHandler.Assign)))
	mux.Handle("/api/admin/summary", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.Summary))))
	mux.Handle("/api/admin/users", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.Users))))
	mux.Handle("/api/admin/users/plan", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.SetPlan))))
	mux.Handle("/api/admin/users/quota", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.SetQuota))))
	mux.Handle("/api/admin/users/role", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.SetRole))))
	mux.Handle("/api/admin/settings", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.Settings))))
	mux.Handle("/api/files", authHandler.RequireAuth(http.HandlerFunc(fileHandler.ListOrCreateFolder)))
	mux.Handle("/api/files/upload", authHandler.RequireAuth(http.HandlerFunc(fileHandler.Upload)))
	mux.Handle("/api/files/move", authHandler.RequireAuth(http.HandlerFunc(fileHandler.Move)))
	mux.Handle("/api/files/rename", authHandler.RequireAuth(http.HandlerFunc(fileHandler.Rename)))
	mux.Handle("/api/files/delete", authHandler.RequireAuth(http.HandlerFunc(fileHandler.Delete)))
	mux.Handle("/api/files/download/", authHandler.RequireAuth(http.HandlerFunc(fileHandler.Download)))
	mux.Handle("/api/files/search", authHandler.RequireAuth(http.HandlerFunc(collabHandler.Search)))
	mux.Handle("/api/files/starred", authHandler.RequireAuth(http.HandlerFunc(collabHandler.Starred)))
	mux.Handle("/api/files/recent", authHandler.RequireAuth(http.HandlerFunc(collabHandler.Recent)))
	mux.Handle("/api/files/comments", authHandler.RequireAuth(http.HandlerFunc(collabHandler.Comments)))
	mux.Handle("/api/files/versions", authHandler.RequireAuth(http.HandlerFunc(collabHandler.Versions)))
	mux.Handle("/api/files/versions/", authHandler.RequireAuth(http.HandlerFunc(collabHandler.Versions)))
	mux.Handle("/api/permissions", authHandler.RequireAuth(http.HandlerFunc(collabHandler.Permissions)))
	mux.Handle("/api/shared-with-me", authHandler.RequireAuth(http.HandlerFunc(collabHandler.SharedWithMe)))
	mux.Handle("/api/activities", authHandler.RequireAuth(http.HandlerFunc(collabHandler.Activities)))
	mux.Handle("/api/notifications", authHandler.RequireAuth(http.HandlerFunc(collabHandler.Notifications)))
	mux.Handle("/api/drives", authHandler.RequireAuth(http.HandlerFunc(driveHandler.ListOrCreate)))
	mux.Handle("/api/drives/", authHandler.RequireAuth(http.HandlerFunc(driveHandler.Drive)))
	mux.Handle("/api/sync/snapshot", authHandler.RequireAuth(http.HandlerFunc(syncHandler.Snapshot)))
	mux.Handle("/api/sync/changes", authHandler.RequireAuth(http.HandlerFunc(syncHandler.Changes)))
	mux.Handle("/api/sync/folders", authHandler.RequireAuth(http.HandlerFunc(syncHandler.Folder)))
	mux.Handle("/api/sync/rename", authHandler.RequireAuth(http.HandlerFunc(syncHandler.Rename)))
	mux.Handle("/api/sync/move", authHandler.RequireAuth(http.HandlerFunc(syncHandler.Move)))
	mux.Handle("/api/sync/trash", authHandler.RequireAuth(http.HandlerFunc(syncHandler.Trash)))
	mux.Handle("/api/sync/restore", authHandler.RequireAuth(http.HandlerFunc(syncHandler.Restore)))
	mux.Handle("/api/sync/purge", authHandler.RequireAuth(http.HandlerFunc(syncHandler.Purge)))
	mux.Handle("/api/trash", authHandler.RequireAuth(http.HandlerFunc(syncHandler.TrashList)))
	mux.Handle("/api/uploads/batches", authHandler.RequireAuth(http.HandlerFunc(uploadHandler.Batches)))
	mux.Handle("/api/uploads/batches/", authHandler.RequireAuth(http.HandlerFunc(uploadHandler.AbortBatch)))
	mux.Handle("/api/uploads/files/", authHandler.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/complete") {
			uploadHandler.CompleteFile(w, r)
			return
		}
		uploadHandler.AppendChunk(w, r)
	})))
	mux.Handle("/api/shares", authHandler.RequireAuth(http.HandlerFunc(shareHandler.API)))
	mux.Handle("/s/", http.HandlerFunc(shareHandler.DownloadPublic))

	staticFiles, err := staticFS()
	if err == nil {
		fileServer := http.FileServer(http.FS(staticFiles))
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/s/") || r.URL.Path == "/healthz" {
				http.NotFound(w, r)
				return
			}
			path := strings.TrimPrefix(r.URL.Path, "/")
			if path != "" {
				if _, err := fs.Stat(staticFiles, path); err != nil {
					r.URL.Path = "/"
				}
			}
			fileServer.ServeHTTP(w, r)
		}))
	}

	return securityHeaders(csrfMiddleware(loggingMiddleware(mux), cfg.SessionSecret)), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}
