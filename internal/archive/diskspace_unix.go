//go:build linux || darwin || freebsd || netbsd || openbsd

package archive

import "syscall"

func freeDiskBytes(dir string) (int64, bool, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, false, err
	}
	// Bavail excludes blocks reserved for privileged users.
	blocks := uint64(stat.Bavail)
	size := uint64(stat.Bsize)
	if size == 0 || blocks > uint64(^uint64(0)>>1)/size {
		return 0, false, nil
	}
	return int64(blocks * size), true, nil
}
