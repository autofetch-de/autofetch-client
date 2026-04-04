package download

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
			if json.Unmarshal(b, &m) == nil && strings.TrimSpace(m.URL) == url {
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

	// Write meta (or refresh)
	if b, err := json.Marshal(meta); err == nil {
		_ = os.WriteFile(metaPath, b, 0o644)
	}

	// Build request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	if resumeOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeOffset))
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	// Capture validators if present
	if et := resp.Header.Get("ETag"); et != "" && meta.ETag == "" {
		meta.ETag = et
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" && meta.LastModified == "" {
		meta.LastModified = lm
	}
	if b, err := json.Marshal(meta); err == nil {
		_ = os.WriteFile(metaPath, b, 0o644)
	}

	if resp.StatusCode == 404 {
		return nil, nil, fmt.Errorf("http_404")
	}

	// Resume logic:
	// - 206: append ok (but verify Content-Range begins where we expect)
	// - 200 when we asked for Range => server ignored Range, restart
	// - 416 => probably already complete; finalize
	if resumeOffset > 0 && resp.StatusCode == 200 {
		_ = os.Remove(partPath)
		_ = os.Remove(metaPath)
		return DownloadToFile(ctx, httpClient, url, destPath, expectedSize, bytesPerSec, prog)
	}

	if resumeOffset > 0 && resp.StatusCode == 206 {
		// Verify Content-Range: "bytes <start>-<end>/<total>"
		cr := resp.Header.Get("Content-Range")
		// Minimal check: must contain "bytes <resumeOffset>-"
		want := fmt.Sprintf("bytes %d-", resumeOffset)
		if cr != "" && !strings.HasPrefix(strings.ToLower(cr), strings.ToLower(want)) {
			// Server responded with a different range than requested -> restart safely.
			_ = os.Remove(partPath)
			_ = os.Remove(metaPath)
			return DownloadToFile(ctx, httpClient, url, destPath, expectedSize, bytesPerSec, prog)
		}
	}

	if resp.StatusCode == 416 && resumeOffset > 0 {
		// Treat as complete-ish: rename to final
		if err := os.Rename(partPath, destPath); err != nil {
			return nil, nil, err
		}
		_ = os.Remove(metaPath)

		st, _ := os.Stat(destPath)
		var sz int64
		if st != nil {
			sz = st.Size()
		}
		prog := NewProgress(expectedSize)
		prog.Downloaded.Store(sz)
		return &Result{FilePath: destPath, Bytes: sz}, prog, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("http_%d", resp.StatusCode)
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
	if err := f.Close(); err != nil {
		return nil, prog, err
	}

	// Finalize
	if err := os.Rename(partPath, destPath); err != nil {
		return nil, prog, err
	}
	_ = os.Remove(metaPath)

	total := resumeOffset + n
	return &Result{FilePath: destPath, Bytes: total}, prog, nil
}
