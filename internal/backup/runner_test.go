package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"necipdrive/internal/store"
)

// TestFullBackupRun exercises a real end-to-end run: pg_dump against the CI
// database, restic backup into a temp repo, retention, and the restore
// rehearsal. It runs only where the database and the backup binaries exist.
func TestFullBackupRun(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	for _, bin := range []string{"restic", "pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(ctx, db, 1<<30, 0); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	dataDir := filepath.Join(t.TempDir(), "files")
	for _, dir := range []string{"photos", "tmp/uploads", "docs"} {
		if err := os.MkdirAll(filepath.Join(dataDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"photos/img1.jpg":    "fake-image-bytes-1",
		"photos/img2.jpg":    "fake-image-bytes-2-longer",
		"docs/note.txt":      "hello backup",
		"tmp/uploads/x.part": "transient-chunk",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(dataDir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := Config{
		Enabled: true, Dir: filepath.Join(t.TempDir(), "restic"),
		Password: "integration-test-backup-password",
		Schedule: "02:00", Timeout: 10 * time.Minute, MaxAge: 30 * time.Hour,
		KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6,
		ResticBin: "restic", PgDumpBin: "pg_dump", PgRestoreBin: "pg_restore",
	}
	svc := NewService(db, cfg, dataDir, dsn)

	run, err := svc.RunSync(ctx, KindManual)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != StatusSuccess {
		t.Fatalf("status=%s error=%s", run.Status, run.Error)
	}
	if !run.VerifyOK {
		t.Fatalf("verify flag not set: %+v", run)
	}
	if run.LocalSnapshotID == "" || run.DbBytes <= 0 {
		t.Fatalf("missing snapshot/db size: %+v", run)
	}
	if run.ProbeRelPath == "" {
		t.Fatal("expected a probe file to be recorded")
	}

	snapshots, err := svc.restic.Snapshots(ctx, svc.localRepo())
	if err != nil {
		t.Fatalf("snapshots: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}

	// Second run must be incremental (no new data beyond the fresh dump).
	second, err := svc.RunSync(ctx, KindManual)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Status != StatusSuccess || !second.VerifyOK {
		t.Fatalf("second run: %+v", second)
	}

	// Verify-only run re-checks without adding snapshots.
	verify, err := svc.VerifySync(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verify.Status != StatusSuccess || !verify.VerifyOK {
		t.Fatalf("verify run: %+v", verify)
	}
	snapshots, err = svc.restic.Snapshots(ctx, svc.localRepo())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("verify must not add snapshots, got %d", len(snapshots))
	}

	st, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Configured || st.Stale || st.LastSuccessAt == nil {
		t.Fatalf("bad status: %+v", st)
	}
	if !st.Binaries["restic"] || !st.Binaries["pg_dump"] || !st.Binaries["pg_restore"] {
		t.Fatalf("binaries: %v", st.Binaries)
	}

	runs, err := ListRuns(ctx, db, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) < 3 {
		t.Fatalf("expected >=3 runs, got %d", len(runs))
	}
}

func TestSingleFlight(t *testing.T) {
	svc := NewService(nil, Config{Enabled: false}, t.TempDir(), "")
	svc.mu.Lock()
	svc.running = true
	svc.mu.Unlock()
	// Not-configured short-circuits before the lock check; force configured.
	svc.cfg = Config{Enabled: true, Password: "1234567890123456", Dir: t.TempDir(),
		Schedule: "02:00", Timeout: time.Minute, MaxAge: time.Hour,
		ResticBin: "restic", PgDumpBin: "pg_dump", PgRestoreBin: "pg_restore"}
	if _, err := svc.RunNow(context.Background(), KindManual); err != ErrAlreadyRunning {
		t.Fatalf("expected ErrAlreadyRunning, got %v", err)
	}
}
