package backup

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults shared by the server config mapper and the disaster-recovery CLI.
const (
	DefaultDir          = "./backups/restic"
	DefaultSchedule     = "02:00"
	DefaultKeepDaily    = 7
	DefaultKeepWeekly   = 4
	DefaultKeepMonthly  = 6
	DefaultTimeout      = 6 * time.Hour
	DefaultMaxAge       = 30 * time.Hour
	DefaultResticBin    = "restic"
	DefaultPgDumpBin    = "pg_dump"
	DefaultPgRestoreBin = "pg_restore"
)

// ConfigFromEnv builds a Config purely from environment variables. The server
// uses config.Load + app mapping instead; the CLI uses this so disaster
// recovery (snapshots/restore) works on a machine without a live database.
func ConfigFromEnv() Config {
	return Config{
		Enabled:       envBool("BACKUP_ENABLED", true),
		Dir:           envString("BACKUP_DIR", DefaultDir),
		Password:      strings.TrimSpace(os.Getenv("BACKUP_PASSWORD")),
		Schedule:      envString("BACKUP_SCHEDULE", DefaultSchedule),
		RunOnBoot:     envBool("BACKUP_RUN_ON_BOOT", false),
		Timeout:       envDuration("BACKUP_TIMEOUT", DefaultTimeout),
		MaxAge:        envDuration("BACKUP_MAX_AGE", DefaultMaxAge),
		KeepDaily:     envInt("BACKUP_KEEP_DAILY", DefaultKeepDaily),
		KeepWeekly:    envInt("BACKUP_KEEP_WEEKLY", DefaultKeepWeekly),
		KeepMonthly:   envInt("BACKUP_KEEP_MONTHLY", DefaultKeepMonthly),
		ResticBin:     envString("BACKUP_RESTIC_BIN", DefaultResticBin),
		PgDumpBin:     envString("BACKUP_PGDUMP_BIN", DefaultPgDumpBin),
		PgRestoreBin:  envString("BACKUP_PGRESTORE_BIN", DefaultPgRestoreBin),
		VerifyRestore: envBool("BACKUP_VERIFY_RESTORE", false),
		WebhookURL:    strings.TrimSpace(os.Getenv("BACKUP_WEBHOOK_URL")),
		S3Endpoint:    strings.TrimSpace(os.Getenv("BACKUP_S3_ENDPOINT")),
		S3Region:      strings.TrimSpace(os.Getenv("BACKUP_S3_REGION")),
		S3Bucket:      strings.TrimSpace(os.Getenv("BACKUP_S3_BUCKET")),
		S3Prefix:      strings.TrimSpace(os.Getenv("BACKUP_S3_PREFIX")),
		S3AccessKey:   strings.TrimSpace(os.Getenv("BACKUP_S3_ACCESS_KEY")),
		S3SecretKey:   strings.TrimSpace(os.Getenv("BACKUP_S3_SECRET_KEY")),
	}
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envInt(key string, fallback int) int {
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

func envDuration(key string, fallback time.Duration) time.Duration {
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
