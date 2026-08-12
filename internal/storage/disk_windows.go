//go:build windows

package storage

import "golang.org/x/sys/windows"

// DiskSpace returns total and available bytes for the filesystem containing path.
func DiskSpace(path string) (total, free int64, err error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, &totalNumberOfBytes, &totalNumberOfFreeBytes); err != nil {
		return 0, 0, err
	}
	return int64(totalNumberOfBytes), int64(freeBytesAvailable), nil
}
