package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/autofetch-de/autofetch-client/internal/filepermissions"
)

const publishIntent = ".autofetch-publish-intent.json"
const publishInProgress = ".autofetch-publishing.json"

type publishPlan struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`   // file or directory
	Target  string `json:"target"` // single safe filename in source directory
	Root    string `json:"root"`   // relative path within verified extraction
}

func archiveBaseName(source string) string {
	b := filepath.Base(source)
	lower := strings.ToLower(b)
	if m := rarFirst.FindStringSubmatch(b); m != nil {
		return m[1]
	}
	if m := sevenFirst.FindStringSubmatch(b); m != nil {
		return strings.TrimSuffix(m[1], filepath.Ext(m[1]))
	}
	for _, suf := range []string{".tar.gz", ".tgz", ".tar", ".zip", ".rar", ".7z"} {
		if strings.HasSuffix(lower, suf) {
			return b[:len(b)-len(suf)]
		}
	}
	return b
}

// determinePublishPlan counts payload files, not extraction metadata or
// archive volumes. One file is presented directly. Several files are kept
// together in their existing common top-level directory or a directory
// named after the archive.
func determinePublishPlan(source, extracted string) (publishPlan, error) {
	entries, err := os.ReadDir(extracted)
	if err != nil {
		return publishPlan{}, err
	}
	top := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.Name() == markerName || e.Name() == publishIntent {
			continue
		}
		top = append(top, e)
	}
	var onlyFile string
	count := 0
	err = filepath.WalkDir(extracted, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == extracted {
			return nil
		}
		rel, err := filepath.Rel(extracted, path)
		if err != nil {
			return err
		}
		if rel == markerName || rel == publishIntent {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive_special_entry_rejected")
		}
		if filepath.Base(path) == publishInProgress {
			return fmt.Errorf("archive_reserved_path")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("archive_special_entry_rejected")
		}
		count++
		onlyFile = rel
		return nil
	})
	if err != nil {
		return publishPlan{}, err
	}
	if count == 0 {
		return publishPlan{}, fmt.Errorf("archive_no_payload_files")
	}
	if count == 1 {
		target := filepath.Base(onlyFile)
		if _, err := validName(target); err != nil {
			return publishPlan{}, err
		}
		return publishPlan{Version: 2, Kind: "file", Target: target, Root: onlyFile}, nil
	}
	if len(top) == 1 && top[0].IsDir() {
		return publishPlan{Version: 2, Kind: "directory", Target: top[0].Name(), Root: top[0].Name()}, nil
	}
	target := archiveBaseName(source)
	if target == "" {
		return publishPlan{}, fmt.Errorf("archive_invalid_destination")
	}
	if _, err := validName(target); err != nil {
		return publishPlan{}, err
	}
	return publishPlan{Version: 2, Kind: "directory", Target: target, Root: "."}, nil
}

func validPublishPlan(plan publishPlan, extracted string) error {
	if plan.Version != 2 || (plan.Kind != "file" && plan.Kind != "directory") || plan.Target == "" || plan.Root == "" {
		return fmt.Errorf("archive_publish_intent_invalid")
	}
	if name, err := validName(plan.Target); err != nil || name != plan.Target || strings.ContainsAny(plan.Target, "/\\") {
		return fmt.Errorf("archive_publish_intent_invalid")
	}
	if plan.Root != "." {
		if name, err := validName(filepath.ToSlash(plan.Root)); err != nil || name != filepath.ToSlash(plan.Root) {
			return fmt.Errorf("archive_publish_intent_invalid")
		}
	}
	if plan.Root == "." && plan.Kind == "file" {
		return fmt.Errorf("archive_publish_intent_invalid")
	}
	path := filepath.Join(extracted, plan.Root)
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("archive_publish_intent_invalid")
	}
	if plan.Kind == "file" && !st.Mode().IsRegular() || plan.Kind == "directory" && !st.IsDir() {
		return fmt.Errorf("archive_publish_intent_invalid")
	}
	return nil
}

func loadOrCreatePublishPlan(source, extracted string) (publishPlan, bool, error) {
	name := filepath.Join(extracted, publishIntent)
	data, err := os.ReadFile(name)
	if err == nil {
		var plan publishPlan
		if json.Unmarshal(data, &plan) != nil {
			return publishPlan{}, false, fmt.Errorf("archive_publish_intent_invalid")
		}
		if err := validPublishPlan(plan, extracted); err != nil {
			return publishPlan{}, false, err
		}
		dest := filepath.Join(filepath.Dir(source), plan.Target)
		if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
			return plan, true, nil
		}
		return plan, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return publishPlan{}, false, err
	}
	plan, err := determinePublishPlan(source, extracted)
	if err != nil {
		return publishPlan{}, false, err
	}
	if err = validPublishPlan(plan, extracted); err != nil {
		return publishPlan{}, false, err
	}
	// An existing output must be rejected BEFORE an intent is saved: recovery
	// may otherwise mistake user-owned content for an interrupted publish.
	dest := filepath.Join(filepath.Dir(source), plan.Target)
	if _, err := os.Lstat(dest); err == nil {
		return publishPlan{}, false, fmt.Errorf("archive_destination_exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return publishPlan{}, false, err
	}
	data, err = json.Marshal(plan)
	if err != nil {
		return publishPlan{}, false, err
	}
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return publishPlan{}, false, err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return publishPlan{}, false, writeErr
	}
	if closeErr != nil {
		return publishPlan{}, false, closeErr
	}
	if err := syncDirectory(extracted); err != nil {
		return publishPlan{}, false, err
	}
	return plan, true, nil
}

// PublishSameDirectory keeps a verified extraction as the recovery source.
// A single file is linked next to the original. Multiple files are published
// in exactly one directory (existing common root or archive-derived name).
// Original files are never removed here.
func PublishSameDirectory(ctx context.Context, source, extracted string) (string, error) {
	st, err := os.Lstat(extracted)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("archive_staging_invalid")
	}
	plan, fresh, err := loadOrCreatePublishPlan(source, extracted)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(filepath.Dir(source), plan.Target)
	root := filepath.Join(extracted, plan.Root)
	if plan.Kind == "file" {
		// Handle recoveries from an earlier version's private extracted file.
		if err := filepermissions.PrepareCompletedFile(root); err != nil {
			return "", err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := os.Link(root, dest); err != nil {
			if !errors.Is(err, os.ErrExist) || fresh {
				return "", fmt.Errorf("archive_destination_exists: %w", err)
			}
		}
	} else {
		// The extraction has already completed. Make hardlinked content
		// readable; destination directories remain private until verified.
		if err := prepareReadableTree(ctx, root); err != nil {
			return "", err
		}
		if fresh {
			if err := os.Mkdir(dest, 0700); err != nil {
				return "", fmt.Errorf("archive_destination_exists: %w", err)
			}
			// A startup interrupted before this marker is written is stopped
			// conservatively; it must not merge into an unmarked directory.
			data, _ := json.Marshal(plan)
			if err := os.WriteFile(filepath.Join(dest, publishInProgress), data, 0600); err != nil {
				return "", err
			}
		} else {
			// Existing directory may only be resumed if it is ours or the
			// completed output can already be fully verified below.
			marker, markerErr := os.ReadFile(filepath.Join(dest, publishInProgress))
			if markerErr == nil {
				var existing publishPlan
				if json.Unmarshal(marker, &existing) != nil || existing != plan {
					return "", fmt.Errorf("archive_destination_exists")
				}
			} else if !errors.Is(markerErr, os.ErrNotExist) {
				return "", markerErr
			}
			if errors.Is(markerErr, os.ErrNotExist) {
				if err := verifyPublishPlan(ctx, root, dest, plan); err != nil {
					return "", fmt.Errorf("archive_destination_exists")
				}
				if err := preparePublishedDirectories(ctx, root, dest); err != nil {
					return "", err
				}
				return dest, nil
			}
		}
		if err := copyLinkedTree(ctx, root, dest); err != nil {
			return "", err
		}
	}
	if err := verifyPublishPlan(ctx, root, dest, plan); err != nil {
		return "", err
	}
	if plan.Kind == "directory" {
		// All links are verified now. Open the completed directory tree
		// to other local readers before clearing the private progress marker.
		if err := preparePublishedDirectories(ctx, root, dest); err != nil {
			return "", err
		}
		if err := os.Remove(filepath.Join(dest, publishInProgress)); err != nil {
			return "", err
		}
	}
	if err := syncDirectory(filepath.Dir(dest)); err != nil {
		return "", err
	}
	return dest, nil
}

func copyLinkedTree(ctx context.Context, root, dest string) error {
	return filepath.WalkDir(root, func(src string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if src == root {
			return nil
		}
		rel, err := filepath.Rel(root, src)
		if err != nil {
			return err
		}
		// When publishing the entire staging tree, omit internal markers.
		if rel == markerName || rel == publishIntent {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive_special_entry_rejected")
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			if err := os.Mkdir(target, 0700); err != nil {
				if !errors.Is(err, os.ErrExist) {
					return err
				}
				st, er := os.Lstat(target)
				if er != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("archive_destination_exists")
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("archive_special_entry_rejected")
		}
		if err := os.Link(src, target); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			a, er1 := os.Lstat(src)
			b, er2 := os.Lstat(target)
			if er1 != nil || er2 != nil || !b.Mode().IsRegular() || !os.SameFile(a, b) {
				return fmt.Errorf("archive_destination_exists")
			}
		}
		return nil
	})
}

func verifyPublishPlan(ctx context.Context, root, dest string, plan publishPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan.Kind == "file" {
		a, errA := os.Lstat(root)
		b, errB := os.Lstat(dest)
		if errA != nil || errB != nil || !a.Mode().IsRegular() || !b.Mode().IsRegular() || !os.SameFile(a, b) {
			return fmt.Errorf("archive_published_file_changed")
		}
		return nil
	}
	return filepath.WalkDir(root, func(src string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, src)
		if err != nil {
			return err
		}
		if rel == "." || rel == markerName || rel == publishIntent {
			return nil
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("archive_unsafe_path")
		}
		a, er1 := os.Lstat(src)
		b, er2 := os.Lstat(filepath.Join(dest, rel))
		if er1 != nil || er2 != nil || a.Mode()&os.ModeSymlink != 0 || b.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive_published_file_missing")
		}
		if d.IsDir() {
			if !b.IsDir() {
				return fmt.Errorf("archive_published_file_changed")
			}
		} else if !a.Mode().IsRegular() || !b.Mode().IsRegular() || !os.SameFile(a, b) {
			return fmt.Errorf("archive_published_file_changed")
		}
		return nil
	})
}

// VerifyPublishedSameDirectory is also used immediately before deletion and
// during recovery. Do not rely on a path merely existing.
func VerifyPublishedSameDirectory(ctx context.Context, source, extracted string) error {
	plan, _, err := loadOrCreatePublishPlan(source, extracted)
	if err != nil {
		return err
	}
	return verifyPublishPlan(ctx, filepath.Join(extracted, plan.Root), filepath.Join(filepath.Dir(source), plan.Target), plan)
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Some platforms do not support fsync on directories; best effort.
	_ = f.Sync()
	return nil
}
