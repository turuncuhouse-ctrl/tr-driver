// Package backup implements the TR Driver disaster-recovery system.
//
// Every run captures two things into restic repositories (local disk + optional
// S3-compatible offsite):
//
//  1. a PostgreSQL custom-format dump (pg_dump -Fc) of the metadata database,
//  2. the DATA_DIR blob tree.
//
// restic gives us encryption (AES-256), deduplication, incremental snapshots,
// retention (keep-daily/weekly/monthly) and repository checks. This package adds
// the orchestration around it: pg_dump integration, daily scheduling, restore
// rehearsal (pg_restore --list + probe-file round-trip), run history in
// PostgreSQL, admin HTTP API, webhook alerts and a CLI for disaster recovery.
//
// 3-2-1 posture: live data + local restic repo + offsite S3 repo.
package backup

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config is the backup subsystem configuration. It is built from the main
// config.Config in internal/app (see backupConfigFromCfg) so this package
// stays decoupled from global config parsing.
type Config struct {
	Enabled     bool
	Dir         string // local restic repository directory (BACKUP_DIR)
	Password    string // restic repository password (BACKUP_PASSWORD, >= 16 chars)
	Schedule    string // daily run time "HH:MM" in server-local time (BACKUP_SCHEDULE)
	RunOnBoot   bool
	Timeout     time.Duration
	MaxAge      time.Duration
	KeepDaily   int
	KeepWeekly  int
	KeepMonthly int

	ResticBin    string
	PgDumpBin    string
	PgRestoreBin string

	VerifyRestore bool // optional full pg_restore rehearsal into a temp database
	WebhookURL    string

	S3Endpoint  string
	S3Region    string
	S3Bucket    string
	S3Prefix    string
	S3AccessKey string
	S3SecretKey string
}

// S3Enabled reports whether offsite replication is configured.
func (c Config) S3Enabled() bool {
	return c.S3Bucket != "" && c.S3AccessKey != "" && c.S3SecretKey != ""
}

// S3RegionOrDefault returns the configured region or the AWS default.
func (c Config) S3RegionOrDefault() string {
	if c.S3Region != "" {
		return c.S3Region
	}
	return "us-east-1"
}

// S3Repo builds the restic S3 repository location, e.g.
// "s3:s3.eu-central-1.wasabisys.com/mybucket/trdriver".
func (c Config) S3Repo() string {
	host := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(c.S3Endpoint), "https://"), "http://")
	host = strings.TrimSuffix(host, "/")
	if host == "" {
		if region := c.S3RegionOrDefault(); region == "us-east-1" {
			host = "s3.amazonaws.com"
		} else {
			host = "s3." + region + ".amazonaws.com"
		}
	}
	repo := "s3:" + host + "/" + c.S3Bucket
	if prefix := strings.Trim(c.S3Prefix, "/"); prefix != "" {
		repo += "/" + prefix
	}
	return repo
}

// S3Options returns extra restic "-o key=value" pairs for S3 backends.
func (c Config) S3Options() []string {
	endpoint := strings.TrimSpace(c.S3Endpoint)
	if endpoint == "" {
		return nil
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	return []string{"-o", "s3.endpoint=" + endpoint}
}

// S3Env returns the credential environment for restic S3 access.
// Callers must never log these values.
func (c Config) S3Env() []string {
	return []string{
		"AWS_ACCESS_KEY_ID=" + c.S3AccessKey,
		"AWS_SECRET_ACCESS_KEY=" + c.S3SecretKey,
		"AWS_REGION=" + c.S3RegionOrDefault(),
	}
}

// Validate checks the static configuration. A missing password is NOT an
// error here: the scheduler simply stays off and Status() reports the reason,
// so existing installs without backup env keep booting.
func (c Config) Validate() error {
	if c.Dir == "" {
		return fmt.Errorf("backup directory is required")
	}
	if c.Password != "" && len(c.Password) < 16 {
		return fmt.Errorf("BACKUP_PASSWORD must be at least 16 characters")
	}
	if _, _, err := ParseSchedule(c.Schedule); err != nil {
		return err
	}
	if c.KeepDaily < 0 || c.KeepWeekly < 0 || c.KeepMonthly < 0 {
		return fmt.Errorf("backup retention values must be >= 0")
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("backup timeout must be positive")
	}
	if c.MaxAge <= 0 {
		return fmt.Errorf("backup max age must be positive")
	}
	if c.ResticBin == "" || c.PgDumpBin == "" || c.PgRestoreBin == "" {
		return fmt.Errorf("backup binaries must be configured")
	}
	return nil
}

// ParseSchedule parses "HH:MM" (24h) into hour and minute.
func ParseSchedule(raw string) (hour, minute int, err error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("BACKUP_SCHEDULE must be HH:MM, got %q", raw)
	}
	hour, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("BACKUP_SCHEDULE hour must be 00-23, got %q", raw)
	}
	minute, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("BACKUP_SCHEDULE minute must be 00-59, got %q", raw)
	}
	return hour, minute, nil
}

// NextRun returns the next daily occurrence of hour:minute strictly after now,
// in now's location (server-local time; set TZ in the container for local time).
func NextRun(now time.Time, hour, minute int) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}
