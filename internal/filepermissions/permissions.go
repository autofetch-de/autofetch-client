// Package filepermissions contains the client-side publication defaults.
// A future per-client portal setting can supply a mode here without changing
// the secure 0600 mode used for unfinished transfers and private metadata.
package filepermissions

import (
	"fmt"
	"os"
)

const (
	CompletedFileMode      os.FileMode = 0o644
	CompletedDirectoryMode os.FileMode = 0o755
)

// PrepareCompletedFile is only for verified payload files, never .part files
// still in progress, download journals, credentials, or metadata sidecars.
func PrepareCompletedFile(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("completed_file_not_regular: %s", path)
	}
	return os.Chmod(path, CompletedFileMode)
}

// PublishCompletedFile sets the final mode before making the completed payload
// visible at the final name. Unfinished .part files retain their private mode.
func PublishCompletedFile(partPath, destPath string) error {
	if err := PrepareCompletedFile(partPath); err != nil {
		return err
	}
	return os.Rename(partPath, destPath)
}
