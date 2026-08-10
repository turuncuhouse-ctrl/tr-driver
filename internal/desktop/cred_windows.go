//go:build windows

package desktop

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modAdvapi32      = windows.NewLazySystemDLL("advapi32.dll")
	procCredWriteW   = modAdvapi32.NewProc("CredWriteW")
	procCredReadW    = modAdvapi32.NewProc("CredReadW")
	procCredFree     = modAdvapi32.NewProc("CredFree")
	procCredDeleteW  = modAdvapi32.NewProc("CredDeleteW")
)

type credWin struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

const (
	credTypeGeneric = 1
	credPersistLocalMachine = 2
)

func WriteCredential(target, username, secret string) error {
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	u, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return err
	}
	blob := append([]byte(nil), secret...)
	var blobPtr *byte
	if len(blob) > 0 {
		blobPtr = &blob[0]
	}
	cred := credWin{
		Type:               credTypeGeneric,
		TargetName:         t,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     blobPtr,
		Persist:            credPersistLocalMachine,
		UserName:           u,
	}
	r, _, e := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if r == 0 {
		return e
	}
	return nil
}

func ReadCredential(target string) (string, error) {
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return "", err
	}
	var out *credWin
	r, _, e := procCredReadW.Call(uintptr(unsafe.Pointer(t)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		if errno, ok := e.(syscall.Errno); ok && errno == windows.ERROR_NOT_FOUND {
			return "", nil
		}
		return "", e
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(out)))
	if out.CredentialBlob == nil || out.CredentialBlobSize == 0 {
		return "", nil
	}
	b := unsafe.Slice(out.CredentialBlob, out.CredentialBlobSize)
	return string(b), nil
}

func DeleteCredential(target string) error {
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r, _, e := procCredDeleteW.Call(uintptr(unsafe.Pointer(t)), credTypeGeneric, 0)
	if r == 0 {
		if errno, ok := e.(syscall.Errno); ok && errno == windows.ERROR_NOT_FOUND {
			return nil
		}
		return e
	}
	return nil
}
