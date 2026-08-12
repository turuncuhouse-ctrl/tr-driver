//go:build unix

package storage

import "syscall"

// DiskSpace returns total and available bytes for the filesystem containing path.
func DiskSpace(path string) (total, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize)
	if bsize <= 0 {
		bsize = 512
	}
	total = bsize * int64(st.Blocks)
	free = bsize * int64(st.Bavail)
	return total, free, nil
}
