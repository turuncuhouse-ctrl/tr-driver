package auth

import "testing"

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"0555 111 22 33":      "905551112233",
		"+90 (555) 111-22-33": "905551112233",
		"0090 555 111 2233":   "905551112233",
		"+1 202 555 0100":     "12025550100",
		"":                    "",
	}
	for input, want := range cases {
		got, err := normalizePhone(input)
		if err != nil || got != want {
			t.Errorf("normalizePhone(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := normalizePhone("123"); err == nil {
		t.Fatal("expected invalid short phone number")
	}
}
