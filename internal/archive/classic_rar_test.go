package archive

import (
	"archive/tar"
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

func TestClassicRARVolumeDiscovery(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "Movie.RAR")
	write := func(name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("part"), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("Movie.RAR")
	r00 := write("Movie.r00")
	r01 := write("Movie.R01")
	write("Other.r00")
	if !Supported(first) || Supported(r00) || Supported(r01) {
		t.Fatal("only the .rar leader should be eligible")
	}
	volumes, err := collectVolumes(first)
	if err != nil || len(volumes) != 3 || volumes[0] != first || volumes[1] != r00 || volumes[2] != r01 {
		t.Fatalf("wrong ordered volumes: %v, %v", volumes, err)
	}
	before, err := volumeSignature(context.Background(), volumes)
	if err != nil || before == "" {
		t.Fatalf("missing volume signature: %v", err)
	}
	if err := os.WriteFile(r01, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := volumeSignature(context.Background(), volumes)
	if err != nil || before == after {
		t.Fatalf("changed sibling was not detected: %v", err)
	}
	if err := os.Remove(r00); err != nil {
		t.Fatal(err)
	}
	if _, err := collectVolumes(first); err == nil || !strings.Contains(err.Error(), "archive_volume_missing") {
		t.Fatalf("missing .r00 accepted: %v", err)
	}
	if err := os.Symlink("/etc/hosts", r00); err == nil {
		if _, err := collectVolumes(first); err == nil || !strings.Contains(err.Error(), "archive_volume_invalid") {
			t.Fatalf("symlink accepted: %v", err)
		}
	}
}

func TestClassicRARVolumeNamingGuards(t *testing.T) {
	t.Run("duplicate_case", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"film.rar", "film.r00"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("part"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		// On case-insensitive filesystems (common on macOS), film.R00
		// refers to film.r00. Such a filesystem cannot hold both names,
		// so the duplicate-case guard is not testable there.
		f, err := os.OpenFile(filepath.Join(dir, "film.R00"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			t.Skip("case-insensitive filesystem cannot store duplicate-case volumes")
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("part")); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := collectVolumes(filepath.Join(dir, "film.rar")); err == nil || !strings.Contains(err.Error(), "archive_volume_duplicate") {
			t.Fatalf("ambiguous volume accepted: %v", err)
		}
	})
	t.Run("next_letter", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"film.rar", "film.s00"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("part"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := collectVolumes(filepath.Join(dir, "film.rar")); err == nil || !strings.Contains(err.Error(), "archive_volume_missing") {
			t.Fatalf("skipped .r00–.r99 accepted: %v", err)
		}
	})
	t.Run("too_many", func(t *testing.T) {
		dir := t.TempDir()
		first := filepath.Join(dir, "film.rar")
		if err := os.WriteFile(first, []byte("part"), 0600); err != nil {
			t.Fatal(err)
		}
		// A classic series with a volume beyond the existing max must fail.
		if err := os.WriteFile(filepath.Join(dir, "film.s27"), []byte("part"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := collectVolumes(first); err == nil || !strings.Contains(err.Error(), "archive_volume_count_limit") {
			t.Fatalf("oversized series accepted: %v", err)
		}
	})
}

// This fixture verifies the old-style RAR set inside a TAR passes through the
// entire *nested* path, including decoder test, listing, streaming and staging.
// It uses a deterministic 7-Zip stub; real RAR data still needs an end-to-end
// test on a client machine with a real decoder.
func TestClassicRARNestedTAR(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	fakeBin := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  t)
    echo test >> "$AUTOFETCH_CLASSIC_RAR_TRACE"
    [ "$AUTOFETCH_CLASSIC_RAR_FAIL_TEST" = 1 ] && exit 2
    case "$5" in
      *.rar) test -f "${5%.rar}.r00" && test -f "${5%.rar}.r01" || exit 2 ;;
      *) exit 2 ;;
    esac
    ;;
  l)
    echo list >> "$AUTOFETCH_CLASSIC_RAR_TRACE"
    printf 'Path = movie.mkv\nSize = 5\nAttributes = A -rw-r--r--\nEncrypted = -\nSplit Before = +\nSplit After = +\n'
    ;;
  x)
    echo stream >> "$AUTOFETCH_CLASSIC_RAR_TRACE"
    printf 'hello'
    ;;
  *) exit 3 ;;
esac
`
	if err := os.WriteFile(filepath.Join(fakeBin, "7zz"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	trace := filepath.Join(t.TempDir(), "trace.txt")
	t.Setenv("AUTOFETCH_CLASSIC_RAR_TRACE", trace)

	writeTAR := func(files []string) (string, string) {
		t.Helper()
		archivePath := filepath.Join(t.TempDir(), "bundle.tar")
		var buf bytes.Buffer
		w := tar.NewWriter(&buf)
		for _, name := range files {
			b := []byte("fragment")
			if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(b); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archivePath, buf.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(buf.Bytes())
		return archivePath, hex.EncodeToString(sum[:])
	}
	path, digest := writeTAR([]string{"Bundle/movie.rar", "Bundle/movie.r00", "Bundle/movie.r01"})
	out, err := ExtractVerifiedNested(context.Background(), path, digest, Limits{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(out, "Bundle", "movie.rar.extracted", "movie.mkv")
	b, err := os.ReadFile(result)
	if err != nil || string(b) != "hello" {
		t.Fatalf("unexpected extracted data: %q, %v", b, err)
	}
	st, err := os.Stat(result)
	if err != nil || st.Mode().Perm() != 0644 {
		t.Fatalf("wrong published permissions: %v, %v", st, err)
	}
	traceContents, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(traceContents), "test\n") || !strings.Contains(string(traceContents), "list\n") || !strings.Contains(string(traceContents), "stream\n") {
		t.Fatalf("decoder validation skipped: %q, %v", traceContents, err)
	}

	// With .r00 absent, the outer TAR extraction must fail closed and never
	// publish an incomplete nested result.
	badPath, badDigest := writeTAR([]string{"Bundle/movie.rar", "Bundle/movie.r01"})
	if _, err := ExtractVerifiedNested(context.Background(), badPath, badDigest, Limits{}, 1); err == nil || !strings.Contains(err.Error(), "archive_volume_missing") || !strings.Contains(err.Error(), "Bundle/movie.rar") {
		t.Fatalf("incomplete nested series accepted or error not attributed: %v", err)
	}
	if _, err := os.Stat(badPath + ".extracted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete nested tree was published")
	}

	// A contiguous set is not proof of completeness. Decoder verification
	// must still pass before any extracted files become visible.
	t.Setenv("AUTOFETCH_CLASSIC_RAR_FAIL_TEST", "1")
	corruptPath, corruptDigest := writeTAR([]string{"Bundle/movie.rar", "Bundle/movie.r00", "Bundle/movie.r01"})
	if _, err := ExtractVerifiedNested(context.Background(), corruptPath, corruptDigest, Limits{}, 1); err == nil || !strings.Contains(err.Error(), "archive_volume_incomplete_or_corrupt") {
		t.Fatalf("decoder test failure ignored: %v", err)
	}
	if _, err := os.Stat(corruptPath + ".extracted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("corrupt nested tree was published")
	}
}
