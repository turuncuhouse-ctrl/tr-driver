package files

import "testing"

func TestSanitizeName(t *testing.T) {
	tests := map[string]string{
		"  rapor.pdf  ":      "rapor.pdf",
		"../../secret.txt":    ".._.._secret.txt",
		`folder\document.txt`: "folder_document.txt",
		"line\nbreak.txt":     "linebreak.txt",
		`quote".txt`:          "quote.txt",
	}
	for input, expected := range tests {
		if actual := sanitizeName(input); actual != expected {
			t.Errorf("sanitizeName(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestSanitizeNameAllowsEmptyValidation(t *testing.T) {
	if actual := sanitizeName(" \n "); actual != "" {
		t.Fatalf("expected empty sanitized name, got %q", actual)
	}
}
