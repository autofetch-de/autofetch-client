package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrAlreadyRunning = errors.New("autofetch-gui is already running")

type Lock struct {
	path string
	file *os.File
}

func defaultLockPath(appName string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = "."
	}
	dir := filepath.Join(base, "autofetch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create lock dir: %w", err)
	}
	return filepath.Join(dir, appName+".lock"), nil
}

func Acquire(appName string) (*Lock, error) {
	path, err := defaultLockPath(appName)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	ok, err := lockFile(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !ok {
		_ = f.Close()
		return nil, ErrAlreadyRunning
	}
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
		_, _ = f.Seek(0, 0)
	}
	return &Lock{path: path, file: f}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = unlockFile(l.file)
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
