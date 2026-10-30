package archive

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// expandNested operates only inside the unpublished outer staging directory.
// No nested result is visible to users unless the COMPLETE outer extraction
// succeeds. Total space and entries are bounded across all levels.
func expandNested(ctx context.Context, root string, limits Limits, remainingDepth int) error {
	if remainingDepth <= 0 {
		return nil
	}
	var pending []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive_special_entry_rejected")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("archive_special_entry_rejected")
		}
		if Supported(p) {
			pending = append(pending, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(pending) > 128 {
		return fmt.Errorf("archive_nested_count_limit")
	}
	for _, p := range pending {
		used, count, err := stageUsage(ctx, root, limits)
		if err != nil {
			return err
		}
		if used >= limits.MaxTotalBytes || count >= limits.MaxEntries {
			return fmt.Errorf("archive_uncompressed_limit")
		}
		childLimits := limits
		childLimits.MaxTotalBytes -= used
		childLimits.MaxEntries -= count
		if childLimits.MaxTotalBytes <= 0 || childLimits.MaxEntries <= 0 {
			return fmt.Errorf("archive_uncompressed_limit")
		}
		if _, err := extract(ctx, p, "", childLimits, remainingDepth-1, nil); err != nil {
			return fmt.Errorf("archive_nested_failed: %w", err)
		}
		if _, _, err := stageUsage(ctx, root, limits); err != nil {
			return err
		}
	}
	return nil
}

func stageUsage(ctx context.Context, root string, limits Limits) (int64, int, error) {
	limits = limits.withDefaults()
	var size int64
	var count int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == root {
			return nil
		}
		count++
		if count > limits.MaxEntries {
			return fmt.Errorf("archive_too_many_entries")
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive_special_entry_rejected")
		}
		if !d.IsDir() {
			if !d.Type().IsRegular() {
				return fmt.Errorf("archive_special_entry_rejected")
			}
			st, err := d.Info()
			if err != nil {
				return err
			}
			if st.Size() > limits.MaxTotalBytes-size {
				return fmt.Errorf("archive_uncompressed_limit")
			}
			size += st.Size()
		}
		return nil
	})
	return size, count, err
}
