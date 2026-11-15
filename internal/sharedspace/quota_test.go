package sharedspace

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEvaluateQuota(t *testing.T) {
	const gb = int64(1) << 30
	cases := []struct {
		name                             string
		quota, used, diskTotal, diskFree int64
		warn                             int
		wantLevel                        string
		wantFree                         int64
	}{
		{"well below quota", 10 * gb, 2 * gb, 100 * gb, 50 * gb, 90, QuotaOK, 8 * gb},
		{"just below warning", 100 * gb, 89 * gb, 500 * gb, 400 * gb, 90, QuotaOK, 11 * gb},
		{"at warning threshold", 100 * gb, 90 * gb, 500 * gb, 400 * gb, 90, QuotaWarning, 10 * gb},
		{"quota reached", 10 * gb, 10 * gb, 100 * gb, 50 * gb, 90, QuotaFull, 0},
		{"over quota", 10 * gb, 12 * gb, 100 * gb, 50 * gb, 90, QuotaFull, 0},
		{"disk tighter than quota", 100 * gb, 10 * gb, 100 * gb, 1 * gb, 90, QuotaWarning, 1*gb - diskReserveBytes},
		{"disk full under quota", 100 * gb, 10 * gb, 100 * gb, diskReserveBytes, 90, QuotaFull, 0},
		{"no quota, roomy disk", 0, 10 * gb, 100 * gb, 50 * gb, 90, QuotaOK, 50*gb - diskReserveBytes},
		{"no quota, disk nearly full", 0, 95 * gb, 100 * gb, 2 * gb, 90, QuotaWarning, 2*gb - diskReserveBytes},
		{"no quota, disk full", 0, 99 * gb, 100 * gb, 0, 90, QuotaFull, 0},
		{"custom threshold", 100 * gb, 55 * gb, 500 * gb, 400 * gb, 50, QuotaWarning, 45 * gb},
	}
	for _, c := range cases {
		got := evaluateQuota(c.quota, c.used, c.diskTotal, c.diskFree, c.warn)
		if got.Level != c.wantLevel || got.FreeBytes != c.wantFree {
			t.Errorf("%s: level=%s free=%d (%d%%), want level=%s free=%d", c.name, got.Level, got.FreeBytes, got.UsedPercent, c.wantLevel, c.wantFree)
		}
		if (got.Level == QuotaOK) != (got.Message == "") {
			t.Errorf("%s: message %q does not match level %s", c.name, got.Message, got.Level)
		}
	}
	// Unknown disk and no quota: nothing to enforce.
	if got := evaluateQuota(0, 5*gb, 0, -1, 90); got.Level != QuotaOK || got.FreeBytes <= 0 {
		t.Errorf("unknown disk: %+v", got)
	}
}

func TestUploadsAreHeldToTheQuota(t *testing.T) {
	// Leave room for exactly 50 bytes on top of the seeded test files.
	env := newTestEnv(t, func(o *Options) { o.QuotaBytes = seedBytes + 50 })

	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler&name=buyuk.bin", strings.NewReader(strings.Repeat("x", 51))), http.StatusInsufficientStorage)
	// Without a declared length the stream itself is cut off at the limit.
	rec := env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler&name=buyuk2.bin", struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 51))})
	wantStatus(t, rec, http.StatusInsufficientStorage)
	if env.exists("Filmler/buyuk.bin") || env.exists("Filmler/buyuk2.bin") {
		t.Fatal("an upload beyond the quota was kept")
	}

	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler&name=sigar.bin", strings.NewReader(strings.Repeat("x", 50))), http.StatusCreated)
	// Now exactly full: even one byte is refused.
	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler&name=bir.bin", strings.NewReader("x")), http.StatusInsufficientStorage)

	quota, err := env.service.Quota(context.Background())
	if err != nil || quota.Level != QuotaFull || quota.UsedBytes != seedBytes+50 {
		t.Fatalf("quota = %+v, %v", quota, err)
	}
	// The refused uploads left no staging files behind.
	staged, err := os.ReadDir(filepath.Join(env.dir, InternalDir, "tmp"))
	if err != nil || len(staged) != 0 {
		t.Fatalf("staging directory = %v, %v", staged, err)
	}
	// Deleting frees the space again.
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"delete", map[string]string{"path": "Filmler/sigar.bin"}), http.StatusOK)
	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler&name=bir.bin", strings.NewReader("x")), http.StatusCreated)
}

func TestUploadSizeLimit(t *testing.T) {
	env := newTestEnv(t, func(o *Options) { o.MaxUploadBytes = 10 })
	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler&name=a.bin", strings.NewReader(strings.Repeat("x", 11))), http.StatusRequestEntityTooLarge)
	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler&name=a.bin", strings.NewReader(strings.Repeat("x", 10))), http.StatusCreated)
}

func TestAdministratorsAreWarnedOncePerWorsening(t *testing.T) {
	const gb = int64(1) << 30
	env := newTestEnv(t)
	ctx := context.Background()
	check := func(wantLevel string, wantNotices ...string) {
		t.Helper()
		status, err := env.service.Quota(ctx)
		if err != nil || status.Level != wantLevel {
			t.Fatalf("quota level = %s (%v), want %s", status.Level, err, wantLevel)
		}
		if got := env.store.noticeTitles(); !slices.Equal(got, wantNotices) {
			t.Fatalf("notifications = %q, want %q", got, wantNotices)
		}
	}
	const nearly, full = "Ortak Paylaşım alanı dolmak üzere", "Ortak Paylaşım alanı doldu"

	check(QuotaOK)
	env.setDisk(100*gb, 5*gb) // 95% used
	check(QuotaWarning, nearly)
	check(QuotaWarning, nearly) // repeated checks do not repeat the warning
	env.setDisk(100*gb, 0)
	check(QuotaFull, nearly, full)
	check(QuotaFull, nearly, full)
	env.setDisk(100*gb, 50*gb) // space was freed: no notification for good news
	check(QuotaOK, nearly, full)
	env.setDisk(100*gb, 0) // straight to full: one notification, not two
	check(QuotaFull, nearly, full, full)

	// The level survives a restart, so the warning is not sent again.
	restarted, err := New(env.store, env.service.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if status, err := restarted.Quota(ctx); err != nil || status.Level != QuotaFull {
		t.Fatalf("restarted quota = %+v, %v", status, err)
	}
	if got := env.store.noticeTitles(); len(got) != 3 {
		t.Fatalf("restart repeated a notification: %q", got)
	}
}

func TestQuotaSettingsAreValidatedAndApplied(t *testing.T) {
	env := newTestEnv(t)
	for _, bad := range []QuotaSettings{{QuotaBytes: -1, WarnPercent: 90}, {QuotaBytes: 0, WarnPercent: 0}, {QuotaBytes: 0, WarnPercent: 100}} {
		wantStatus(t, env.doJSON("admin", http.MethodPut, AdminBasePath+"settings", bad), http.StatusBadRequest)
	}
	rec := env.doJSON("admin", http.MethodPut, AdminBasePath+"settings", QuotaSettings{QuotaBytes: seedBytes, WarnPercent: 50})
	wantStatus(t, rec, http.StatusOK)
	out := decode[struct {
		Settings QuotaSettings `json:"settings"`
		Quota    QuotaStatus   `json:"quota"`
	}](t, rec)
	if out.Settings.QuotaBytes != seedBytes || out.Quota.Level != QuotaFull {
		t.Fatalf("settings response = %+v", out)
	}
	// Users of the space see the fill level in their status.
	status := decode[struct {
		Quota QuotaStatus `json:"quota"`
	}](t, env.do("reader", http.MethodGet, BasePath+"status", nil))
	if status.Quota.Level != QuotaFull || status.Quota.Message == "" {
		t.Fatalf("reader status quota = %+v", status.Quota)
	}
}
