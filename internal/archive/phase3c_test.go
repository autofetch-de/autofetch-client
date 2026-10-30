package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMultipartNamingAndSafeVolumePreflight(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "book.part01.rar")
	second := filepath.Join(dir, "book.part02.rar")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("fake"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !Supported(first) || Supported(second) || !Supported(filepath.Join(dir, "book.7z.001")) || Supported(filepath.Join(dir, "book.7z.002")) {
		t.Fatal("bad leader eligibility")
	}
	paths, err := collectVolumes(first)
	if err != nil || len(paths) != 2 {
		t.Fatalf("set %+v: %v", paths, err)
	}
	ctx := context.Background()
	before, err := volumeSignature(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := volumeSignature(ctx, paths)
	if err != nil || before == after {
		t.Fatalf("sibling integrity not detected %v", err)
	}
	if err := os.Rename(second, filepath.Join(dir, "book.part03.rar")); err != nil {
		t.Fatal(err)
	}
	if _, err := collectVolumes(first); err == nil || !strings.Contains(err.Error(), "volume_missing") {
		t.Fatalf("gap accepted %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "book.part03.rar")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", second); err == nil {
		if _, err := collectVolumes(first); err == nil {
			t.Fatal("symlink volume accepted")
		}
	}
}

func TestMultipartRequiresFullDecoderVerification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	first := filepath.Join(dir, "album.7z.001")
	second := filepath.Join(dir, "album.7z.002")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("fake"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	toolDir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
 t) if [ "$MULTIPART_TEST" = fail ]; then exit 2; fi; exit 0 ;;
 l) printf 'Path = inner.txt\nSize = 5\nAttributes = A -rw-r--r--\nEncrypted = -\nSplit After = +\n' ;;
 x) printf hello ;;
 *) exit 3 ;;
esac
`
	if err := os.WriteFile(filepath.Join(toolDir, "7zz"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MULTIPART_TEST", "fail")
	if _, err := Extract(context.Background(), first, Limits{}); err == nil || !strings.Contains(err.Error(), "archive_volume_incomplete_or_corrupt") {
		t.Fatalf("bad decoder verified set: %v", err)
	}
	if _, err := os.Stat(first + ".extracted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published incomplete set")
	}
	t.Setenv("MULTIPART_TEST", "")
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	out, err := ExtractVerified(context.Background(), first, hex.EncodeToString(digest[:]), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "inner.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("content %q %v", got, err)
	}
	// A changed sibling must NEVER make a completed extraction receipt reusable.
	if err := os.WriteFile(second, []byte("diff"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractVerified(context.Background(), first, hex.EncodeToString(digest[:]), Limits{}); err == nil || !strings.Contains(err.Error(), "destination_exists") {
		t.Fatalf("stale volume receipt accepted %v", err)
	}
}

func zipPayload(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	b := new(bytes.Buffer)
	z := zip.NewWriter(b)
	f, err := z.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestNestedOptInDepthAndAtomicFailure(t *testing.T) {
	inner := zipPayload(t, "bottom.txt", []byte("ok"))
	outer := zipPayload(t, "folder/inner.zip", inner)
	src := filepath.Join(t.TempDir(), "outer.zip")
	if err := os.WriteFile(src, outer, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(outer)
	hash := hex.EncodeToString(digest[:])
	output, err := ExtractVerifiedNested(context.Background(), src, hash, Limits{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, "folder", "inner.zip.extracted")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nested extraction not off by default")
	}
	if err := os.RemoveAll(output); err != nil {
		t.Fatal(err)
	}
	output, err = ExtractVerifiedNested(context.Background(), src, hash, Limits{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "folder", "inner.zip.extracted", "bottom.txt"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("nested result: %q %v", data, err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("original removed")
	}
	badInner := zipPayload(t, "../outside", []byte("bad"))
	badOuter := zipPayload(t, "nested.zip", badInner)
	badPath := filepath.Join(t.TempDir(), "nested-bad.zip")
	if err := os.WriteFile(badPath, badOuter, 0600); err != nil {
		t.Fatal(err)
	}
	badHash := sha256.Sum256(badOuter)
	if _, err := ExtractVerifiedNested(context.Background(), badPath, hex.EncodeToString(badHash[:]), Limits{}, 1); err == nil {
		t.Fatal("unsafe nested archive accepted")
	}
	if _, err := os.Stat(badPath + ".extracted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partially published nested result")
	}
}

func TestManualRetryFailedExtractionNoRedownload(t *testing.T) {
	src := zipFixture(t, "safe.zip", entry{name: "message.txt", content: "original"})
	data, _ := os.ReadFile(src)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	q := &Queue{Dir: filepath.Join(t.TempDir(), "queue")}
	if err := q.Enqueue("client", "job", src, hash); err != nil {
		t.Fatal(err)
	}
	output, err := Extract(context.Background(), src, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := q.ProcessOne(context.Background(), "client", Limits{}); !ok || err == nil {
		t.Fatalf("expected conflict %v %v", ok, err)
	}
	if ok, err := q.ProcessOne(context.Background(), "client", Limits{}); ok || err != nil {
		t.Fatalf("unexpected automatic retry: %v %v", ok, err)
	}
	if err := q.Retry("another-client", "job"); err == nil {
		t.Fatal("cross-client retry")
	}
	if err := q.Retry("client", "job"); err != nil {
		t.Fatal(err)
	}
	if err := q.Retry("client", "job"); err == nil {
		t.Fatal("double retry")
	}
	if err := os.RemoveAll(output); err != nil {
		t.Fatal(err)
	}
	if ok, err := q.ProcessOne(context.Background(), "client", Limits{}); err != nil || !ok {
		t.Fatalf("retry failed %v %v", ok, err)
	}
	if err := q.Retry("client", "job"); err == nil {
		t.Fatal("completed retry")
	}
	after, err := os.ReadFile(src)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("original download changed")
	}
}
