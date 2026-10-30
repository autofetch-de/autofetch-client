package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// safeDownloadPath treats output.dir and output.filename_video as untrusted
// server data. The download root is user-configured; output must stay inside.
func safeDownloadPath(baseDir, dir, filename string) (string, error) {
	if strings.TrimSpace(baseDir) == "" {
		return "", fmt.Errorf("download_root_missing")
	}
	if filename == "" || filename == "." || filename == ".." || strings.ContainsAny(filename, "/\\\x00\r\n") || filepath.IsAbs(filename) || filepath.VolumeName(filename) != "" {
		return "", fmt.Errorf("invalid_download_filename")
	}
	if strings.ContainsAny(dir, "\\\x00") || filepath.IsAbs(dir) || filepath.VolumeName(dir) != "" {
		return "", fmt.Errorf("invalid_download_directory")
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == ".." {
			return "", fmt.Errorf("invalid_download_directory")
		}
	}
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, dir, filename)
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("download_path_outside_root")
	}
	// Refuse symlinks inside the user-chosen download root. User-configured
	// baseDir itself can be a symlink (e.g. a mounted drive).
	parent := base
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		parent = filepath.Join(parent, seg)
		fi, err := os.Lstat(parent)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("download_path_symlink")
		}
	}
	return target, nil
}

// chooseNewJobOutput never overwrites a prior independent download.
// A re-leased job retains its recorded path; a fresh job reserves a unique
// filename when a previous file already occupies the preferred name.
func chooseNewJobOutput(preferred, jobID string) (string, error) {
	if availableOutputSlot(preferred) {
		return preferred, nil
	}
	ext := filepath.Ext(preferred)
	lower := strings.ToLower(preferred)
	for _, compound := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".7z.001", ".zip.001"} {
		if strings.HasSuffix(lower, compound) {
			ext = preferred[len(preferred)-len(compound):]
			break
		}
	}
	stem := strings.TrimSuffix(preferred, ext)
	safeID := strings.ReplaceAll(jobID, "-", "")
	if len(safeID) > 12 {
		safeID = safeID[:12]
	}
	if safeID == "" {
		return "", fmt.Errorf("download_job_id_missing")
	}
	for i := 0; i < 100; i++ {
		candidate := fmt.Sprintf("%s.%s%s", stem, safeID, ext)
		if i > 0 {
			candidate = fmt.Sprintf("%s.%s.%d%s", stem, safeID, i, ext)
		}
		if availableOutputSlot(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("download_filename_collision")
}

// A leftover partial is also owned by some previous download and must never
// be resumed accidentally for a new job, even when the final file is absent.
func availableOutputSlot(path string) bool {
	for _, suffix := range []string{"", ".part", ".part.meta.json"} {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	return true
}
