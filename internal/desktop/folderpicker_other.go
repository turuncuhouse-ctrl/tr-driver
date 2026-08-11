//go:build !windows

package desktop

func PickingFolder() bool { return false }

func PickFolder() (string, error) {
	return "", nil
}
