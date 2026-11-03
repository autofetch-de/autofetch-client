package download

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPWrongJobExpectedSizeAcceptsComplete200(t *testing.T) {
	content := []byte("the entire actual media body")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"rev1"`)
		w.Write(content)
	}))
	defer srv.Close()
	target := filepath.Join(t.TempDir(), "episode.mp4")
	result, progress, err := DownloadToFile(context.Background(), srv.Client(), srv.URL, target, int64(len(content)+99), 0, nil)
	if err != nil || result == nil || result.Bytes != int64(len(content)) {
		t.Fatalf("complete HTTP 200 failed: result=%+v err=%v", result, err)
	}
	if progress.Expected != int64(len(content)) {
		t.Fatalf("expected progress total=%d, got %d", len(content), progress.Expected)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(content) {
		t.Fatalf("published bytes=%q err=%v", got, err)
	}
	if _, err := os.Stat(target + ".part.meta.json"); !os.IsNotExist(err) {
		t.Fatalf("stale metadata: %v", err)
	}
}

func TestHTTP416AtRemoteEOFRecoversStoredFileWithoutReDownload(t *testing.T) {
	content := []byte("complete-video-body")
	const etag = `"rev1"`
	var fullGETs, headGETs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		switch {
		case r.Method == http.MethodHead:
			headGETs++
			w.Header().Set("Content-Length", "19")
			w.WriteHeader(http.StatusOK)
		case r.Header.Get("Range") == "bytes=19-":
			w.Header().Set("Content-Range", "bytes */19")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		default:
			fullGETs++
			t.Errorf("unexpected transfer request Range=%s", r.Header.Get("Range"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	target := filepath.Join(t.TempDir(), "episode.mp4")
	if err := os.WriteFile(target+".part", content, 0o644); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(partMeta{URL: srv.URL, ExpectedSize: 24, ETag: etag})
	if err := os.WriteFile(target+".part.meta.json", metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	result, progress, err := DownloadToFile(context.Background(), srv.Client(), srv.URL, target, 24, 0, nil)
	if err != nil || result == nil || result.Bytes != int64(len(content)) {
		t.Fatalf("recovery: %+v %v", result, err)
	}
	if headGETs != 1 || fullGETs != 0 || progress.Expected != int64(len(content)) {
		t.Fatalf("head=%d full=%d progress=%d", headGETs, fullGETs, progress.Expected)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(content) {
		t.Fatalf("recovered bytes=%q err=%v", got, err)
	}
}

func TestHTTP416DoesNotFinalizeIfRemoteValidatorChanged(t *testing.T) {
	content := []byte("complete-video-body")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("ETag", `"changed"`)
			w.Header().Set("Content-Length", "19")
			return
		}
		if r.Header.Get("Range") == "bytes=19-" {
			w.Header().Set("Content-Range", "bytes */19")
			w.WriteHeader(416)
			return
		}
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("ETag", `"changed"`)
			w.Header().Set("Content-Range", "bytes 0-0/19")
			w.WriteHeader(206)
			w.Write([]byte("c"))
			return
		}
		t.Errorf("unexpected request %v", r)
	}))
	defer srv.Close()
	target := filepath.Join(t.TempDir(), "episode.mp4")
	os.WriteFile(target+".part", content, 0o644)
	metadata, _ := json.Marshal(partMeta{URL: srv.URL, ExpectedSize: 24, ETag: `"old"`})
	os.WriteFile(target+".part.meta.json", metadata, 0o600)
	result, _, err := DownloadToFile(context.Background(), srv.Client(), srv.URL, target, 24, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "http_416_unverified_partial") || result != nil {
		t.Fatalf("should reject: result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("unexpected published file: %v", err)
	}
	if _, err := os.Stat(target + ".part"); err != nil {
		t.Fatalf("lost user data: %v", err)
	}
}

func TestHTTP416RangeProbeFallbackRecoversAtEOF(t *testing.T) {
	content := []byte("complete-video-body")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("ETag", `"rev1"`)
		if r.Header.Get("Range") == "bytes=19-" {
			w.WriteHeader(416)
			return
		}
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/19")
			w.WriteHeader(206)
			w.Write([]byte("c"))
			return
		}
		t.Errorf("unexpected HTTP request")
	}))
	defer srv.Close()
	target := filepath.Join(t.TempDir(), "episode.mp4")
	os.WriteFile(target+".part", content, 0o644)
	metadata, _ := json.Marshal(partMeta{URL: srv.URL, ExpectedSize: 24, ETag: `"rev1"`})
	os.WriteFile(target+".part.meta.json", metadata, 0o600)
	result, _, err := DownloadToFile(context.Background(), srv.Client(), srv.URL, target, 24, 0, nil)
	if err != nil || result == nil {
		t.Fatalf("expected safe recovery %+v %v", result, err)
	}
}

func TestHTTP416NoValidatorDoesNotPublish(t *testing.T) {
	// A 416 without a trusted saved validator is never proof of success.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer srv.Close()
	target := filepath.Join(t.TempDir(), "episode.mp4")
	_, _, err := DownloadToFile(context.Background(), srv.Client(), srv.URL, target, 24, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "http_416_unverified_partial") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestHTTPZeroLengthDoesNotSatisfyNonEmptyJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "video.mp4")
	_, _, err := DownloadToFile(context.Background(), srv.Client(), srv.URL, dest, 100, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "http_zero_length_response") {
		t.Fatalf("empty response accepted: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("zero-length file published: %v", err)
	}
}
