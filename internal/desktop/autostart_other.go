//go:build !windows

package desktop

func SetAutostart(enabled bool) error { _ = enabled; return nil }
func IsAutostartEnabled() bool        { return false }
