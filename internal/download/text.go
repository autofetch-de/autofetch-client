package download

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// DownloadText downloads a (usually small) subtitle file to a final path atomically.
func DownloadText(ctx context.Context, httpClient *http.Client, url, destPath string) error {
	if url == "" {
		return fmt.Errorf("empty_url")
	}
	if destPath == "" {
		return fmt.Errorf("empty_dest_path")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}

	partPath := destPath + ".part"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http_%d", resp.StatusCode)
	}

	f, err := os.Create(partPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(partPath)
		}
	}()

	if _, err = io.Copy(f, resp.Body); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(partPath, destPath)
}
