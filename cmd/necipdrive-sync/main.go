package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"necipdrive/internal/desktop"
)

func main() {
	server := flag.String("server", "", "optional initial server URL")
	email := flag.String("email", "", "optional account email")
	password := flag.String("password", "", "optional password for first login")
	folder := flag.String("folder", "", "optional folder to add on first run")
	dataDir := flag.String("data-dir", desktop.DefaultDataDir(), "sync state directory")
	debugConsole := flag.Bool("console", false, "keep console logging attached")
	flag.Parse()
	_ = debugConsole

	if _, err := desktop.SetupLogging(*dataDir); err == nil {
		defer desktop.CloseLogging()
	}

	app, err := desktop.New(desktop.Config{
		DataDir:   *dataDir,
		ServerURL: *server,
		Email:     *email,
		Password:  *password,
		Folder:    *folder,
	})
	if err != nil {
		fatal(err)
	}
	defer app.Close()

	if *server != "" && *email != "" && *password != "" {
		if err := app.SaveConnection(*server, *email, *password); err != nil {
			desktop.Logf("initial login: %v", err)
		} else {
			_ = app.Start()
		}
	}
	if *folder != "" {
		_ = os.MkdirAll(*folder, 0o755)
		if err := app.AddFolder(filepath.Clean(*folder)); err != nil {
			desktop.Logf("add folder: %v", err)
		}
		_ = app.Start()
	}

	if err := desktop.RunUI(app); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "necipdrive-sync:", err)
	os.Exit(1)
}
