//go:build windows

package syncengine

import (
	"os"
	"path/filepath"
	"time"
)

// moveToTrash is deliberately isolated so the desktop host can replace it with
// SHFileOperation/IFileOperation recycle-bin integration without changing sync.
func moveToTrash(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.Rename(path, path+".trash."+time.Now().Format("20060102150405")+filepath.Ext(path))
}
