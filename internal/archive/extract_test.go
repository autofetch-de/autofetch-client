package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name, content string
	mode          os.FileMode
}

func zipFixture(t *testing.T, filename string, entries ...entry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), filename)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}
func tarFixture(t *testing.T, filename string, entries ...entry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), filename)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, e := range entries {
		typ := byte(tar.TypeReg)
		if e.mode&os.ModeSymlink != 0 {
			typ = tar.TypeSymlink
		}
		hdr := &tar.Header{Name: e.name, Size: int64(len(e.content)), Mode: 0644, Typeflag: typ}
		if typ == tar.TypeSymlink {
			hdr.Size = 0
			hdr.Linkname = "../../outside"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestZIPAndTARExtractAtomicAndKeepOriginal(t *testing.T) {
	for _, ext := range []string{"files.zip", "files.tar"} {
		t.Run(ext, func(t *testing.T) {
			var source string
			if strings.HasSuffix(ext, "zip") {
				source = zipFixture(t, ext, entry{name: "folder/hello.txt", content: "hello"})
			} else {
				source = tarFixture(t, ext, entry{name: "folder/hello.txt", content: "hello"})
			}
			dest, err := Extract(context.Background(), source, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(dest, "folder", "hello.txt"))
			if err != nil || string(b) != "hello" {
				t.Fatalf("extracted %q: %v", b, err)
			}
			if _, err := os.Stat(source); err != nil {
				t.Fatal("archive disappeared", err)
			}
			if _, err := Extract(context.Background(), source, Limits{}); err == nil || !strings.Contains(err.Error(), "destination_exists") {
				t.Fatalf("dest overwrite allowed: %v", err)
			}
		})
	}
}
func TestUnsafePathsAndSpecialFilesAreRejected(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "dir/../../escape", "a\\b", "folder/C:evil", "NUL.txt", "a//b", "foo/../bar"} {
		t.Run(name, func(t *testing.T) {
			src := zipFixture(t, "bad.zip", entry{name: name, content: "bad"})
			if _, err := Extract(context.Background(), src, Limits{}); err == nil {
				t.Fatal("unsafe path extracted")
			}
			if _, err := os.Stat(src + ".extracted"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected output: %v", err)
			}
		})
	}
	src := zipFixture(t, "link.zip", entry{name: "link", content: "../../escape", mode: os.ModeSymlink | 0777})
	if _, err := Extract(context.Background(), src, Limits{}); err == nil {
		t.Fatal("symlink accepted")
	}
	tarSrc := tarFixture(t, "link.tar", entry{name: "link", mode: os.ModeSymlink})
	if _, err := Extract(context.Background(), tarSrc, Limits{}); err == nil {
		t.Fatal("tar symlink accepted")
	}
}
func TestCompressionLimitsFileCountAndDuplicateNames(t *testing.T) {
	src := zipFixture(t, "limits.zip", entry{name: "a", content: strings.Repeat("a", 200)}, entry{name: "b", content: "b"})
	tests := []Limits{{MaxFileBytes: 100}, {MaxTotalBytes: 100}, {MaxEntries: 1}, {MaxCompressionRatio: 1}}
	for _, limits := range tests {
		if _, err := Extract(context.Background(), src, limits); err == nil {
			t.Fatalf("limits %+v ignored", limits)
		}
	}
	for _, entries := range [][]entry{
		{{name: "a.txt", content: "1"}, {name: "a.txt", content: "2"}},
		{{name: "A.txt", content: "1"}, {name: "a.txt", content: "2"}},
		{{name: "folder", content: "1"}, {name: "folder/child", content: "2"}},
	} {
		p := zipFixture(t, "dupe.zip", entries...)
		if _, err := Extract(context.Background(), p, Limits{}); err == nil {
			t.Fatalf("duplicate accepted %+v", entries)
		}
	}
}
func TestCanceledExtractionLeavesNoPublishedDirectory(t *testing.T) {
	src := zipFixture(t, "cancel.zip", entry{name: "data", content: "abc"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Extract(ctx, src, Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation got %v", err)
	}
	if _, err := os.Stat(src + ".extracted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial output published")
	}
}
func TestGzipTAR(t *testing.T) {
	src := filepath.Join(t.TempDir(), "content.tgz")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	// Avoid depending on external tar binaries for cross-platform tests.
	b := new(bytes.Buffer)
	tw := tar.NewWriter(b)
	payload := []byte("hello gzip")
	if err := tw.WriteHeader(&tar.Header{Name: "test.txt", Size: int64(len(payload)), Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write(b.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	dest, err := Extract(context.Background(), src, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	extracted, err := os.ReadFile(filepath.Join(dest, "test.txt"))
	if err != nil || string(extracted) != "hello gzip" {
		t.Fatalf("got %q %v", extracted, err)
	}
}

func TestCorruptedZIPCRCMustNotBePublished(t *testing.T) {
	src := filepath.Join(t.TempDir(), "crc.zip")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "data.txt", Method: zip.Store}
	entryWriter, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entryWriter.Write([]byte("known-good-bytes"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(content, []byte("known-good-bytes"))
	if i < 0 {
		t.Fatal("missing uncompressed payload")
	}
	content[i] ^= 0x01
	if err := os.WriteFile(src, content, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(context.Background(), src, Limits{}); err == nil {
		t.Fatal("CRC corruption accepted")
	}
	if _, err := os.Stat(src + ".extracted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("corrupt output published")
	}
}

func TestArchiveCrashAfterPublishRecoveredFromMarker(t *testing.T) {
	src := zipFixture(t, "restart.zip", entry{name: "good.txt", content: "safe"})
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	hash := hex.EncodeToString(h[:])
	dest, err := ExtractVerified(context.Background(), src, hash, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	q := &Queue{Dir: filepath.Join(t.TempDir(), "queue")}
	if err := q.Enqueue("client", "job", src, hash); err != nil {
		t.Fatal(err)
	}
	p, _ := q.itemPath("client", "job")
	it, _, _ := q.load(p)
	it.Status = "extracting"
	if err := q.save(p, it); err != nil {
		t.Fatal(err)
	}
	done, err := q.ProcessOne(context.Background(), "client", Limits{})
	if err != nil || !done {
		t.Fatalf("marker recovery failed: %v %v", done, err)
	}
	items, err := q.History("client", 10)
	if err != nil || len(items) != 1 || items[0].Status != "completed" || items[0].Destination != dest {
		t.Fatalf("unexpected %+v %v", items, err)
	}
}
