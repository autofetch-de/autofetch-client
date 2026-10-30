package download

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Result struct {
	FilePath string
	Bytes    int64
}

type partMeta struct {
	URL          string `json:"url"`
	ExpectedSize int64  `json:"expected_size,omitempty"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

func DownloadToFile(ctx context.Context, httpClient *http.Client, url, destPath string, expectedSize int64, bytesPerSec int64, prog *Progress) (*Result, *Progress, error) {
	url = strings.TrimSpace(url)
	destPath = strings.TrimSpace(destPath)

	if url == "" {
		return nil, nil, fmt.Errorf("empty_url")
	}
	if destPath == "" {
		return nil, nil, fmt.Errorf("empty_dest_path")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return nil, nil, err
	}

	partPath := destPath + ".part"
	metaPath := destPath + ".part.meta.json"

	meta := &partMeta{
		URL:          url,
		ExpectedSize: expectedSize,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	var resumeOffset int64

	// Detect existing partial and validate meta
	if st, err := os.Stat(partPath); err == nil && st.Mode().IsRegular() {
		if b, err := os.ReadFile(metaPath); err == nil {
			var m partMeta
			if json.Unmarshal(b, &m) == nil && strings.TrimSpace(m.URL) == url && m.ExpectedSize == expectedSize {
				resumeOffset = st.Size()
				// keep meta fields if present
				if m.ETag != "" {
					meta.ETag = m.ETag
				}
				if m.LastModified != "" {
					meta.LastModified = m.LastModified
				}
				if m.CreatedAt != "" {
					meta.CreatedAt = m.CreatedAt
				}
			} else {
				// meta invalid or mismatched => discard
				_ = os.Remove(partPath)
				_ = os.Remove(metaPath)
				resumeOffset = 0
			}
		} else {
			// No meta => safer to discard (prevents resuming wrong content)
			_ = os.Remove(partPath)
			_ = os.Remove(metaPath)
			resumeOffset = 0
		}
	}

	// A range response is not safe to append without a validator for the
	// original representation. Legacy .part files without validators are
	// restarted rather than silently mixing different remote objects.
	if resumeOffset > 0 && (meta.ETag == "" || strings.HasPrefix(meta.ETag, "W/")) && meta.LastModified == "" {
		resumeOffset = 0
		meta.ETag = ""
		meta.LastModified = ""
	}

	// Write meta (or refresh)
	if b, err := json.Marshal(meta); err == nil {
		_ = os.WriteFile(metaPath, b, 0o600)
	}

	// Build request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	if resumeOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeOffset))
		if meta.ETag != "" && !strings.HasPrefix(meta.ETag, "W/") {
			req.Header.Set("If-Range", meta.ETag)
		} else if meta.LastModified != "" {
			req.Header.Set("If-Range", meta.LastModified)
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, nil, fmt.Errorf("http_404")
	}
	if resp.StatusCode == 416 {
		// 416 says only that a range cannot be served; it is NOT evidence
		// that the existing partial contains the complete expected file.
		return nil, nil, fmt.Errorf("http_416_unverified_partial")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("http_%d", resp.StatusCode)
	}

	if resumeOffset > 0 && resp.StatusCode == 200 {
		// The server ignored Range or If-Range detected a changed object.
		// Use this fresh 200 response rather than recursively requesting.
		resumeOffset = 0
		meta.ETag = ""
		meta.LastModified = ""
	}
	var rangeEnd, rangeTotal int64
	if resp.StatusCode == 206 {
		if resumeOffset == 0 {
			return nil, nil, fmt.Errorf("http_unexpected_partial_response")
		}
		rangeEnd, rangeTotal, err = validateContentRange(resp.Header.Get("Content-Range"), resumeOffset, expectedSize)
		if err != nil {
			return nil, nil, err
		}
	}
	if et := resp.Header.Get("ETag"); et != "" {
		meta.ETag = et
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		meta.LastModified = lm
	}
	if b, err := json.Marshal(meta); err == nil {
		_ = os.WriteFile(metaPath, b, 0600)
	}

	// Open .part for write/append
	var f *os.File
	if resumeOffset > 0 && resp.StatusCode == 206 {
		f, err = os.OpenFile(partPath, os.O_WRONLY|os.O_APPEND, 0o644)
	} else {
		// fresh download
		f, err = os.Create(partPath)
		resumeOffset = 0
	}
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()

	if prog == nil {
		prog = NewProgress(expectedSize)
	} else {
		prog.Expected = expectedSize
		if prog.Start.IsZero() {
			prog.Start = time.Now()
		}
		prog.Downloaded.Store(0)
	}
	if resumeOffset > 0 {
		prog.Downloaded.Store(resumeOffset)
	}

	reader := TeeReader(NewRateLimitedReader(resp.Body, bytesPerSec), prog)
	n, err := io.Copy(f, reader)
	if err != nil {
		return nil, prog, err
	}
	if err := f.Sync(); err != nil {
		return nil, prog, err
	}
	if err := f.Close(); err != nil {
		return nil, prog, err
	}

	total := resumeOffset + n
	if resp.StatusCode == 206 && (total != rangeTotal || total != rangeEnd+1) {
		return nil, prog, fmt.Errorf("http_incomplete_partial_response")
	}
	if expectedSize > 0 && total != expectedSize {
		return nil, prog, fmt.Errorf("http_size_mismatch: received=%d expected=%d", total, expectedSize)
	}
	// Finalize only a complete transfer.
	if err := os.Rename(partPath, destPath); err != nil {
		return nil, prog, err
	}
	_ = os.Remove(metaPath)

	return &Result{FilePath: destPath, Bytes: total}, prog, nil
}

var contentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)

func validateContentRange(value string, offset, expectedSize int64) (int64, int64, error) {
	parts := contentRangePattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(parts) != 4 {
		return 0, 0, fmt.Errorf("http_invalid_content_range")
	}
	start, err1 := strconv.ParseInt(parts[1], 10, 64)
	end, err2 := strconv.ParseInt(parts[2], 10, 64)
	total, err3 := strconv.ParseInt(parts[3], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || start != offset || end < start || end >= total || (expectedSize > 0 && total != expectedSize) {
		return 0, 0, fmt.Errorf("http_invalid_content_range")
	}
	return end, total, nil
}
