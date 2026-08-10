//go:build windows

package desktop

import (
	"os/exec"
	"syscall"
)

func openFileExplorer(path string) error {
	cmd := exec.Command("explorer.exe", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
