//go:build windows

package desktop

import (
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
)

var folderPickerBusy atomic.Bool

// PickingFolder is true while the native folder dialog is open.
func PickingFolder() bool { return folderPickerBusy.Load() }

func PickFolder() (string, error) {
	if !folderPickerBusy.CompareAndSwap(false, true) {
		return "", fmt.Errorf("klasör seçimi zaten açık")
	}
	defer folderPickerBusy.Store(false)

	// STA + interactive: FolderBrowserDialog needs a real apartment and must not use -NonInteractive.
	ps := `
Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.Application]::EnableVisualStyles() | Out-Null
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = 'Senkronize edilecek TR Driver klasörünü seçin'
$dialog.ShowNewFolderButton = $true
try { $dialog.UseDescriptionForTitle = $true } catch {}
$r = $dialog.ShowDialog()
if ($r -eq [System.Windows.Forms.DialogResult]::OK -and ![string]::IsNullOrWhiteSpace($dialog.SelectedPath)) {
  [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
  Write-Output $dialog.SelectedPath
}
`
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		// Cancel / closed dialog often surfaces as empty output; treat as cancel.
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}
