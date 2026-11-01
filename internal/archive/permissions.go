package archive

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/autofetch-de/autofetch-client/internal/filepermissions"
)

// prepareReadableTree changes only files produced by our extractor/publisher.
// Run it after successful extraction/verification and before exposing the
// completed destination. Private state/intent markers stay at 0600.
// Directory modes are updated from the leaves upwards, with root last.
func prepareReadableTree(ctx context.Context, root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive_special_entry_rejected")
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("archive_special_entry_rejected")
		}
		if path == filepath.Join(root, markerName) ||
			path == filepath.Join(root, publishIntent) ||
			path == filepath.Join(root, publishInProgress) {
			return nil
		}
		return filepermissions.PrepareCompletedFile(path)
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Chmod(dirs[i], filepermissions.CompletedDirectoryMode); err != nil {
			return err
		}
	}
	return nil
}

// preparePublishedDirectories opens only the directories that belong to the
// verified extracted tree. In a recovered destination there may be unrelated
// user-owned files; their permissions must not be changed.
// Payload file modes are already set on the extraction source, with which
// the published files share their inode via hard links.
func preparePublishedDirectories(ctx context.Context, root, dest string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		published := filepath.Join(dest, rel)
		st, err := os.Lstat(published)
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive_published_directory_changed")
		}
		dirs = append(dirs, published)
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Chmod(dirs[i], filepermissions.CompletedDirectoryMode); err != nil {
			return err
		}
	}
	return nil
}
