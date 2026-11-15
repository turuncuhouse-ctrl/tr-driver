package personalbackup

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseKind(t *testing.T) {
	for _, raw := range []string{"contacts", "CONTACTS", " sms ", "Sms"} {
		if _, err := ParseKind(raw); err != nil {
			t.Fatalf("%q should parse: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "calls", "contact", "sms/../x"} {
		if _, err := ParseKind(raw); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q should be rejected, got %v", raw, err)
		}
	}
}

func TestNormalizeKey(t *testing.T) {
	good := "aGVsbG8td29ybGQta2V5LTAwMDE"
	if _, err := normalizeKey(" " + good + " "); err != nil {
		t.Fatalf("expected key to be accepted: %v", err)
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 129), "has space", "semi;colon", "../etc/passwd", "key<script>"} {
		if _, err := normalizeKey(bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q should be rejected, got %v", bad, err)
		}
	}
}

func TestNormalizeHash(t *testing.T) {
	upper := strings.Repeat("AB", 32)
	got, err := normalizeHash(upper)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if got != strings.ToLower(upper) {
		t.Fatalf("hash must be lowercased, got %q", got)
	}
	if empty, err := normalizeHash("  "); err != nil || empty != "" {
		t.Fatalf("empty hash must be allowed, got %q %v", empty, err)
	}
	for _, bad := range []string{"abc", strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		if _, err := normalizeHash(bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q should be rejected, got %v", bad, err)
		}
	}
}

func TestNormalizeDataLimitsAndShape(t *testing.T) {
	if _, err := normalizeData(KindContacts, json.RawMessage(`{"name":"x"}`)); err != nil {
		t.Fatalf("valid object rejected: %v", err)
	}
	cases := []struct {
		name string
		kind Kind
		raw  json.RawMessage
	}{
		{"empty", KindContacts, nil},
		{"array", KindContacts, json.RawMessage(`[1,2,3]`)},
		{"broken", KindContacts, json.RawMessage(`{"a":`)},
		{"too big contacts", KindContacts, json.RawMessage(`{"v":"` + strings.Repeat("x", maxContactsItemBytes) + `"}`)},
		{"too big sms", KindSMS, json.RawMessage(`{"v":"` + strings.Repeat("x", maxSMSItemBytes) + `"}`)},
	}
	for _, tc := range cases {
		if _, err := normalizeData(tc.kind, tc.raw); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: expected ErrInvalidInput, got %v", tc.name, err)
		}
	}
	// A 64 KiB SMS record is valid but the contacts cap is much lower.
	bigSMS := json.RawMessage(`{"v":"` + strings.Repeat("x", maxSMSItemBytes-len(`{"v":""}`)) + `"}`)
	if _, err := normalizeData(KindSMS, bigSMS); err != nil {
		t.Fatalf("large sms record should still fit: %v", err)
	}
	if _, err := normalizeData(KindContacts, bigSMS); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("large sms record must be rejected for contacts")
	}
}

func TestNormalizeDeviceID(t *testing.T) {
	if got := normalizeDeviceID("   "); got != "unknown" {
		t.Fatalf("blank device id: got %q", got)
	}
	if got := normalizeDeviceID("Android\x00-Pixel\n7"); got != "Android-Pixel7" {
		t.Fatalf("control characters must be stripped, got %q", got)
	}
	long := normalizeDeviceID(strings.Repeat("d", 300))
	if len(long) > maxDeviceIDLen {
		t.Fatalf("device id too long: %d", len(long))
	}
}

func TestNewServiceRequiresVault(t *testing.T) {
	if _, err := NewService(nil, nil); err == nil {
		t.Fatal("expected an error without a vault")
	}
}

func TestAADDiffersPerUserAndKind(t *testing.T) {
	if string(aadFor("a", KindContacts)) == string(aadFor("b", KindContacts)) {
		t.Fatal("aad must include the user id")
	}
	if string(aadFor("a", KindContacts)) == string(aadFor("a", KindSMS)) {
		t.Fatal("aad must include the kind")
	}
}
