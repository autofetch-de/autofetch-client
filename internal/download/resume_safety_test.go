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

func TestHTTPResumeWithIfRangeAndMatchingContentRange(t *testing.T) {
	data := "0123456789"
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=5-" || r.Header.Get("If-Range") != "\"rev1\"" {
			t.Errorf("missing resume validators: Range=%q If-Range=%q", r.Header.Get("Range"), r.Header.Get("If-Range"))
		}
		w.Header().Set("ETag", "\"rev1\"")
		w.Header().Set("Content-Range", "bytes 5-9/10")
		w.WriteHeader(206)
		w.Write([]byte(data[5:]))
	}))
	defer server.Close()
	serverURL = server.URL
	path := filepath.Join(t.TempDir(), "video.bin")
	if err := os.WriteFile(path+".part", []byte(data[:5]), 0600); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(partMeta{URL: serverURL, ExpectedSize: 10, ETag: "\"rev1\""})
	if err := os.WriteFile(path+".part.meta.json", meta, 0600); err != nil {
		t.Fatal(err)
	}
	res, _, err := DownloadToFile(context.Background(), server.Client(), serverURL, path, 10, 0, nil)
	if err != nil || res.Bytes != 10 {
		t.Fatalf("resume: %+v %v", res, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != data {
		t.Fatalf("file content=%q", b)
	}
}

func TestHTTPBadRangeDoesNotFinalizePartial(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		header string
		body   string
	}{
		{"missing range", 206, "", "56789"},
		{"wrong offset", 206, "bytes 4-9/10", "56789"},
		{"incomplete chunk", 206, "bytes 5-7/10", "567"},
		{"range unsatisfiable", 416, "bytes */10", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Range", test.header)
				w.WriteHeader(test.status)
				w.Write([]byte(test.body))
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "target")
			os.WriteFile(path+".part", []byte("01234"), 0600)
			meta, _ := json.Marshal(partMeta{URL: server.URL, ExpectedSize: 10, ETag: "\"rev1\""})
			os.WriteFile(path+".part.meta.json", meta, 0600)
			_, _, err := DownloadToFile(context.Background(), server.Client(), server.URL, path, 10, 0, nil)
			if err == nil {
				t.Fatal("invalid resume accepted")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("invalid partial finalized")
			}
		})
	}
}

func TestHTTPServerChangesETagRestartsWithCurrentFullBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Range") != "\"old\"" {
			t.Error("missing If-Range")
		}
		w.Header().Set("ETag", "\"new\"")
		w.WriteHeader(200)
		w.Write([]byte("new-content"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "file")
	os.WriteFile(path+".part", []byte("old-p"), 0600)
	meta, _ := json.Marshal(partMeta{URL: server.URL, ExpectedSize: 11, ETag: "\"old\""})
	os.WriteFile(path+".part.meta.json", meta, 0600)
	_, _, err := DownloadToFile(context.Background(), server.Client(), server.URL, path, 11, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "new-content" {
		t.Fatal(string(b))
	}
}

func TestHTTPNoValidatorStartsFresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			t.Errorf("unsafe range used without validator: %q", r.Header.Get("Range"))
		}
		w.Write([]byte("fresh"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "file")
	os.WriteFile(path+".part", []byte("old"), 0600)
	meta, _ := json.Marshal(partMeta{URL: server.URL, ExpectedSize: 5})
	os.WriteFile(path+".part.meta.json", meta, 0600)
	_, _, err := DownloadToFile(context.Background(), server.Client(), server.URL, path, 5, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "fresh" {
		t.Fatalf("got %q", b)
	}
}

func TestHTTPShortTransferNeverFinalizes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("short")) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "file")
	_, _, err := DownloadToFile(context.Background(), server.Client(), server.URL, path, 12, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "http_size_mismatch") {
		t.Fatalf("expected mismatch, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("short download finalized")
	}
	if _, err := os.Stat(path + ".part"); err != nil {
		t.Fatal("partial unexpectedly deleted")
	}
}
