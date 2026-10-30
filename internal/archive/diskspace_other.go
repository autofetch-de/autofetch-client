//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !windows

package archive

func freeDiskBytes(string) (int64, bool, error) { return 0, false, nil }
