// Package archive provides conservative local extraction of untrusted downloads.
// An extraction is staged in a private sibling directory and published only
// after all entries have passed validation. The original archive is untouched.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var ErrUnsupported = errors.New("archive_format_unsupported")

// Limits are upper bounds, not estimates of available local disk space.
type Limits struct {
	MaxEntries          int
	MaxFileBytes        int64
	MaxTotalBytes       int64
	MaxCompressionRatio uint64
}

func (l Limits) withDefaults() Limits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = 25000
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = 16 << 30
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = 24 << 30
	}
	if l.MaxCompressionRatio == 0 {
		l.MaxCompressionRatio = 300
	}
	return l
}

func Supported(filename string) bool {
	name := strings.ToLower(filename)
	if volumeLeader(filename) {
		return true
	}
	// Non-leading parts must never produce separate extraction jobs.
	if rarVolume.MatchString(filepath.Base(name)) || sevenVolume.MatchString(filepath.Base(name)) {
		return false
	}
	return strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar") || strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz") || strings.HasSuffix(name, ".rar") || strings.HasSuffix(name, ".7z")
}

// Extract writes to source + ".extracted". Existing output is never replaced.
// No links, device entries or executable permission bits are materialized.
// On error, staged output is removed; the original archive is never deleted.
func Extract(ctx context.Context, source string, limits Limits) (string, error) {
	return extract(ctx, source, "", limits, 0, nil)
}

// ExtractVerified records the download journal SHA-256 inside the finished
// directory. This allows crash recovery if the process dies after publishing
// the extracted files but before acknowledging completion in the queue.
func ExtractVerified(ctx context.Context, source, digest string, limits Limits) (string, error) {
	if len(digest) != 64 {
		return "", fmt.Errorf("archive_digest_missing")
	}
	return extract(ctx, source, digest, limits, 0, nil)
}

// ExtractVerifiedNested enables optional expansion of nested archives, at most
// three additional layers, within the outer unpublished staging directory.
func ExtractVerifiedNested(ctx context.Context, source, digest string, limits Limits, depth int) (string, error) {
	if len(digest) != 64 {
		return "", fmt.Errorf("archive_digest_missing")
	}
	if depth < 0 || depth > 3 {
		return "", fmt.Errorf("archive_nested_depth_invalid")
	}
	return extract(ctx, source, digest, limits, depth, nil)
}

// Progress describes completed output bytes for the current archive layer.
// For nested extraction, further expansion is reported as a separate phase;
// the original layer estimate is NOT a prediction for nested outputs.
type Progress struct {
	Phase      string
	Bytes      int64
	TotalBytes int64
}

// ExtractVerifiedNestedProgress keeps the public extraction API backwards
// compatible while allowing the local queue to persist progress.
func ExtractVerifiedNestedProgress(ctx context.Context, source, digest string, limits Limits, depth int, notify func(Progress)) (string, error) {
	if len(digest) != 64 {
		return "", fmt.Errorf("archive_digest_missing")
	}
	if depth < 0 || depth > 3 {
		return "", fmt.Errorf("archive_nested_depth_invalid")
	}
	return extract(ctx, source, digest, limits, depth, notify)
}

const markerName = ".autofetch-extraction-state.json"

type extractMarker struct {
	Version      int    `json:"version"`
	SHA256       string `json:"sha256"`
	VolumeDigest string `json:"volume_digest,omitempty"`
}

func finishedDirectory(dest, digest, signature string) bool {
	if digest == "" {
		return false
	}
	st, err := os.Lstat(dest)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return false
	}
	fi, err := os.Lstat(filepath.Join(dest, markerName))
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 256 {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dest, markerName))
	if err != nil {
		return false
	}
	var m extractMarker
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	return m.Version == 1 && m.SHA256 == digest && m.VolumeDigest == signature
}

func extract(ctx context.Context, source, digest string, limits Limits, depth int, notify func(Progress)) (string, error) {
	if !Supported(source) {
		return "", ErrUnsupported
	}
	limits = limits.withDefaults()
	volumes, err := collectVolumes(source)
	if err != nil {
		return "", err
	}
	signature, err := volumeSignature(ctx, volumes)
	if err != nil {
		return "", err
	}
	st, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("archive_source_not_regular")
	}
	dest := source + ".extracted"
	if _, err = os.Lstat(dest); err == nil {
		if finishedDirectory(dest, digest, signature) {
			return dest, nil
		}
		return "", fmt.Errorf("archive_destination_exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(dest)
	if notify != nil {
		notify(Progress{Phase: "preflight"})
	}
	estimate, err := estimateOutputBytes(ctx, source, limits)
	if err != nil {
		return "", err
	}
	if err := checkExtractionSpace(parent, estimate); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".autofetch-extract-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err = os.Chmod(stage, 0700); err != nil {
		return "", err
	}
	x := extractor{root: stage, limits: limits, names: make(map[string]string), spellings: make(map[string]string), ctx: ctx, notify: notify, estimated: estimate}
	x.emit("extracting")
	switch {
	case strings.HasSuffix(strings.ToLower(source), ".zip"):
		err = x.extractZIP(source)
	case strings.HasSuffix(strings.ToLower(source), ".rar"), strings.HasSuffix(strings.ToLower(source), ".7z"), volumeLeader(source):
		err = x.extractSevenZip(source, volumes)
	default:
		err = x.extractTAR(source, strings.HasSuffix(strings.ToLower(source), ".gz") || strings.HasSuffix(strings.ToLower(source), ".tgz"))
	}
	if err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if depth > 0 {
		x.emit("nested")
		if err := expandNested(ctx, stage, limits, depth); err != nil {
			return "", err
		}
	}
	if signature != "" {
		newSignature, err := volumeSignature(ctx, volumes)
		if err != nil {
			return "", err
		}
		if newSignature != signature {
			return "", fmt.Errorf("archive_volume_changed")
		}
	}
	if digest != "" {
		// Reject archives attempting to pre-create our private completion marker.
		if _, ok := x.names[strings.ToLower(markerName)]; ok {
			return "", fmt.Errorf("archive_reserved_path")
		}
		if _, ok := x.names[strings.ToLower(publishIntent)]; ok {
			return "", fmt.Errorf("archive_reserved_path")
		}
		content, err := json.Marshal(extractMarker{Version: 1, SHA256: digest, VolumeDigest: signature})
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(stage, markerName), content, 0600); err != nil {
			return "", err
		}
	}
	// On normal clients there is a single archive worker. Never overwrite a
	// destination created by a previous attempt or another application.
	if _, err = os.Lstat(dest); err == nil {
		if finishedDirectory(dest, digest, signature) {
			return dest, nil
		}
		return "", fmt.Errorf("archive_destination_exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Only publish a verified, complete tree with the legacy user-readable
	// permissions. The staging tree remains private until this point.
	if err := prepareReadableTree(ctx, stage); err != nil {
		return "", err
	}
	x.emit("publishing")
	if err = os.Rename(stage, dest); err != nil {
		return "", err
	}
	return dest, nil
}

type extractor struct {
	root      string
	limits    Limits
	ctx       context.Context
	count     int
	total     int64
	names     map[string]string // case-folded path -> "file" or "dir"
	spellings map[string]string // canonical spelling of each path component
	notify    func(Progress)
	estimated int64
	written   int64
}

func (x *extractor) emit(phase string) {
	if x.notify != nil {
		x.notify(Progress{Phase: phase, Bytes: x.written, TotalBytes: x.estimated})
	}
}

func validName(raw string) (string, error) {
	if raw == "" || strings.ContainsAny(raw, "\\\x00") || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("archive_unsafe_path")
	}
	// Many tar tools place a harmless ./ in front of every member.
	for strings.HasPrefix(raw, "./") {
		raw = strings.TrimPrefix(raw, "./")
	}
	raw = strings.TrimSuffix(raw, "/")
	if raw == "." || raw == "" {
		return "", nil
	}
	if len(raw) > 4096 {
		return "", fmt.Errorf("archive_unsafe_path")
	}
	for _, part := range strings.Split(raw, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 220 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.Contains(part, ":") {
			return "", fmt.Errorf("archive_unsafe_path")
		}
		// Windows device names and alternate data streams must never be materialized.
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && ((strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return "", fmt.Errorf("archive_unsafe_path")
		}
	}
	if path.Clean(raw) != raw {
		return "", fmt.Errorf("archive_unsafe_path")
	}
	return raw, nil
}

func (x *extractor) reserve(raw string, dir bool) (string, error) {
	name, err := validName(raw)
	if err != nil {
		return "", err
	}
	if name == "" {
		if dir {
			return "", nil
		}
		return "", fmt.Errorf("archive_unsafe_path")
	}
	x.count++
	if x.count > x.limits.MaxEntries {
		return "", fmt.Errorf("archive_too_many_entries")
	}
	kind := "file"
	if dir {
		kind = "dir"
	}
	parts := strings.Split(name, "/")
	for i := 1; i <= len(parts); i++ {
		key := strings.ToLower(strings.Join(parts[:i], "/"))
		want := "dir"
		if i == len(parts) {
			want = kind
		}
		if prev, ok := x.names[key]; ok {
			if x.spellings[key] != strings.Join(parts[:i], "/") {
				return "", fmt.Errorf("archive_duplicate_or_conflicting_path")
			}
			if prev != want || (i == len(parts) && !dir) {
				return "", fmt.Errorf("archive_duplicate_or_conflicting_path")
			}
		} else {
			x.names[key] = want
			x.spellings[key] = strings.Join(parts[:i], "/")
		}
	}
	return filepath.Join(x.root, filepath.FromSlash(name)), nil
}

func (x *extractor) writeFile(name string, n int64, reader io.Reader) error {
	if n < 0 || n > x.limits.MaxFileBytes || n > x.limits.MaxTotalBytes-x.total {
		return fmt.Errorf("archive_uncompressed_limit")
	}
	target, err := x.reserve(name, false)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	buf := make([]byte, 128*1024)
	remaining := n
	for remaining > 0 {
		if err = x.ctx.Err(); err != nil {
			break
		}
		size := int64(len(buf))
		if size > remaining {
			size = remaining
		}
		var got int
		got, err = io.ReadFull(reader, buf[:int(size)])
		if got > 0 {
			var written int
			written, err = f.Write(buf[:got])
			remaining -= int64(written)
			x.written += int64(written)
			if written > 0 {
				x.emit("extracting")
			}
			if err == nil && written != got {
				err = io.ErrShortWrite
			}
		}
		if err != nil {
			break
		}
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if remaining != 0 {
		return fmt.Errorf("archive_truncated_entry")
	}
	x.total += n
	return nil
}

func (x *extractor) writeDir(name string) error {
	target, err := x.reserve(name, true)
	if err != nil {
		return err
	}
	if target == "" {
		return nil
	}
	return os.MkdirAll(target, 0700)
}

func (x *extractor) extractZIP(source string) error {
	zr, err := zip.OpenReader(source)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) > x.limits.MaxEntries {
		return fmt.Errorf("archive_too_many_entries")
	}
	// Validate bounds before writing a single byte.
	var total uint64
	for _, f := range zr.File {
		if _, err := validName(f.Name); err != nil {
			return err
		}
		if f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return fmt.Errorf("archive_special_entry_rejected")
		}
		if f.FileInfo().IsDir() {
			if f.UncompressedSize64 != 0 {
				return fmt.Errorf("archive_directory_with_data")
			}
			continue
		}
		if f.UncompressedSize64 > uint64(x.limits.MaxFileBytes) || f.UncompressedSize64 > uint64(x.limits.MaxTotalBytes)-total {
			return fmt.Errorf("archive_uncompressed_limit")
		}
		if f.UncompressedSize64 > 0 && (f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > x.limits.MaxCompressionRatio) {
			return fmt.Errorf("archive_compression_ratio_limit")
		}
		total += f.UncompressedSize64
	}
	for _, f := range zr.File {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := x.writeDir(f.Name); err != nil {
				return err
			}
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		err = x.writeFile(f.Name, int64(f.UncompressedSize64), r)
		// zip.Reader verifies CRC only when the stream is consumed to EOF.
		// Reading precisely the declared size and closing would miss corruption.
		if err == nil {
			var extra [1]byte
			n, endErr := r.Read(extra[:])
			if n != 0 || endErr != io.EOF {
				err = fmt.Errorf("archive_zip_integrity_failed")
			}
		}
		closeErr := r.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (x *extractor) extractTAR(source string, gz bool) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	var reader io.Reader = f
	if gz {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer zr.Close()
		reader = zr
	}
	tr := tar.NewReader(reader)
	for {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := x.writeDir(hdr.Name); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := x.writeFile(hdr.Name, hdr.Size, tr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive_special_entry_rejected")
		}
	}
}
