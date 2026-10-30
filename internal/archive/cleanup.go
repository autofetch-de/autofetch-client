package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

type cleanupFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func fileDigest(ctx context.Context, name string) (string, error) {
	fi, err := os.Lstat(name)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("archive_source_not_regular")
	}
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, &contextReader{ctx: ctx, reader: f})
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func snapshotCleanupFiles(ctx context.Context, source, sourceHash string) ([]cleanupFile, error) {
	volumes, err := collectVolumes(source)
	if err != nil {
		return nil, err
	}
	files := make([]cleanupFile, 0, len(volumes))
	for _, path := range volumes {
		hash, err := fileDigest(ctx, path)
		if err != nil {
			return nil, err
		}
		if path == source && hash != sourceHash {
			return nil, fmt.Errorf("archive_source_changed")
		}
		files = append(files, cleanupFile{Path: path, SHA256: hash})
	}
	return files, nil
}

// Cleanup never deletes an unverified file and only acts after publication
// is durable and the download receipt has recorded intentional consumption.
// A partially completed multi-volume cleanup can resume from saved digests.
func cleanupOriginalFiles(ctx context.Context, files []cleanupFile) error {
	// Check every remaining volume before touching even the first one.
	// A changed or unexpectedly non-regular sibling must not cause partial
	// deletion of the original multi-volume set.
	for _, entry := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		actual, err := fileDigest(ctx, entry.Path)
		if os.IsNotExist(err) {
			continue
		} // already removed by earlier cleanup
		if err != nil || actual != entry.SHA256 {
			return fmt.Errorf("archive_source_changed")
		}
	}
	for _, entry := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		actual, err := fileDigest(ctx, entry.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || actual != entry.SHA256 {
			return fmt.Errorf("archive_source_changed")
		}
		if err := os.Remove(entry.Path); err != nil {
			return err
		}
	}
	return nil
}
