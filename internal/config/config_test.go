package config

import "testing"

func TestParseTrustedProxies(t *testing.T) {
	got, err := parseTrustedProxies(" 10.0.0.0/8, 192.168.1.5/32 ,, ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].String() != "10.0.0.0/8" || got[1].String() != "192.168.1.5/32" {
		t.Fatalf("got %v", got)
	}
	if _, err := parseTrustedProxies("not-a-cidr"); err == nil {
		t.Fatal("expected error")
	}
}

func TestPersonalBackupKeyDefaultsToSessionSecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("SESSION_SECRET", "session-secret-for-personal-backup-tests")
	t.Setenv("PERSONAL_BACKUP_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PersonalBackupKey != cfg.SessionSecret {
		t.Fatalf("personal backup key should fall back to the session secret, got %q", cfg.PersonalBackupKey)
	}
}

func TestPersonalBackupKeyRejectsShortExplicitValue(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("SESSION_SECRET", "session-secret-for-personal-backup-tests")
	t.Setenv("PERSONAL_BACKUP_KEY", "short")
	if _, err := Load(); err == nil {
		t.Fatal("expected a short PERSONAL_BACKUP_KEY to be rejected")
	}
}

func TestLocalTelKeyDefaultsAndRejectsShortExplicitValue(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("SESSION_SECRET", "session-secret-for-localtel-tests-1234")
	t.Setenv("PERSONAL_BACKUP_KEY", "personal-backup-key-for-localtel-tests")
	t.Setenv("LOCAL_TEL_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LocalTelKey != cfg.PersonalBackupKey {
		t.Fatalf("localtel key should fall back to personal backup key, got %q", cfg.LocalTelKey)
	}
	t.Setenv("LOCAL_TEL_KEY", "short")
	if _, err := Load(); err == nil {
		t.Fatal("expected short LOCAL_TEL_KEY to be rejected")
	}
}

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("SESSION_SECRET", "session-secret-for-shared-space-tests")
	for _, key := range []string{"SHARED_DIR", "SHARED_QUOTA_BYTES", "SHARED_QUOTA_WARN_PERCENT", "SHARED_LINK_MAX_TTL",
		"TORRENT_ENABLED", "TORRENT_LISTEN_PORT", "TORRENT_DOWNLOAD_LIMIT_KBPS", "TORRENT_UPLOAD_LIMIT_KBPS",
		"TORRENT_SEED_AFTER_COMPLETE", "TORRENT_DISABLE_DHT"} {
		t.Setenv(key, "")
	}
}

func TestSharedSpaceDefaults(t *testing.T) {
	setRequired(t)
	t.Setenv("DATA_DIR", "/data/files")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SharedDir != "/data/paylasim" {
		t.Fatalf("SharedDir = %q, want /data/paylasim (a sibling of DATA_DIR)", cfg.SharedDir)
	}
	if cfg.SharedQuotaBytes != 0 || cfg.SharedQuotaWarnPercent != 90 || cfg.SharedLinkMaxTTL.Hours() != 720 {
		t.Fatalf("shared defaults = %d / %d / %v", cfg.SharedQuotaBytes, cfg.SharedQuotaWarnPercent, cfg.SharedLinkMaxTTL)
	}
	if !cfg.TorrentEnabled || cfg.TorrentListenPort != 42069 || cfg.TorrentDownloadLimitKBps != 0 || cfg.TorrentUploadLimitKBps != 0 ||
		!cfg.TorrentSeedAfterComplete || cfg.TorrentDisableDHT {
		t.Fatalf("torrent defaults = %+v", cfg)
	}
}

func TestSharedDirMustNotOverlapDataDir(t *testing.T) {
	for _, tc := range []struct{ data, shared string }{
		{"/data/files", "/data/files"},
		{"/data/files", "/data/files/paylasim"},
		{"/data/files", "/data/files/../files/x"},
		{"/data/files/blobs", "/data/files"},
		{"/data/files", "/data"},
		{"/data/files", "/"},
	} {
		setRequired(t)
		t.Setenv("DATA_DIR", tc.data)
		t.Setenv("SHARED_DIR", tc.shared)
		if _, err := Load(); err == nil {
			t.Errorf("DATA_DIR=%s SHARED_DIR=%s was accepted", tc.data, tc.shared)
		}
	}
	// A name that merely starts the same is a different directory.
	setRequired(t)
	t.Setenv("DATA_DIR", "/data/files")
	t.Setenv("SHARED_DIR", "/data/files-ortak")
	if cfg, err := Load(); err != nil || cfg.SharedDir != "/data/files-ortak" {
		t.Fatalf("sibling with a common prefix: %v", err)
	}
}

func TestSharedSpaceSettingsAreValidated(t *testing.T) {
	for key, value := range map[string]string{
		"SHARED_QUOTA_WARN_PERCENT":   "100",
		"SHARED_LINK_MAX_TTL":         "-1h",
		"TORRENT_LISTEN_PORT":         "70000",
		"TORRENT_DOWNLOAD_LIMIT_KBPS": "-5",
		"TORRENT_UPLOAD_LIMIT_KBPS":   "-5",
	} {
		setRequired(t)
		t.Setenv("DATA_DIR", "/data/files")
		t.Setenv(key, value)
		if _, err := Load(); err == nil {
			t.Errorf("%s=%s was accepted", key, value)
		}
	}
	setRequired(t)
	t.Setenv("DATA_DIR", "/data/files")
	t.Setenv("SHARED_QUOTA_BYTES", "53687091200")
	t.Setenv("TORRENT_ENABLED", "false")
	t.Setenv("TORRENT_DOWNLOAD_LIMIT_KBPS", "2048")
	t.Setenv("TORRENT_SEED_AFTER_COMPLETE", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SharedQuotaBytes != 53687091200 || cfg.TorrentEnabled || cfg.TorrentDownloadLimitKBps != 2048 || !cfg.TorrentSeedAfterComplete {
		t.Fatalf("explicit settings not applied: %+v", cfg)
	}
	setRequired(t)
	t.Setenv("DATA_DIR", "/data/files")
	t.Setenv("TORRENT_SEED_AFTER_COMPLETE", "false")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.TorrentSeedAfterComplete {
		t.Fatal("TORRENT_SEED_AFTER_COMPLETE=false was ignored")
	}
}
