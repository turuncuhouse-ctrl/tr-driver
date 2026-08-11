//go:build windows

package desktop

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
)

const (
	swHide         = 0
	swRestore      = 9
	smCXScreen     = 0
	smCYScreen     = 1
	swpNoSize      = 0x0001
	swpNoZOrder    = 0x0004
	swpShowWindow  = 0x0040
)

var (
	procShowWindow       = modUser32.NewProc("ShowWindow")
	procSetWindowPos     = modUser32.NewProc("SetWindowPos")
	procGetSystemMetrics = modUser32.NewProc("GetSystemMetrics")
	procIsWindow         = modUser32.NewProc("IsWindow")
)

type windowController struct {
	mu        sync.Mutex
	webview   webview2.WebView
	hostURL   string
	mode      string // flyout|settings|hidden
	ready     chan struct{}
	readyOnce sync.Once
	quitting  atomic.Bool
}

func newWindowController(hostURL string) *windowController {
	return &windowController{
		hostURL: hostURL,
		mode:    "hidden",
		ready:   make(chan struct{}),
	}
}

func (c *windowController) run() {
	c.runOnce()
	// Do not auto-recreate the WebView forever — that causes tray flicker.
}

func (c *windowController) runOnce() {
	c.mu.Lock()
	c.ready = make(chan struct{})
	c.readyOnce = sync.Once{}
	c.webview = nil
	c.mu.Unlock()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  DefaultDataDir() + `\webview`,
		WindowOptions: webview2.WindowOptions{
			Title:  "TR Driver Sync",
			Width:  400,
			Height: 520,
			Center: false,
		},
	})
	if w == nil {
		log.Print("webview2 unavailable; install Microsoft Edge WebView2 Runtime")
		select {}
	}

	c.mu.Lock()
	c.webview = w
	c.mu.Unlock()
	c.readyOnce.Do(func() { close(c.ready) })

	_ = w.Bind("hostHide", func() {
		if c.currentMode() == "flyout" {
			c.hide()
		}
	})
	w.Init(`
		window.addEventListener('blur', function () {
			try {
				if (window.__trPickerOpen) return;
				if ((location.hash || '').indexOf('flyout') >= 0 && window.hostHide) {
					window.hostHide();
				}
			} catch (e) {}
		});
	`)
	w.SetSize(400, 520, webview2.HintFixed)
	w.Navigate(c.hostURL + "/#/flyout")
	c.hide()
	w.Run()

	c.mu.Lock()
	c.webview = nil
	c.mu.Unlock()
}

func (c *windowController) currentMode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

func (c *windowController) waitReady() {
	for !c.quitting.Load() {
		c.mu.Lock()
		ch := c.ready
		w := c.webview
		c.mu.Unlock()
		if w != nil {
			return
		}
		if ch != nil {
			select {
			case <-ch:
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (c *windowController) dispatch(fn func()) {
	c.mu.Lock()
	w := c.webview
	c.mu.Unlock()
	if w == nil {
		return
	}
	w.Dispatch(fn)
}

func (c *windowController) showFlyout() {
	c.waitReady()
	c.dispatch(func() {
		c.mu.Lock()
		w := c.webview
		c.mode = "flyout"
		c.mu.Unlock()
		if w == nil {
			return
		}
		w.SetTitle("TR Driver Sync")
		w.SetSize(400, 520, webview2.HintFixed)
		w.Eval(`try{location.hash='#/flyout'}catch(e){}`)
		positionNearTray(w.Window(), 400, 520)
		showNativeWindow(w.Window(), true)
	})
}

func (c *windowController) showSettings() {
	c.waitReady()
	c.dispatch(func() {
		c.mu.Lock()
		w := c.webview
		c.mode = "settings"
		c.mu.Unlock()
		if w == nil {
			return
		}
		w.SetTitle("TR Driver Ayarlar")
		w.SetSize(780, 720, webview2.HintNone)
		w.Eval(`try{location.hash='#/settings'}catch(e){}`)
		showNativeWindow(w.Window(), true)
	})
}

func (c *windowController) hide() {
	if PickingFolder() {
		return
	}
	c.dispatch(func() {
		c.mu.Lock()
		w := c.webview
		c.mode = "hidden"
		c.mu.Unlock()
		if w == nil {
			return
		}
		showNativeWindow(w.Window(), false)
	})
}

func (c *windowController) terminate() {
	c.quitting.Store(true)
	c.dispatch(func() {
		c.mu.Lock()
		w := c.webview
		c.mu.Unlock()
		if w != nil {
			w.Terminate()
		}
	})
}

func showNativeWindow(hwnd unsafe.Pointer, show bool) {
	if hwnd == nil {
		return
	}
	h := uintptr(hwnd)
	alive, _, _ := procIsWindow.Call(h)
	if alive == 0 {
		return
	}
	if show {
		procShowWindow.Call(h, swRestore)
		procSetForegroundWindow.Call(h)
		return
	}
	procShowWindow.Call(h, swHide)
}

func positionNearTray(hwnd unsafe.Pointer, width, height int) {
	if hwnd == nil {
		return
	}
	screenW, _, _ := procGetSystemMetrics.Call(smCXScreen)
	screenH, _, _ := procGetSystemMetrics.Call(smCYScreen)
	x := int(screenW) - width - 16
	y := int(screenH) - height - 56
	if x < 8 {
		x = 8
	}
	if y < 8 {
		y = 8
	}
	procSetWindowPos.Call(uintptr(hwnd), 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpShowWindow)
}
