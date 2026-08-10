package syncapi

import "testing"

func TestNormalizeLimit(t *testing.T) {
	tests := []struct {
		input, want int
	}{
		{0, 500},
		{-1, 500},
		{1, 1},
		{2000, 2000},
		{2001, 2000},
	}
	for _, test := range tests {
		if got := normalizeLimit(test.input); got != test.want {
			t.Errorf("normalizeLimit(%d) = %d, want %d", test.input, got, test.want)
		}
	}
}
