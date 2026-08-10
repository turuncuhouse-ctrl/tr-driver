//go:build !windows

package desktop

import "fmt"

func PickFolder() (string, error) {
	return "", fmt.Errorf("klasör seçici yalnızca Windows'ta desteklenir")
}
