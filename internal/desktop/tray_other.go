//go:build !windows

package desktop

import (
	"fmt"
	"os"
	"os/signal"
)

// RunUI starts a headless-ish host for non-Windows development.
func RunUI(app *App) error {
	if _, err := SetupLogging(app.DataDir()); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	defer CloseLogging()

	lock, ok, err := AcquireInstance(app.DataDir())
	if err != nil {
		return err
	}
	if !ok {
		_ = SignalExistingInstance(cmdShowSettings)
		return nil
	}
	defer lock.Close()

	host, err := NewHost(app)
	if err != nil {
		return err
	}
	defer host.Close()
	fmt.Println("Desktop UI:", host.URL()+"/#/settings")
	_ = openBrowser(host.URL() + "/#/settings")
	_ = app.Start()

	lock.ServeIPC(func(cmd int) {
		Logf("ipc command %d", cmd)
	})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	app.Stop()
	return nil
}
