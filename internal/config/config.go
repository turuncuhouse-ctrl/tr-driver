package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv               string
	HTTPAddr             string
	DatabaseURL          string
	SessionSecret        string
	DataDir              string
	PublicBaseURL        string
	FreeQuotaBytes       int64
	MaxUploadBytes       int64
	MaxUploadBatchBytes  int64
	UploadChunkBytes     int64
	UploadSessionTTL     time.Duration
	MaxBatchFiles        int
	MaxConcurrentChunks  int
	MaxConcurrentUploads int
	SessionTTL           time.Duration
	DeviceTokenTTL       time.Duration
	TrashRetention       time.Duration
	FileChangeRetention  time.Duration
	ShareTokenBytes      int
	SharePasswordSalt    string
	AllowRegistration    bool
	UpdateManifestURL    string
	UpdateChannel        string
	LicenseVendorMode    bool
	// PII at-rest encryption key for contacts/SMS backup. Falls back to
	// SessionSecret; set it explicitly so rotating the session secret does not
	// make already stored personal records unreadable.
	PersonalBackupKey string
	// Encryption key for saved LocalTel SIP/gateway credentials. Falls back to
	// PersonalBackupKey (then SessionSecret) when empty.
	LocalTelKey string
	// LocalTel zero-config gateway settings. The server auto-provisions a
	// calling account per user and exposes a WSS gateway on its own domain.
	LocalTelDomain        string
	LocalTelTokenTTL      time.Duration
	LocalTelAutoProvision bool
	TrustedProxies        []netip.Prefix
	// Backup subsystem (see internal/backup + docs/BACKUP.md).
	BackupEnabled       bool
	BackupDir           string
	BackupPassword      string
	BackupSchedule      string
	BackupRunOnBoot     bool
	BackupTimeout       time.Duration
	BackupMaxAge        time.Duration
	BackupKeepDaily     int
	BackupKeepWeekly    int
	BackupKeepMonthly   int
	BackupResticBin     string
	BackupPgDumpBin     string
	BackupPgRestoreBin  string
	BackupVerifyRestore bool
	BackupWebhookURL    string
	BackupS3Endpoint    string
	BackupS3Region      string
	BackupS3Bucket      string
	BackupS3Prefix      string
	BackupS3AccessKey   string
	BackupS3SecretKey   string
	// "Ortak Paylaşım": a server-wide shared folder on its own volume. Torrent
	// downloads land here; access is governed by per-user / per-group grants.
	SharedDir              string
	SharedQuotaBytes       int64 // 0 = capacity of the filesystem holding SharedDir
	SharedQuotaWarnPercent int
	SharedLinkMaxTTL       time.Duration
	// Embedded BitTorrent client (admin only). Rate limits are in KiB/s, 0 = unlimited.
	TorrentEnabled           bool
	TorrentListenPort        int
	TorrentDownloadLimitKBps int64
	TorrentUploadLimitKBps   int64
	TorrentSeedAfterComplete bool
	TorrentDisableDHT        bool
}

func Load() (Config, error) {
	cfg := Config{
		AppEnv:        getEnv("APP_ENV", "development"),
		HTTPAddr:      getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL:   getEnv("DATABASE_URL", ""),
		SessionSecret: getEnv("SESSION_SECRET", ""),
		DataDir:       getEnv("DATA_DIR", "./data"),
		PublicBaseURL: strings.TrimRight(getEnv("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		// 0 / unset / "auto" → resolve from DATA_DIR disk capacity at startup
		FreeQuotaBytes:        getEnvQuotaBytes("FREE_QUOTA_BYTES"),
		MaxUploadBytes:        getEnvInt64("MAX_UPLOAD_BYTES", 10*1024*1024*1024),
		MaxUploadBatchBytes:   getEnvInt64("MAX_UPLOAD_BATCH_BYTES", 10*1024*1024*1024),
		UploadChunkBytes:      getEnvInt64("UPLOAD_CHUNK_BYTES", 8*1024*1024),
		UploadSessionTTL:      getEnvDuration("UPLOAD_SESSION_TTL", 48*time.Hour),
		MaxBatchFiles:         getEnvInt("MAX_BATCH_FILES", 2000),
		MaxConcurrentChunks:   getEnvInt("MAX_CONCURRENT_CHUNKS", 2),
		MaxConcurrentUploads:  getEnvInt("MAX_CONCURRENT_UPLOADS", 3),
		SessionTTL:            getEnvDuration("SESSION_TTL", 24*time.Hour),
		DeviceTokenTTL:        getEnvDuration("DEVICE_TOKEN_TTL", 0),
		TrashRetention:        getEnvDuration("TRASH_RETENTION", 720*time.Hour),
		FileChangeRetention:   getEnvDuration("FILE_CHANGE_RETENTION", 2160*time.Hour),
		ShareTokenBytes:       getEnvInt("SHARE_TOKEN_BYTES", 24),
		SharePasswordSalt:     getEnv("SHARE_PASSWORD_SALT", "change-me"),
		AllowRegistration:     getEnvBool("ALLOW_REGISTRATION", true),
		UpdateManifestURL:     getEnv("UPDATE_MANIFEST_URL", ""),
		UpdateChannel:         getEnv("UPDATE_CHANNEL", "stable"),
		LicenseVendorMode:     getEnvBool("LICENSE_VENDOR_MODE", false),
		PersonalBackupKey:     getEnv("PERSONAL_BACKUP_KEY", ""),
		LocalTelKey:           getEnv("LOCAL_TEL_KEY", ""),
		LocalTelDomain:        getEnv("LOCAL_TEL_DOMAIN", ""),
		LocalTelTokenTTL:      getEnvDuration("LOCAL_TEL_TOKEN_TTL", 15*time.Minute),
		LocalTelAutoProvision: getEnvBool("LOCAL_TEL_AUTO_PROVISION", true),
		// Defaults mirror internal/backup Default* constants.
		BackupEnabled:       getEnvBool("BACKUP_ENABLED", true),
		BackupDir:           getEnv("BACKUP_DIR", "./backups/restic"),
		BackupPassword:      getEnv("BACKUP_PASSWORD", ""),
		BackupSchedule:      getEnv("BACKUP_SCHEDULE", "02:00"),
		BackupRunOnBoot:     getEnvBool("BACKUP_RUN_ON_BOOT", false),
		BackupTimeout:       getEnvDuration("BACKUP_TIMEOUT", 6*time.Hour),
		BackupMaxAge:        getEnvDuration("BACKUP_MAX_AGE", 30*time.Hour),
		BackupKeepDaily:     getEnvInt("BACKUP_KEEP_DAILY", 7),
		BackupKeepWeekly:    getEnvInt("BACKUP_KEEP_WEEKLY", 4),
		BackupKeepMonthly:   getEnvInt("BACKUP_KEEP_MONTHLY", 6),
		BackupResticBin:     getEnv("BACKUP_RESTIC_BIN", "restic"),
		BackupPgDumpBin:     getEnv("BACKUP_PGDUMP_BIN", "pg_dump"),
		BackupPgRestoreBin:  getEnv("BACKUP_PGRESTORE_BIN", "pg_restore"),
		BackupVerifyRestore: getEnvBool("BACKUP_VERIFY_RESTORE", false),
		BackupWebhookURL:    getEnv("BACKUP_WEBHOOK_URL", ""),
		BackupS3Endpoint:    getEnv("BACKUP_S3_ENDPOINT", ""),
		BackupS3Region:      getEnv("BACKUP_S3_REGION", ""),
		BackupS3Bucket:      getEnv("BACKUP_S3_BUCKET", ""),
		BackupS3Prefix:      getEnv("BACKUP_S3_PREFIX", ""),
		BackupS3AccessKey:   getEnv("BACKUP_S3_ACCESS_KEY", ""),
		BackupS3SecretKey:   getEnv("BACKUP_S3_SECRET_KEY", ""),

		SharedDir:                getEnv("SHARED_DIR", ""),
		SharedQuotaBytes:         getEnvQuotaBytes("SHARED_QUOTA_BYTES"),
		SharedQuotaWarnPercent:   getEnvInt("SHARED_QUOTA_WARN_PERCENT", 90),
		SharedLinkMaxTTL:         getEnvDuration("SHARED_LINK_MAX_TTL", 720*time.Hour),
		TorrentEnabled:           getEnvBool("TORRENT_ENABLED", true),
		TorrentListenPort:        getEnvInt("TORRENT_LISTEN_PORT", 42069),
		TorrentDownloadLimitKBps: getEnvInt64("TORRENT_DOWNLOAD_LIMIT_KBPS", 0),
		TorrentUploadLimitKBps:   getEnvInt64("TORRENT_UPLOAD_LIMIT_KBPS", 0),
		TorrentSeedAfterComplete: getEnvBool("TORRENT_SEED_AFTER_COMPLETE", false),
		TorrentDisableDHT:        getEnvBool("TORRENT_DISABLE_DHT", false),
	}
	var err error
	cfg.TrustedProxies, err = parseTrustedProxies(getEnv("TRUSTED_PROXY_CIDRS", ""))
	if err != nil {
		return Config{}, err
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if len(cfg.SessionSecret) < 16 {
		return Config{}, fmt.Errorf("SESSION_SECRET must be at least 16 characters")
	}
	if cfg.PersonalBackupKey == "" {
		cfg.PersonalBackupKey = cfg.SessionSecret
	}
	if len(cfg.PersonalBackupKey) < 16 {
		return Config{}, fmt.Errorf("PERSONAL_BACKUP_KEY must be at least 16 characters")
	}
	if cfg.LocalTelKey == "" {
		cfg.LocalTelKey = cfg.PersonalBackupKey
	}
	if len(cfg.LocalTelKey) < 16 {
		return Config{}, fmt.Errorf("LOCAL_TEL_KEY must be at least 16 characters")
	}
	if cfg.LocalTelTokenTTL <= 0 {
		return Config{}, fmt.Errorf("LOCAL_TEL_TOKEN_TTL must be positive")
	}
	if cfg.AppEnv == "production" {
		if cfg.SessionSecret == "change-me" || strings.Contains(strings.ToLower(cfg.SessionSecret), "degistir") {
			return Config{}, fmt.Errorf("SESSION_SECRET looks like a placeholder; set a strong secret in production")
		}
		if cfg.SharePasswordSalt == "change-me" || len(cfg.SharePasswordSalt) < 16 {
			return Config{}, fmt.Errorf("SHARE_PASSWORD_SALT must be at least 16 characters in production")
		}
	}
	if err := cfg.resolveShared(); err != nil {
		return Config{}, err
	}
	// A missing backup password only disables the scheduler (existing installs
	// keep booting); a present-but-weak one is a misconfiguration.
	if cfg.BackupPassword != "" && len(cfg.BackupPassword) < 16 {
		return Config{}, fmt.Errorf("BACKUP_PASSWORD must be at least 16 characters")
	}
	return cfg, nil
}

// DefaultSharedDir places the shared folder next to DATA_DIR, so the stock
// container layout (/data/files) yields /data/paylasim on its own volume.
func DefaultSharedDir(dataDir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(dataDir)), "paylasim")
}

// resolveShared fills defaults for the shared space and rejects layouts where
// the shared folder and the blob store overlap: one would then expose (or
// garbage-collect) the other's files.
func (cfg *Config) resolveShared() error {
	if cfg.SharedDir == "" {
		cfg.SharedDir = DefaultSharedDir(cfg.DataDir)
	}
	shared, err := filepath.Abs(cfg.SharedDir)
	if err != nil {
		return fmt.Errorf("SHARED_DIR: %w", err)
	}
	data, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("DATA_DIR: %w", err)
	}
	if pathContains(shared, data) || pathContains(data, shared) {
		return fmt.Errorf("SHARED_DIR (%s) and DATA_DIR (%s) must not contain each other", shared, data)
	}
	if cfg.SharedQuotaWarnPercent < 1 || cfg.SharedQuotaWarnPercent > 99 {
		return fmt.Errorf("SHARED_QUOTA_WARN_PERCENT must be between 1 and 99")
	}
	if cfg.SharedLinkMaxTTL <= 0 {
		return fmt.Errorf("SHARED_LINK_MAX_TTL must be positive")
	}
	if cfg.TorrentListenPort < 0 || cfg.TorrentListenPort > 65535 {
		return fmt.Errorf("TORRENT_LISTEN_PORT must be between 0 and 65535")
	}
	if cfg.TorrentDownloadLimitKBps < 0 || cfg.TorrentUploadLimitKBps < 0 {
		return fmt.Errorf("TORRENT_*_LIMIT_KBPS must not be negative")
	}
	return nil
}

// pathContains reports whether child equals parent or lives below it.
func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func getEnvBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// getEnvQuotaBytes: empty, "0", or "auto" means derive from DATA_DIR disk size.
func getEnvQuotaBytes(key string) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" || strings.EqualFold(value, "auto") {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: invalid CIDR %q", part)
		}
		out = append(out, prefix.Masked())
	}
	return out, nil
}
