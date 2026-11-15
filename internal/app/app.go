package app

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"necipdrive/internal/access"
	"necipdrive/internal/admin"
	"necipdrive/internal/auth"
	"necipdrive/internal/backup"
	"necipdrive/internal/collab"
	"necipdrive/internal/config"
	"necipdrive/internal/drives"
	"necipdrive/internal/files"
	"necipdrive/internal/license"
	"necipdrive/internal/loadpace"
	"necipdrive/internal/localtel"
	"necipdrive/internal/mailer"
	"necipdrive/internal/personalbackup"
	"necipdrive/internal/plans"
	"necipdrive/internal/shares"
	"necipdrive/internal/storage"
	"necipdrive/internal/store"
	"necipdrive/internal/syncapi"
	"necipdrive/internal/uploads"
)

func New(ctx context.Context, cfg config.Config) (*http.Server, func(), error) {
	cfg.FreeQuotaBytes = storage.ResolveDefaultQuota(cfg.DataDir, cfg.FreeQuotaBytes)
	log.Printf("default quota: %s (%d bytes) for DATA_DIR=%s", storage.FormatQuotaHint(cfg.FreeQuotaBytes), cfg.FreeQuotaBytes, cfg.DataDir)

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := store.Migrate(ctx, db, cfg.FreeQuotaBytes, cfg.DeviceTokenTTL); err != nil {
		db.Close()
		return nil, nil, err
	}

	fileStorage, err := storage.NewLocal(cfg.DataDir)
	if err != nil {
		db.Close()
		return nil, nil, err
	}

	accessSvc := access.New(db)
	licenseService := license.NewService(db, cfg.AllowRegistration, cfg.LicenseVendorMode)
	authService := auth.NewService(db, cfg, licenseService)
	adminService := admin.NewService(db, cfg.DataDir, cfg.FreeQuotaBytes)
	mailService := mailer.New(db)
	authService.SetMailer(mailService)
	pace := loadpace.New(cfg.MaxConcurrentUploads)
	fileService := files.NewService(db, fileStorage, cfg, accessSvc, pace)
	planService := plans.NewService(db)
	shareService := shares.NewService(db, cfg, accessSvc)
	uploadService := uploads.NewService(db, fileStorage, cfg, pace)
	syncService := syncapi.NewService(db, fileService)
	driveService := drives.NewService(db, accessSvc)
	collabService := collab.NewService(db, accessSvc, fileStorage)
	backupService := backup.NewService(db, backupConfigFromCfg(cfg), cfg.DataDir, cfg.DatabaseURL)
	personalVault, err := personalbackup.NewVault(cfg.PersonalBackupKey)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	personalService, err := personalbackup.NewService(db, personalVault)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	localtelVault, err := localtel.NewVault(cfg.LocalTelKey)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	localtelService, err := localtel.NewService(db, localtelVault)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	localTelDomain := cfg.LocalTelDomain
	if localTelDomain == "" {
		if publicURL, parseErr := url.Parse(cfg.PublicBaseURL); parseErr == nil {
			localTelDomain = publicURL.Host
		}
	}
	localtelService.Configure(localTelDomain, cfg.LocalTelTokenTTL, cfg.LocalTelAutoProvision)
	localtelHub := localtel.NewHub()

	_ = uploadService.CleanupExpired(ctx)
	_ = fileService.CleanupTrash(ctx, cfg.TrashRetention)
	_ = fileService.CleanupTemp(0)
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
				if err := fileService.CleanupTemp(24 * time.Hour); err != nil {
					log.Printf("temp cleanup: %v", err)
				}
				if cfg.FileChangeRetention > 0 {
					if _, err := db.Exec(cleanupCtx, `delete from file_changes where created_at < now() - $1::interval`, cfg.FileChangeRetention.String()); err != nil {
						log.Printf("file_changes cleanup: %v", err)
					}
				}
			}
		}
	}()

	backupService.Start(cleanupCtx)

	router, err := NewRouter(cfg, authService, adminService, fileService, planService, shareService, uploadService, syncService, driveService, collabService, licenseService, mailService, pace, backupService, personalService, localtelService, localtelHub)
	if err != nil {
		cleanupCancel()
		db.Close()
		return nil, nil, err
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := db.Ping(ctx); err != nil {
				http.Error(w, "database not ready", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ready"}`))
			return
		}
		router.ServeHTTP(w, r)
	})
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	return server, func() {
		cleanupCancel()
		db.Close()
	}, nil
}

// backupConfigFromCfg maps global config onto the backup subsystem config.
func backupConfigFromCfg(cfg config.Config) backup.Config {
	return backup.Config{
		Enabled:       cfg.BackupEnabled,
		Dir:           cfg.BackupDir,
		Password:      cfg.BackupPassword,
		Schedule:      cfg.BackupSchedule,
		RunOnBoot:     cfg.BackupRunOnBoot,
		Timeout:       cfg.BackupTimeout,
		MaxAge:        cfg.BackupMaxAge,
		KeepDaily:     cfg.BackupKeepDaily,
		KeepWeekly:    cfg.BackupKeepWeekly,
		KeepMonthly:   cfg.BackupKeepMonthly,
		ResticBin:     cfg.BackupResticBin,
		PgDumpBin:     cfg.BackupPgDumpBin,
		PgRestoreBin:  cfg.BackupPgRestoreBin,
		VerifyRestore: cfg.BackupVerifyRestore,
		WebhookURL:    cfg.BackupWebhookURL,
		S3Endpoint:    cfg.BackupS3Endpoint,
		S3Region:      cfg.BackupS3Region,
		S3Bucket:      cfg.BackupS3Bucket,
		S3Prefix:      cfg.BackupS3Prefix,
		S3AccessKey:   cfg.BackupS3AccessKey,
		S3SecretKey:   cfg.BackupS3SecretKey,
	}
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
