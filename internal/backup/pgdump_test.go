package backup

import (
	"strings"
	"testing"
)

func TestSplitDatabaseURL(t *testing.T) {
	sanitized, password, err := SplitDatabaseURL("postgres://bob:s3cret@db:5432/app?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if password != "s3cret" {
		t.Fatalf("password %q", password)
	}
	if strings.Contains(sanitized, "s3cret") {
		t.Fatalf("password leaked into URL: %s", sanitized)
	}
	if !strings.Contains(sanitized, "bob@") || !strings.Contains(sanitized, "sslmode=disable") {
		t.Fatalf("sanitized URL lost parts: %s", sanitized)
	}

	sanitized, password, err = SplitDatabaseURL("postgres://bob@db/app?password=hunter2&sslmode=require")
	if err != nil {
		t.Fatal(err)
	}
	if password != "hunter2" || strings.Contains(sanitized, "hunter2") {
		t.Fatalf("query password handling: %q %s", password, sanitized)
	}

	sanitized, password, err = SplitDatabaseURL("postgres://db/app")
	if err != nil || password != "" {
		t.Fatalf("no-password URL: %q %v", sanitized, err)
	}
}

func TestReplaceDBName(t *testing.T) {
	got, err := replaceDBName("postgres://u:p@h:5432/app?sslmode=disable", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://u:p@h:5432/postgres?sslmode=disable" {
		t.Fatalf("got %s", got)
	}
}
