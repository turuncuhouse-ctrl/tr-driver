//go:build windows

package desktop

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	instanceMutexName = "Local\\TRDriverSyncSingleton"
	ipcPipeName       = `\\.\pipe\TRDriverSyncIPC`
	cmdShowFlyout     = 1
	cmdShowSettings   = 2
	cmdQuit           = 3
)

type InstanceLock struct {
	mutex windows.Handle
	ln    net.Listener
	once  sync.Once
}

type IPCHandler func(cmd int)

func AcquireInstance(dataDir string) (*InstanceLock, bool, error) {
	name, err := windows.UTF16PtrFromString(instanceMutexName)
	if err != nil {
		return nil, false, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		// ERROR_ALREADY_EXISTS still returns a handle
		if err == windows.ERROR_ALREADY_EXISTS {
			if handle != 0 {
				_ = windows.CloseHandle(handle)
			}
			return nil, false, nil
		}
		return nil, false, err
	}
	if handle == 0 {
		return nil, false, fmt.Errorf("create mutex failed")
	}
	last := windows.GetLastError()
	if last == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(handle)
		return nil, false, nil
	}

	lock := &InstanceLock{mutex: handle}
	ln, err := listenPipe()
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, false, err
	}
	lock.ln = ln
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.WriteFile(filepath.Join(dataDir, "instance.pid"), []byte(fmt.Sprintf("%d", os.Getpid())), 0o644)
	return lock, true, nil
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
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				var buf [4]byte
				if _, err := c.Read(buf[:]); err != nil {
					return
				}
				cmd := int(binary.LittleEndian.Uint32(buf[:]))
				if handler != nil {
					handler(cmd)
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
		if l.mutex != 0 {
			_ = windows.CloseHandle(l.mutex)
		}
	})
}

func SignalExistingInstance(cmd int) error {
	conn, err := dialPipe()
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(cmd))
	_, err = conn.Write(buf[:])
	return err
}

func listenPipe() (net.Listener, error) {
	return winPipeListen(ipcPipeName)
}

func dialPipe() (net.Conn, error) {
	return winPipeDial(ipcPipeName)
}

// Minimal named-pipe helpers using Windows APIs without third-party deps.
func winPipeListen(name string) (net.Listener, error) {
	return &pipeListener{name: name}, nil
}

type pipeListener struct {
	name   string
	closed bool
	mu     sync.Mutex
}

func (l *pipeListener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, net.ErrClosed
		}
		l.mu.Unlock()
		h, err := createNamedPipe(l.name)
		if err != nil {
			return nil, err
		}
		if err := connectNamedPipe(h); err != nil {
			_ = windows.CloseHandle(h)
			continue
		}
		return &pipeConn{h: h}, nil
	}
}

func (l *pipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	// Connect a dummy client to unblock Accept.
	go func() { _, _ = winPipeDial(l.name) }()
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr(l.name) }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

type pipeConn struct{ h windows.Handle }

func (c *pipeConn) Read(b []byte) (int, error) {
	var n uint32
	err := windows.ReadFile(c.h, b, &n, nil)
	return int(n), err
}
func (c *pipeConn) Write(b []byte) (int, error) {
	var n uint32
	err := windows.WriteFile(c.h, b, &n, nil)
	return int(n), err
}
func (c *pipeConn) Close() error                       { return windows.CloseHandle(c.h) }
func (c *pipeConn) LocalAddr() net.Addr                { return pipeAddr(ipcPipeName) }
func (c *pipeConn) RemoteAddr() net.Addr               { return pipeAddr(ipcPipeName) }
func (c *pipeConn) SetDeadline(t time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return nil }

func createNamedPipe(name string) (windows.Handle, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	const (
		pipeAccessDuplex = 0x00000003
		pipeTypeMessage  = 0x00000004
		pipeReadMessage  = 0x00000002
		pipeWait         = 0x00000000
		pipeUnlimited    = 255
	)
	h, _, e := procCreateNamedPipeW.Call(
		uintptr(unsafe.Pointer(n)),
		pipeAccessDuplex,
		pipeTypeMessage|pipeReadMessage|pipeWait,
		pipeUnlimited,
		4096, 4096, 0, 0,
	)
	if h == 0 || h == uintptr(windows.InvalidHandle) {
		return 0, e
	}
	return windows.Handle(h), nil
}

func connectNamedPipe(h windows.Handle) error {
	r, _, e := procConnectNamedPipe.Call(uintptr(h), 0)
	if r == 0 {
		if e == windows.ERROR_PIPE_CONNECTED {
			return nil
		}
		return e
	}
	return nil
}

func winPipeDial(name string) (net.Conn, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(n, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}
	return &pipeConn{h: h}, nil
}

var (
	modKernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procCreateNamedPipeW   = modKernel32.NewProc("CreateNamedPipeW")
	procConnectNamedPipe   = modKernel32.NewProc("ConnectNamedPipe")
)
