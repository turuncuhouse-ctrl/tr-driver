//go:build windows

package desktop

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmApp       = 0x8000
	wmTray      = wmApp + 1
	wmDestroy   = 0x0002
	wmCommand   = 0x0111
	wmLButtonUp = 0x0202
	wmRButtonUp = 0x0205
	wmClose     = 0x0010
	nimAdd      = 0x00000000
	nimDelete   = 0x00000002
	nifMessage  = 0x00000001
	nifIcon     = 0x00000002
	nifTip      = 0x00000004
	mfString    = 0x00000000
	mfSeparator = 0x00000800
	tpmRightButton = 0x0002
	idiApplication = 32512
	idOpen      = 1001
	idSettings  = 1002
	idPause     = 1003
	idResume    = 1004
	idQuit      = 1005
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
}

var (
	modUser32               = windows.NewLazySystemDLL("user32.dll")
	modShell32              = windows.NewLazySystemDLL("shell32.dll")
	procCreateWindowExW     = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW      = modUser32.NewProc("DefWindowProcW")
	procRegisterClassExW    = modUser32.NewProc("RegisterClassExW")
	procGetMessageW         = modUser32.NewProc("GetMessageW")
	procTranslateMessage    = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW    = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage     = modUser32.NewProc("PostQuitMessage")
	procLoadIconW           = modUser32.NewProc("LoadIconW")
	procCreatePopupMenu     = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW         = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenu      = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu         = modUser32.NewProc("DestroyMenu")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
	procGetCursorPos        = modUser32.NewProc("GetCursorPos")
	procShellNotifyIconW    = modShell32.NewProc("Shell_NotifyIconW")
)

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type point struct{ X, Y int32 }
type msg struct {
	HWnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

var (
	trayApp  *App
	trayCtrl *windowController
	trayHost *Host
)

// RunUI starts the professional tray + WebView2 shell.
func RunUI(app *App) error {
	trayApp = app
	if _, err := SetupLogging(app.DataDir()); err != nil {
		log.Printf("logging setup: %v", err)
	}
	defer CloseLogging()

	lock, ok, err := AcquireInstance(app.DataDir())
	if err != nil {
		return err
	}
	if !ok {
		_ = SignalExistingInstance(cmdShowFlyout)
		return nil
	}
	defer lock.Close()

	host, err := NewHost(app)
	if err != nil {
		return err
	}
	defer host.Close()
	trayHost = host

	ctrl := newWindowController(host.URL())
	trayCtrl = ctrl
	host.SetWindowCallbacks(ctrl.showFlyout, ctrl.showSettings, ctrl.hide, func() {
		ctrl.terminate()
	})

	lock.ServeIPC(func(cmd int) {
		switch cmd {
		case cmdShowSettings:
			ctrl.showSettings()
		case cmdQuit:
			ctrl.terminate()
		default:
			ctrl.showFlyout()
		}
	})

	_ = app.Start()

	go func() {
		ctrl.run()
		// WebView loop exits only on explicit quit (or fatal webview failure).
		app.Stop()
		procPostQuitMessage.Call(0)
	}()

	return runTrayLoop()
}

func runTrayLoop() error {
	className, _ := windows.UTF16PtrFromString("TRDriverSyncTray")
	wndProc := windows.NewCallback(trayWndProc)
	cls := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   wndProc,
		LpszClassName: className,
	}
	r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&cls)))
	if r == 0 {
		return err
	}
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if hwnd == 0 {
		return err
	}
	icon, _, _ := procLoadIconW.Call(0, uintptr(idiApplication))
	nid := notifyIconData{
		CbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:             windows.HWND(hwnd),
		UID:              1,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmTray,
		HIcon:            windows.Handle(icon),
	}
	tip, _ := windows.UTF16FromString("TR Driver Sync")
	copy(nid.SzTip[:], tip)
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		if trayCtrl != nil {
			trayCtrl.terminate()
		}
		procPostQuitMessage.Call(0)
	}()

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	if trayApp != nil {
		trayApp.Stop()
	}
	return nil
}

func trayWndProc(hwnd windows.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	if msg == wmTray {
		switch lParam {
		case wmLButtonUp:
			if trayCtrl != nil {
				trayCtrl.showFlyout()
			}
		case wmRButtonUp:
			showTrayMenu(hwnd)
		}
		return 0
	}
	if msg == wmCommand {
		switch wParam {
		case idOpen:
			if trayCtrl != nil {
				trayCtrl.showFlyout()
			}
		case idSettings:
			if trayCtrl != nil {
				trayCtrl.showSettings()
			}
		case idPause:
			_ = trayApp.PauseAll()
		case idResume:
			_ = trayApp.ResumeAll()
		case idQuit:
			if trayCtrl != nil {
				trayCtrl.terminate()
			}
			procPostQuitMessage.Call(0)
		}
		return 0
	}
	if msg == wmClose || msg == wmDestroy {
		// Closing tray helper window should not kill unless quitting.
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

func showTrayMenu(hwnd windows.HWND) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendMenu(menu, idOpen, "Durumu aç")
	appendMenu(menu, idSettings, "Ayarlar")
	appendMenu(menu, idPause, "Duraklat")
	appendMenu(menu, idResume, "Devam")
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	appendMenu(menu, idQuit, "Çıkış")
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(hwnd))
	procTrackPopupMenu.Call(menu, tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, uintptr(hwnd), 0)
}

func appendMenu(menu uintptr, id uintptr, text string) {
	p, _ := windows.UTF16PtrFromString(text)
	procAppendMenuW.Call(menu, mfString, id, uintptr(unsafe.Pointer(p)))
}
