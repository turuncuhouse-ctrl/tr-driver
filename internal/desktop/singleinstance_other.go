//go:build !windows

package desktop

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
)

const (
	cmdShowFlyout   = 1
	cmdShowSettings = 2
	cmdQuit         = 3
)

type InstanceLock struct {
	ln   net.Listener
	once sync.Once
	path string
}

type IPCHandler func(cmd int)

func AcquireInstance(dataDir string) (*InstanceLock, bool, error) {
	_ = os.MkdirAll(dataDir, 0o755)
	sock := filepath.Join(dataDir, "instance.sock")
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, false, nil
	}
	return &InstanceLock{ln: ln, path: sock}, true, nil
}

func (l *InstanceLock) ServeIPC(handler IPCHandler) {
	if l == nil || l.ln == nil {
		return
	}
	go func() {
		for {
			conn, err := l.ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1)
				if _, err := c.Read(buf); err == nil && handler != nil {
					handler(int(buf[0]))
				}
			}(conn)
		}
	}()
}

func (l *InstanceLock) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.ln != nil {
			_ = l.ln.Close()
		}
		if l.path != "" {
			_ = os.Remove(l.path)
		}
	})
}

func SignalExistingInstance(cmd int) error {
	dir := DefaultDataDir()
	conn, err := net.Dial("unix", filepath.Join(dir, "instance.sock"))
	if err != nil {
		return fmt.Errorf("no running instance")
	}
	defer conn.Close()
	_, err = conn.Write([]byte{byte(cmd)})
	return err
}
