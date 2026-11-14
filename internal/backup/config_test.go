package backup

import (
	"testing"
	"time"
)

func TestParseSchedule(t *testing.T) {
	hour, minute, err := ParseSchedule("02:00")
	if err != nil || hour != 2 || minute != 0 {
		t.Fatalf("got %d:%d err=%v", hour, minute, err)
	}
	if _, _, err := ParseSchedule("2:5"); err != nil {
		t.Fatalf("single digits should parse: %v", err)
	}
	for _, bad := range []string{"", "nope", "24:00", "02:60", "02", "02:00:00", "-1:00"} {
		if _, _, err := ParseSchedule(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestNextRun(t *testing.T) {
	loc := time.FixedZone("test", 3*3600)
	now := time.Date(2026, 11, 14, 1, 0, 0, 0, loc)
	next := NextRun(now, 2, 30)
	if next.Hour() != 2 || next.Minute() != 30 || next.Day() != 14 {
		t.Fatalf("expected same-day 02:30, got %s", next)
	}
	now = time.Date(2026, 11, 14, 3, 0, 0, 0, loc)
	next = NextRun(now, 2, 30)
	if next.Day() != 15 || next.Hour() != 2 || next.Minute() != 30 {
		t.Fatalf("expected next-day 02:30, got %s", next)
	}
	// Exact hit rolls to the next day (strictly-after semantics).
	now = time.Date(2026, 11, 14, 2, 30, 0, 0, loc)
	if next := NextRun(now, 2, 30); next.Day() != 15 {
		t.Fatalf("expected rollover, got %s", next)
	}
}

func TestS3Repo(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			"aws default region",
			Config{S3Bucket: "bkt", S3Prefix: "trdriver"},
			"s3:s3.amazonaws.com/bkt/trdriver",
		},
		{
			"aws eu region",
			Config{S3Region: "eu-central-1", S3Bucket: "bkt"},
			"s3:s3.eu-central-1.amazonaws.com/bkt",
		},
		{
			"compatible endpoint with scheme",
			Config{S3Endpoint: "https://s3.wasabisys.com", S3Bucket: "bkt", S3Prefix: "/pre/"},
			"s3:s3.wasabisys.com/bkt/pre",
		},
		{
			"compatible endpoint bare host",
			Config{S3Endpoint: "minio.lan:9000", S3Bucket: "bkt", S3Prefix: "p"},
			"s3:minio.lan:9000/bkt/p",
		},
	}
	for _, tc := range cases {
		if got := tc.cfg.S3Repo(); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestS3Enabled(t *testing.T) {
	full := Config{S3Bucket: "b", S3AccessKey: "a", S3SecretKey: "s"}
	if !full.S3Enabled() {
		t.Fatal("expected enabled")
	}
	for _, cfg := range []Config{
		{},
		{S3Bucket: "b"},
		{S3Bucket: "b", S3AccessKey: "a"},
	} {
		if cfg.S3Enabled() {
			t.Fatalf("expected disabled for %+v", cfg)
		}
	}
}

func TestValidate(t *testing.T) {
	good := Config{
		Enabled: true, Dir: "/tmp/x", Password: "1234567890123456",
		Schedule: "02:00", Timeout: time.Hour, MaxAge: time.Hour,
		ResticBin: "restic", PgDumpBin: "pg_dump", PgRestoreBin: "pg_restore",
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("good config: %v", err)
	}
	bad := good
	bad.Password = "short"
	if err := bad.Validate(); err == nil {
		t.Fatal("expected short-password error")
	}
	empty := good
	empty.Password = ""
	if err := empty.Validate(); err != nil {
		t.Fatalf("missing password must not fail validation (scheduler stays off): %v", err)
	}
	bad = good
	bad.Schedule = "99:99"
	if err := bad.Validate(); err == nil {
		t.Fatal("expected schedule error")
	}
}
