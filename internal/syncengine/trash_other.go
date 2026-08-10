//go:build !windows

package syncengine

import "os"

func moveToTrash(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(path)
}
