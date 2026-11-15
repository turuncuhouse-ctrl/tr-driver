package auth

import (
	"errors"
	"strings"
)

var ErrInvalidPhone = errors.New("invalid phone number")

// normalizePhone stores a phone in digits-only international form so Android's
// local contact matcher and gateway target resolution compare the same value.
// Turkish national numbers are canonicalized to country code 90.
func normalizePhone(raw string) (string, error) {
	var digits strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	value := digits.String()
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "00") {
		value = strings.TrimPrefix(value, "00")
	}
	if len(value) == 11 && strings.HasPrefix(value, "0") {
		value = "90" + value[1:]
	} else if len(value) == 10 && strings.HasPrefix(value, "5") {
		value = "90" + value
	}
	if len(value) < 7 || len(value) > 15 {
		return "", ErrInvalidPhone
	}
	return value, nil
}
