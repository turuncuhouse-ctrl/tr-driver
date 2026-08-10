package desktop

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

var (
	logMu   sync.Mutex
	logFile *os.File
)

func SetupLogging(dataDir string) (string, error) {
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "sync.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	logMu.Lock()
	if logFile != nil {
		_ = logFile.Close()
	}
	logFile = f
	logMu.Unlock()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	log.Printf("necipdrive-sync logging to %s", path)
	return path, nil
}

func CloseLogging() {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}
}

func Logf(format string, args ...any) {
	log.Printf(format, args...)
}

func OpenLogDir(dataDir string) error {
	dir := filepath.Join(dataDir, "logs")
	_ = os.MkdirAll(dir, 0o755)
	return openPath(dir)
}

func openPath(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	return openFileExplorer(path)
}
