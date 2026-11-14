package backup

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// Start launches the daily scheduler. It is a no-op (with a log line) when
// backups are not configured, so installs without BACKUP_PASSWORD keep working.
func (s *Service) Start(ctx context.Context) {
	if ok, reason := s.Configured(); !ok {
		log.Printf("backup scheduler disabled: %s", reason)
		return
	}
	hour, minute, err := ParseSchedule(s.cfg.Schedule)
	if err != nil {
		log.Printf("backup scheduler disabled: %v", err)
		return
	}
	s.mu.Lock()
	s.schedulerOn = true
	s.schedHour = hour
	s.schedMin = minute
	s.nextRun = NextRun(time.Now(), hour, minute)
	next := s.nextRun
	s.mu.Unlock()

	if err := MarkOrphanedRunning(ctx, s.db, s.cfg.Timeout); err != nil {
		log.Printf("backup: mark orphaned runs: %v", err)
	}
	offsite := "off"
	if s.cfg.S3Enabled() {
		offsite = "on"
	}
	log.Printf("backup scheduler on: daily %02d:%02d (next %s), repo %s, offsite %s",
		hour, minute, next.Format(time.RFC3339), s.cfg.Dir, offsite)

	if s.cfg.RunOnBoot {
		go s.maybeBootRun(ctx)
	}
	go s.loop(ctx)
}

// NextScheduled returns the upcoming scheduled run time when the scheduler runs.
func (s *Service) NextScheduled() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.schedulerOn {
		return time.Time{}, false
	}
	return s.nextRun, true
}

func (s *Service) loop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	staleEvery := 0
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.mu.Lock()
			due := s.schedulerOn && !now.Before(s.nextRun)
			hour, minute := s.schedHour, s.schedMin
			if due {
				s.nextRun = NextRun(now, hour, minute)
			}
			s.mu.Unlock()
			if due {
				if _, err := s.RunNow(ctx, KindScheduled); err != nil {
					if errors.Is(err, ErrAlreadyRunning) {
						log.Printf("backup: scheduled run skipped, previous run still active")
					} else {
						log.Printf("backup: scheduled run: %v", err)
					}
				}
			}
			staleEvery++
			if staleEvery >= 15 {
				staleEvery = 0
				s.checkStale(ctx)
			}
		}
	}
}

// maybeBootRun catches up after downtime: if no recent success exists, run once
// shortly after boot instead of waiting for the next nightly slot.
func (s *Service) maybeBootRun(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	last, err := LastSuccess(ctx, s.db)
	if err == nil && time.Since(last.StartedAt) < 20*time.Hour {
		return
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("backup: boot check: %v", err)
		return
	}
	log.Printf("backup: no recent successful run, starting catch-up run")
	if _, err := s.RunNow(ctx, KindScheduled); err != nil && !errors.Is(err, ErrAlreadyRunning) {
		log.Printf("backup: boot catch-up: %v", err)
	}
}

// checkStale sends at most one "no recent backup" webhook per day, and only
// after the process has been up longer than MaxAge (fresh installs get time
// for their first nightly run).
func (s *Service) checkStale(ctx context.Context) {
	if s.cfg.WebhookURL == "" {
		return
	}
	st, err := s.Status(ctx)
	if err != nil || !st.Stale {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.startTime) < s.cfg.MaxAge {
		return
	}
	if now.Sub(s.lastStaleAlert) < 24*time.Hour {
		return
	}
	s.lastStaleAlert = now
	message := "no successful backup within the retention window"
	if st.LastSuccessAt != nil {
		message = "last successful backup: " + st.LastSuccessAt.Format(time.RFC3339)
	}
	if err := Notify(s.cfg.WebhookURL, EventStale, st.LastRun, message); err != nil {
		log.Printf("backup: stale webhook: %v", err)
	}
}
