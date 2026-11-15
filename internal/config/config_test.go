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
