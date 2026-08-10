package app

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"necipdrive/internal/access"
	"necipdrive/internal/admin"
	"necipdrive/internal/auth"
	"necipdrive/internal/collab"
	"necipdrive/internal/config"
	"necipdrive/internal/drives"
	"necipdrive/internal/files"
	"necipdrive/internal/license"
	"necipdrive/internal/plans"
	"necipdrive/internal/shares"
	"necipdrive/internal/storage"
	"necipdrive/internal/store"
	"necipdrive/internal/syncapi"
	"necipdrive/internal/uploads"
)

func New(ctx context.Context, cfg config.Config) (*http.Server, func(), error) {
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := store.Migrate(ctx, db, cfg.FreeQuotaBytes); err != nil {
		db.Close()
		return nil, nil, err
	}

	fileStorage, err := storage.NewLocal(cfg.DataDir)
	if err != nil {
		db.Close()
		return nil, nil, err
	}

	accessSvc := access.New(db)
	licenseService := license.NewService(db, cfg.AllowRegistration)
	authService := auth.NewService(db, cfg, licenseService)
	adminService := admin.NewService(db)
	fileService := files.NewService(db, fileStorage, cfg, accessSvc)
	planService := plans.NewService(db)
	shareService := shares.NewService(db, cfg, accessSvc)
	uploadService := uploads.NewService(db, fileStorage, cfg)
	syncService := syncapi.NewService(db, fileService)
	driveService := drives.NewService(db, accessSvc)
	collabService := collab.NewService(db, accessSvc, fileStorage)

	_ = uploadService.CleanupExpired(ctx)
	_ = fileService.CleanupTrash(ctx, cfg.TrashRetention)
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-cleanupCtx.Done():
				return
			case <-ticker.C:
				if err := uploadService.CleanupExpired(cleanupCtx); err != nil {
					log.Printf("upload cleanup: %v", err)
				}
				if err := fileService.CleanupTrash(cleanupCtx, cfg.TrashRetention); err != nil {
					log.Printf("trash cleanup: %v", err)
				}
			}
		}
	}()

	router, err := NewRouter(cfg, authService, adminService, fileService, planService, shareService, uploadService, syncService, driveService, collabService, licenseService)
	if err != nil {
		cleanupCancel()
		db.Close()
		return nil, nil, err
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	return server, func() {
		cleanupCancel()
		db.Close()
	}, nil
}

func staticFS() (fs.FS, error) {
	info, err := os.Stat("web/dist/index.html")
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fs.ErrNotExist
	}
	return os.DirFS("web/dist"), nil
}
