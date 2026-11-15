package sharedspace

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// Quota levels reported to clients.
const (
	QuotaOK      = "ok"
	QuotaWarning = "warning"
	QuotaFull    = "full"
)

// diskReserveBytes is never handed out: the database or the blob store may sit
// on the same disk, and a completely full disk takes the whole server down.
const diskReserveBytes int64 = 64 << 20

const (
	settingQuotaBytes  = "shared_quota_bytes"
	settingWarnPercent = "shared_quota_warn_percent"
	settingNotifiedLvl = "shared_quota_notified_level"
	usageCacheTTL      = 30 * time.Second
	quotaMonitorPeriod = time.Minute
	notificationKind   = "shared_quota"
	defaultWarnPercent = 90
	minWarnPercent     = 1
	maxWarnPercent     = 99
)

// QuotaStatus describes how full the shared folder is.
type QuotaStatus struct {
	// QuotaBytes is the configured limit, or 0 when the folder is only bounded
	// by the disk it lives on.
	QuotaBytes int64 `json:"quotaBytes"`
	// LimitBytes is the limit actually in force: the configured quota, or what
	// the folder could hold on this disk when no quota is set.
	LimitBytes int64 `json:"limitBytes"`
	// FreeBytes is what can still be added: the smaller of the room left in
	// the quota and the room left on the disk.
	UsedBytes      int64 `json:"usedBytes"`
	FreeBytes      int64 `json:"freeBytes"`
	DiskTotalBytes int64 `json:"diskTotalBytes"`
	DiskFreeBytes  int64 `json:"diskFreeBytes"`
	// UsedPercent is the fill level the warning is based on: the share of the
	// quota in use or the share of the disk in use, whichever is higher.
	UsedPercent int    `json:"usedPercent"`
	WarnPercent int    `json:"warnPercent"`
	Level       string `json:"level"`
	Message     string `json:"message,omitempty"`
}

// evaluateQuota is the pure decision: given the numbers, how full are we?
// diskFree < 0 means the disk could not be probed and is ignored.
func evaluateQuota(quota, used, diskTotal, diskFree int64, warnPercent int) QuotaStatus {
	if warnPercent < minWarnPercent || warnPercent > maxWarnPercent {
		warnPercent = defaultWarnPercent
	}
	if used < 0 {
		used = 0
	}
	st := QuotaStatus{
		QuotaBytes:     quota,
		UsedBytes:      used,
		DiskTotalBytes: max(diskTotal, 0),
		DiskFreeBytes:  max(diskFree, 0),
		WarnPercent:    warnPercent,
		Level:          QuotaOK,
	}
	diskAvail := int64(-1)
	if diskFree >= 0 {
		diskAvail = max(diskFree-diskReserveBytes, 0)
	}
	if quota <= 0 && diskAvail < 0 {
		// No quota and no disk information: nothing to enforce.
		st.FreeBytes = 1 << 62
		return st
	}

	// Two bounds apply and the tighter one wins: the configured quota, and the
	// disk the folder lives on (which other data may be filling up).
	st.FreeBytes = 1 << 62
	if quota > 0 {
		st.LimitBytes = quota
		st.FreeBytes = max(quota-used, 0)
		st.UsedPercent = int(min(100, used*100/quota))
	}
	if diskAvail >= 0 {
		st.FreeBytes = min(st.FreeBytes, diskAvail)
		if quota <= 0 {
			st.LimitBytes = used + diskAvail
		}
		if diskTotal > 0 {
			diskPercent := int(min(100, max(diskTotal-diskAvail, 0)*100/diskTotal))
			st.UsedPercent = max(st.UsedPercent, diskPercent)
		}
	}
	switch {
	case st.FreeBytes <= 0:
		st.Level = QuotaFull
		st.UsedPercent = 100
		st.Message = "Ortak Paylaşım alanı doldu. Yer açılana kadar yeni yükleme ve torrent indirmesi yapılamaz."
	case st.UsedPercent >= warnPercent:
		st.Level = QuotaWarning
		st.Message = fmt.Sprintf("Ortak Paylaşım alanı dolmak üzere: %%%d dolu, %s boş.", st.UsedPercent, formatBytes(st.FreeBytes))
	}
	return st
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	for _, suffix := range []string{"KB", "MB", "GB", "TB", "PB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f EB", value/unit)
}

// QuotaSettings are the administrator-adjustable quota parameters.
type QuotaSettings struct {
	QuotaBytes  int64 `json:"quotaBytes"`
	WarnPercent int   `json:"warnPercent"`
}

// Settings returns the quota parameters in force: the admin's saved values, or
// the environment defaults when nothing was saved.
func (s *Service) Settings(ctx context.Context) (QuotaSettings, error) {
	out := QuotaSettings{QuotaBytes: s.opts.QuotaBytes, WarnPercent: s.opts.WarnPercent}
	if value, ok, err := s.store.Setting(ctx, settingQuotaBytes); err != nil {
		return out, err
	} else if ok {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && parsed >= 0 {
			out.QuotaBytes = parsed
		}
	}
	if value, ok, err := s.store.Setting(ctx, settingWarnPercent); err != nil {
		return out, err
	} else if ok {
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed >= minWarnPercent && parsed <= maxWarnPercent {
			out.WarnPercent = parsed
		}
	}
	if out.WarnPercent < minWarnPercent || out.WarnPercent > maxWarnPercent {
		out.WarnPercent = defaultWarnPercent
	}
	return out, nil
}

// SaveSettings stores new quota parameters. Admin only.
func (s *Service) SaveSettings(ctx context.Context, actor Actor, in QuotaSettings) (QuotaSettings, error) {
	if !actor.IsAdmin() {
		return QuotaSettings{}, ErrForbidden
	}
	if in.QuotaBytes < 0 {
		return QuotaSettings{}, fmt.Errorf("%w: kota negatif olamaz", ErrInvalidInput)
	}
	if in.WarnPercent < minWarnPercent || in.WarnPercent > maxWarnPercent {
		return QuotaSettings{}, fmt.Errorf("%w: uyarı eşiği %d ile %d arasında olmalı", ErrInvalidInput, minWarnPercent, maxWarnPercent)
	}
	if err := s.store.SetSetting(ctx, settingQuotaBytes, strconv.FormatInt(in.QuotaBytes, 10)); err != nil {
		return QuotaSettings{}, err
	}
	if err := s.store.SetSetting(ctx, settingWarnPercent, strconv.Itoa(in.WarnPercent)); err != nil {
		return QuotaSettings{}, err
	}
	return s.Settings(ctx)
}

// usedBytes returns the cached folder size, rescanning when the cache is stale.
func (s *Service) usedBytes() (int64, error) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.usageValid && s.now().Sub(s.usageAt) < usageCacheTTL {
		return s.usageBytes, nil
	}
	used, err := s.tree.usage()
	if err != nil {
		return 0, err
	}
	s.usageBytes, s.usageAt, s.usageValid = used, s.now(), true
	return used, nil
}

// InvalidateUsage forces the next quota check to rescan the folder. The
// torrent service calls it when it adds or removes data.
func (s *Service) InvalidateUsage() {
	s.usageMu.Lock()
	s.usageValid = false
	s.usageMu.Unlock()
}

// Quota reports the current fill level and raises a warning to administrators
// the first time the level worsens.
func (s *Service) Quota(ctx context.Context) (QuotaStatus, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return QuotaStatus{}, err
	}
	used, err := s.usedBytes()
	if err != nil {
		return QuotaStatus{}, err
	}
	diskTotal, diskFree, err := s.diskSpace(s.tree.dir)
	if err != nil {
		diskTotal, diskFree = 0, -1
	}
	status := evaluateQuota(settings.QuotaBytes, used, diskTotal, diskFree, settings.WarnPercent)
	s.noteQuotaLevel(ctx, status)
	return status, nil
}

func quotaRank(level string) int {
	switch level {
	case QuotaWarning:
		return 1
	case QuotaFull:
		return 2
	default:
		return 0
	}
}

// noteQuotaLevel notifies administrators once per worsening: ok → warning,
// warning → full, ok → full. Improvements are recorded silently so the next
// worsening notifies again. The last notified level is persisted, so a restart
// does not repeat the warning.
func (s *Service) noteQuotaLevel(ctx context.Context, status QuotaStatus) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	if !s.notifiedLoaded {
		if value, ok, err := s.store.Setting(ctx, settingNotifiedLvl); err == nil {
			if ok {
				s.notifiedLevel = value
			} else {
				s.notifiedLevel = QuotaOK
			}
			s.notifiedLoaded = true
		} else {
			return
		}
	}
	if status.Level == s.notifiedLevel {
		return
	}
	worsened := quotaRank(status.Level) > quotaRank(s.notifiedLevel)
	if err := s.store.SetSetting(ctx, settingNotifiedLvl, status.Level); err != nil {
		return
	}
	s.notifiedLevel = status.Level
	if !worsened {
		return
	}
	title := "Ortak Paylaşım alanı dolmak üzere"
	if status.Level == QuotaFull {
		title = "Ortak Paylaşım alanı doldu"
	}
	log.Printf("shared space quota %s: used=%d limit=%d free=%d", status.Level, status.UsedBytes, status.LimitBytes, status.FreeBytes)
	emails, err := s.store.NotifyAdmins(ctx, notificationKind, title, status.Message)
	if err != nil {
		log.Printf("shared space quota notification: %v", err)
		return
	}
	if s.mail == nil {
		return
	}
	for _, email := range emails {
		// Mail is best effort: SMTP may be unconfigured, and the in-app
		// notification above is the authoritative record.
		go func(to string) {
			sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = s.mail.Send(sendCtx, to, "TR Driver: "+title, status.Message+"\n")
		}(email)
	}
}

// Start runs the periodic quota check so administrators are warned even when
// nobody has the shared space open. It returns immediately.
func (s *Service) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(quotaMonitorPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.Quota(ctx); err != nil && ctx.Err() == nil {
					log.Printf("shared space quota check: %v", err)
				}
			}
		}
	}()
}
