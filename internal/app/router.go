package app

import (
	"io/fs"
	"log"
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
	"necipdrive/internal/mailer"
	"necipdrive/internal/plans"
	"necipdrive/internal/shares"
	"necipdrive/internal/syncapi"
	"necipdrive/internal/updates"
	"necipdrive/internal/uploads"
	"necipdrive/internal/version"
	"necipdrive/internal/webdavx"
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
	mailService *mailer.Service,
) (http.Handler, error) {
	mux := http.NewServeMux()

	authHandler := auth.NewHandler(authService)
	adminHandler := admin.NewHandler(adminService, cfg.MaxUploadBatchBytes, cfg.UploadChunkBytes)
	mailHandler := admin.NewMailHandler(mailService)
	fileHandler := files.NewHandler(fileService)
	planHandler := plans.NewHandler(planService)
	shareHandler := shares.NewHandler(shareService, fileService, mailService, cfg)
	uploadHandler := uploads.NewHandler(uploadService)
	syncHandler := syncapi.NewHandler(syncService)
	driveHandler := drives.NewHandler(driveService)
	collabHandler := collab.NewHandler(collabService)
	licenseHandler := license.NewHandler(licenseService)
	updateHandler := updates.NewHandler(updates.Config{ManifestURL: cfg.UpdateManifestURL, Channel: cfg.UpdateChannel})
	davHandler := webdavx.New(authService, fileService)

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "product": version.ProductName, "version": version.Version})
	})
	mux.Handle("/api/license", http.HandlerFunc(licenseHandler.PublicStatus))
	mux.Handle("/api/updates/check", http.HandlerFunc(updateHandler.Check))
	adminLicense := authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(licenseHandler.Admin)))
	mux.Handle("/api/admin/license", adminLicense)
	mux.Handle("/api/admin/license/", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(licenseHandler.AdminPath))))
	mux.Handle("/api/auth/register", http.HandlerFunc(authHandler.Register))
	mux.Handle("/api/auth/login", http.HandlerFunc(authHandler.Login))
	mux.Handle("/api/auth/login/2fa", http.HandlerFunc(authHandler.Login2FA))
	mux.Handle("/api/auth/forgot-password", http.HandlerFunc(authHandler.ForgotPassword))
	mux.Handle("/api/auth/reset-password", http.HandlerFunc(authHandler.ResetPassword))
	mux.Handle("/api/auth/device-login", http.HandlerFunc(authHandler.DeviceLogin))
	mux.Handle("/api/auth/device-logout", authHandler.RequireAuth(http.HandlerFunc(authHandler.DeviceLogout)))
	mux.Handle("/api/auth/qr/create", authHandler.RequireSession(http.HandlerFunc(authHandler.CreateQRLogin)))
	mux.Handle("/api/auth/qr/redeem", http.HandlerFunc(authHandler.RedeemQRLogin))
	mux.Handle("/api/auth/logout", authHandler.RequireAuth(http.HandlerFunc(authHandler.Logout)))
	mux.Handle("/api/auth/me", authHandler.RequireAuth(http.HandlerFunc(authHandler.Me)))
	mux.Handle("/api/auth/security", authHandler.RequireSession(http.HandlerFunc(authHandler.Security)))
	mux.Handle("/api/auth/devices", authHandler.RequireSession(http.HandlerFunc(authHandler.Devices)))
	mux.Handle("/api/auth/devices/", authHandler.RequireSession(http.HandlerFunc(authHandler.Device)))
	mux.Handle("/api/plans", authHandler.RequireAuth(http.HandlerFunc(planHandler.List)))
	mux.Handle("/api/plans/assign", authHandler.RequireAuth(http.HandlerFunc(planHandler.Assign)))
	mux.Handle("/api/admin/summary", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.Summary))))
	mux.Handle("/api/admin/users", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.Users))))
	mux.Handle("/api/admin/users/plan", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.SetPlan))))
	mux.Handle("/api/admin/users/quota", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.SetQuota))))
	mux.Handle("/api/admin/users/bonus-quota", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.SetBonusQuota))))
	mux.Handle("/api/admin/users/role", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.SetRole))))
	mux.Handle("/api/admin/settings", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(adminHandler.Settings))))
	mux.Handle("/api/admin/mail", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(mailHandler.Settings))))
	mux.Handle("/api/admin/mail/test", authHandler.RequireAuth(adminHandler.RequireAdmin(http.HandlerFunc(mailHandler.Test))))
	mux.Handle("/api/mail/status", authHandler.RequireAuth(http.HandlerFunc(mailHandler.Status)))
	mux.Handle("/api/files", authHandler.RequireAuth(http.HandlerFunc(fileHandler.ListOrCreateFolder)))
	mux.Handle("/api/files/upload", authHandler.RequireAuth(http.HandlerFunc(fileHandler.Upload)))
	mux.Handle("/api/files/upload-pace", authHandler.RequireAuth(http.HandlerFunc(fileHandler.UploadPace)))
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
	mux.Handle("/api/shares/email", authHandler.RequireAuth(http.HandlerFunc(shareHandler.EmailLink)))
	mux.Handle("/s/", http.HandlerFunc(shareHandler.DownloadPublic))
	mux.Handle("/dav", davHandler)
	mux.Handle("/dav/", davHandler)

	mux.HandleFunc("/download/TRDriver.apk", serveAndroidAPK)
	mux.HandleFunc("/apps/TRDriver.apk", serveAndroidAPK)
	mux.HandleFunc("/api/android/version", serveAndroidVersion)

	staticFiles, err := staticFS()
	if err == nil {
		fileServer := http.FileServer(http.FS(staticFiles))
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Never SPA-fallback APK routes to index.html (causes 1KB fake installers).
			if isAPKRequest(r.URL.Path) {
				serveAndroidAPK(w, r)
				return
			}
			// Unmatched /api/* must never look like a generic Go 404; force redeploy messages.
			if strings.HasPrefix(r.URL.Path, "/api/") {
				httpx.Error(w, http.StatusNotFound, "api route not found: "+r.URL.Path+" — sunucu binary eski olabilir; container'ı yeniden build/redeploy edin")
				return
			}
			if strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/dav") || r.URL.Path == "/healthz" {
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
	} else {
		log.Printf("static UI not found (web/dist); API-only mode")
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if isAPKRequest(r.URL.Path) {
				serveAndroidAPK(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/") {
				httpx.Error(w, http.StatusNotFound, "api route not found: "+r.URL.Path)
				return
			}
			http.NotFound(w, r)
		})
	}

	return securityHeaders(csrfMiddleware(loggingMiddleware(mux), cfg.SessionSecret)), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
