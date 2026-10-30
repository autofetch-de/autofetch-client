//go:build windows

package archive

import (
	"fmt"
	"syscall"
	"unsafe"
)

func freeDiskBytes(dir string) (int64, bool, error) {
	path, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false, err
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
	var free uint64
	r, _, callErr := proc.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&free)), 0, 0)
	if r == 0 {
		return 0, false, fmt.Errorf("disk query failed: %w", callErr)
	}
	if free > uint64(^uint64(0)>>1) {
		return 0, false, nil
	}
	return int64(free), true, nil
}
