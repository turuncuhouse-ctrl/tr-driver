package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv              string
	HTTPAddr            string
	DatabaseURL         string
	SessionSecret       string
	DataDir             string
	PublicBaseURL       string
	FreeQuotaBytes      int64
	MaxUploadBytes      int64
	MaxUploadBatchBytes int64
	UploadChunkBytes    int64
	UploadSessionTTL    time.Duration
	MaxBatchFiles       int
	MaxConcurrentChunks int
	SessionTTL          time.Duration
	DeviceTokenTTL      time.Duration
	TrashRetention      time.Duration
	ShareTokenBytes     int
	SharePasswordSalt   string
	AllowRegistration   bool
	UpdateManifestURL   string
	UpdateChannel       string
}

func Load() (Config, error) {
	cfg := Config{
		AppEnv:              getEnv("APP_ENV", "development"),
		HTTPAddr:            getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL:         getEnv("DATABASE_URL", ""),
		SessionSecret:       getEnv("SESSION_SECRET", ""),
		DataDir:             getEnv("DATA_DIR", "./data"),
		PublicBaseURL:       strings.TrimRight(getEnv("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		FreeQuotaBytes:      getEnvInt64("FREE_QUOTA_BYTES", 5*1024*1024*1024),
		MaxUploadBytes:      getEnvInt64("MAX_UPLOAD_BYTES", 10*1024*1024*1024),
		MaxUploadBatchBytes: getEnvInt64("MAX_UPLOAD_BATCH_BYTES", 10*1024*1024*1024),
		UploadChunkBytes:    getEnvInt64("UPLOAD_CHUNK_BYTES", 8*1024*1024),
		UploadSessionTTL:    getEnvDuration("UPLOAD_SESSION_TTL", 48*time.Hour),
		MaxBatchFiles:       getEnvInt("MAX_BATCH_FILES", 2000),
		MaxConcurrentChunks: getEnvInt("MAX_CONCURRENT_CHUNKS", 2),
		SessionTTL:          getEnvDuration("SESSION_TTL", 24*time.Hour),
		DeviceTokenTTL:      getEnvDuration("DEVICE_TOKEN_TTL", 720*time.Hour),
		TrashRetention:      getEnvDuration("TRASH_RETENTION", 720*time.Hour),
		ShareTokenBytes:     getEnvInt("SHARE_TOKEN_BYTES", 24),
		SharePasswordSalt:   getEnv("SHARE_PASSWORD_SALT", "change-me"),
		AllowRegistration:   getEnvBool("ALLOW_REGISTRATION", true),
		UpdateManifestURL:   getEnv("UPDATE_MANIFEST_URL", ""),
		UpdateChannel:       getEnv("UPDATE_CHANNEL", "stable"),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if len(cfg.SessionSecret) < 16 {
		return Config{}, fmt.Errorf("SESSION_SECRET must be at least 16 characters")
	}
	if cfg.AppEnv == "production" {
		if cfg.SessionSecret == "change-me" || strings.Contains(strings.ToLower(cfg.SessionSecret), "degistir") {
			return Config{}, fmt.Errorf("SESSION_SECRET looks like a placeholder; set a strong secret in production")
		}
		if cfg.SharePasswordSalt == "change-me" || len(cfg.SharePasswordSalt) < 16 {
			return Config{}, fmt.Errorf("SHARE_PASSWORD_SALT must be at least 16 characters in production")
		}
	}
	return cfg, nil
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
