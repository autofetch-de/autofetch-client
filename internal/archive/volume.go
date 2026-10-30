package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Only a FIRST volume is eligible to create a queue item. Other volumes
// belong to that extraction and must never become independent jobs.
var rarFirst = regexp.MustCompile(`(?i)^(.+)\.part(0*1)\.rar$`)
var sevenFirst = regexp.MustCompile(`(?i)^(.+\.7z)\.(0*1)$`)
var rarVolume = regexp.MustCompile(`(?i)^(.+)\.part([0-9]+)\.rar$`)
var sevenVolume = regexp.MustCompile(`(?i)^(.+\.7z)\.([0-9]+)$`)

const maxVolumeFiles = 128

func volumeLeader(name string) bool {
	b := filepath.Base(name)
	return rarFirst.MatchString(b) || sevenFirst.MatchString(b)
}

// collectVolumes requires a contiguous, same-width series of regular files.
// The final count cannot be known from filenames, so the external decoder's
// full archive test is also mandatory before extraction.
func collectVolumes(source string) ([]string, error) {
	base := filepath.Base(source)
	var match []string
	switch {
	case rarFirst.MatchString(base):
		match = rarFirst.FindStringSubmatch(base)
	case sevenFirst.MatchString(base):
		match = sevenFirst.FindStringSubmatch(base)
	default:
		return []string{source}, nil
	}
	prefix, width := match[1], len(match[2])
	names, err := os.ReadDir(filepath.Dir(source))
	if err != nil {
		return nil, err
	}
	parts := map[int]string{}
	for _, e := range names {
		name := e.Name()
		var sub []string
		if strings.HasSuffix(strings.ToLower(base), ".rar") {
			sub = rarVolume.FindStringSubmatch(name)
		} else {
			sub = sevenVolume.FindStringSubmatch(name)
		}
		if sub == nil || !strings.EqualFold(sub[1], prefix) {
			continue
		}
		// Different-width volumes are ambiguous and cannot be safely merged.
		if len(sub[2]) != width {
			return nil, fmt.Errorf("archive_volume_naming_conflict")
		}
		num, er := strconv.Atoi(sub[2])
		if er != nil || num < 1 || num > maxVolumeFiles {
			return nil, fmt.Errorf("archive_volume_count_limit")
		}
		if _, exists := parts[num]; exists {
			return nil, fmt.Errorf("archive_volume_duplicate")
		}
		parts[num] = filepath.Join(filepath.Dir(source), name)
	}
	if len(parts) == 0 || len(parts) > maxVolumeFiles || filepath.Clean(parts[1]) != filepath.Clean(source) {
		return nil, fmt.Errorf("archive_volume_set_invalid")
	}
	result := make([]string, 0, len(parts))
	for i := 1; i <= len(parts); i++ {
		p, ok := parts[i]
		if !ok {
			return nil, fmt.Errorf("archive_volume_missing")
		}
		st, er := os.Lstat(p)
		if er != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			return nil, fmt.Errorf("archive_volume_invalid")
		}
		result = append(result, p)
	}
	return result, nil
}

// A digest across ALL volumes makes an earlier extraction receipt invalid if
// a sibling is changed while the leading downloaded file remains identical.
func volumeSignature(ctx context.Context, paths []string) (string, error) {
	if len(paths) == 1 {
		return "", nil
	}
	h := sha256.New()
	for i, p := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() {
			return "", fmt.Errorf("archive_volume_invalid")
		}
		fmt.Fprintf(h, "volume:%d:size:%d\x00", i, st.Size())
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, &contextReader{ctx: ctx, reader: f})
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}
