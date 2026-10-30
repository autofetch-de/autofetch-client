package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// estimateOutputBytes reads archive metadata without extracting payloads.
// The estimate covers only the current layer. Nested archives can expand
// further and are still guarded by the normal extraction limits.
func estimateOutputBytes(ctx context.Context, source string, limits Limits) (int64, error) {
	limits = limits.withDefaults()
	lower := strings.ToLower(source)
	var total int64
	add := func(n int64) error {
		if n < 0 || n > limits.MaxFileBytes || n > limits.MaxTotalBytes-total {
			return fmt.Errorf("archive_uncompressed_limit")
		}
		total += n
		return nil
	}
	switch {
	case strings.HasSuffix(lower, ".zip"):
		zr, err := zip.OpenReader(source)
		if err != nil {
			return 0, err
		}
		defer zr.Close()
		if len(zr.File) > limits.MaxEntries {
			return 0, fmt.Errorf("archive_too_many_entries")
		}
		for _, f := range zr.File {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if _, err := validName(f.Name); err != nil {
				return 0, err
			}
			if f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
				return 0, fmt.Errorf("archive_special_entry_rejected")
			}
			if f.FileInfo().IsDir() {
				continue
			}
			if f.UncompressedSize64 > uint64(limits.MaxFileBytes) || f.UncompressedSize64 > uint64(limits.MaxTotalBytes-total) {
				return 0, fmt.Errorf("archive_uncompressed_limit")
			}
			if err := add(int64(f.UncompressedSize64)); err != nil {
				return 0, err
			}
		}
	case strings.HasSuffix(lower, ".tar") || strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(source)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		var r io.Reader = f
		if strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".tgz") {
			g, err := gzip.NewReader(f)
			if err != nil {
				return 0, err
			}
			defer g.Close()
			r = g
		}
		tr := tar.NewReader(r)
		count := 0
		for {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return 0, err
			}
			count++
			if count > limits.MaxEntries {
				return 0, fmt.Errorf("archive_too_many_entries")
			}
			if _, err := validName(hdr.Name); err != nil {
				return 0, err
			}
			switch hdr.Typeflag {
			case tar.TypeDir:
			case tar.TypeReg, tar.TypeRegA:
				if err := add(hdr.Size); err != nil {
					return 0, err
				}
			default:
				return 0, fmt.Errorf("archive_special_entry_rejected")
			}
		}
	case strings.HasSuffix(lower, ".rar") || strings.HasSuffix(lower, ".7z") || volumeLeader(source):
		binary, err := sevenZipBinary()
		if err != nil {
			return 0, err
		}
		abs, err := filepath.Abs(source)
		if err != nil {
			return 0, err
		}
		cmd := exec.CommandContext(ctx, binary, "l", "-slt", "-ba", "-sccUTF-8", "-p-", "-bsp0", abs)
		cmd.Stdin = bytes.NewReader(nil)
		output := &boundedBuffer{maximum: maxSevenZipListing}
		cmd.Stdout = output
		cmd.Stderr = &boundedBuffer{maximum: 8192}
		if err := cmd.Run(); err != nil {
			return 0, fmt.Errorf("archive_7zip_list_failed: %w", err)
		}
		entries, err := parseSevenZipList(output.Bytes(), limits, volumeLeader(source))
		if err != nil {
			return 0, err
		}
		for _, e := range entries {
			if !e.dir {
				if err := add(e.size); err != nil {
					return 0, err
				}
			}
		}
	default:
		return 0, ErrUnsupported
	}
	return total, nil
}

const minDiskReserve int64 = 64 << 20
const maxDiskReserve int64 = 512 << 20

// checkExtractionSpace checks the actual filesystem used for the temporary
// extraction directory. On platforms without a free-space query, the normal
// bounded writes still protect integrity, but no capacity claim is made.
func checkExtractionSpace(dir string, bytes int64) error {
	free, known, err := freeDiskBytes(dir)
	if err != nil {
		return fmt.Errorf("archive_disk_check_failed: %w", err)
	}
	if !known {
		return nil
	}
	reserve := bytes / 20 // 5% headroom for filesystem metadata and other writers
	if reserve < minDiskReserve {
		reserve = minDiskReserve
	}
	if reserve > maxDiskReserve {
		reserve = maxDiskReserve
	}
	if bytes > free || reserve > free-bytes {
		return fmt.Errorf("archive_insufficient_disk_space")
	}
	return nil
}
