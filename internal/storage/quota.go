package storage

import (
	"fmt"
	"os"
)

const fallbackQuotaBytes int64 = 5 * 1024 * 1024 * 1024 // 5 GiB if disk probe fails

// ResolveDefaultQuota returns configuredQuota when > 0; otherwise the total
// capacity of the filesystem that holds dataDir.
func ResolveDefaultQuota(dataDir string, configuredQuota int64) int64 {
	if configuredQuota > 0 {
		return configuredQuota
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fallbackQuotaBytes
	}
	total, _, err := DiskSpace(dataDir)
	if err != nil || total <= 0 {
		return fallbackQuotaBytes
	}
	return total
}

// FormatQuotaHint is a short human string for logs.
func FormatQuotaHint(bytes int64) string {
	const gib = 1024 * 1024 * 1024
	if bytes >= gib {
		return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(gib))
	}
	return fmt.Sprintf("%d B", bytes)
}
