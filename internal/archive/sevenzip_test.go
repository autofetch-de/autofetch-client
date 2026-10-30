package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sampleSevenZipList = "Path = folder\nSize = 0\nAttributes = D drwxr-xr-x\nEncrypted = -\n\nPath = folder/test.txt\nSize = 5\nAttributes = A -rw-r--r--\nEncrypted = -\n"

func TestSevenZipListingPreflight(t *testing.T) {
	entries, err := parseSevenZipList([]byte(sampleSevenZipList), Limits{}, false)
	if err != nil || len(entries) != 2 || !entries[0].dir || entries[1].size != 5 || entries[1].name != "folder/test.txt" {
		t.Fatalf("valid listing rejected: %+v %v", entries, err)
	}
	// Real 7-Zip RAR5 technical listings include blank metadata fields.
	// 7-Zip versions differ in whether a trailing space follows '='.
	rarListing := "Path = book.epub\nFolder = -\nSize = 5\nPacked Size = 4\nAttributes =\nCreated =\nAccessed = \nEncrypted = -\nSymbolic Link =\nHard Link =\nCopy Link =\nAlternate Stream = -\nSplit Before = -\nSplit After = -\n"
	rarEntries, rarErr := parseSevenZipList([]byte(rarListing), Limits{}, false)
	if rarErr != nil || len(rarEntries) != 1 || rarEntries[0].name != "book.epub" || rarEntries[0].size != 5 {
		t.Fatalf("RAR listing with empty fields rejected: %+v %v", rarEntries, rarErr)
	}
	// A present, but non-empty link/split/stream marker must still be refused.
	for _, key := range []string{"Copy Link", "Alternate Stream", "Split Before", "Split After"} {
		listing := strings.Replace(rarListing, key+" =", key+" = +", 1)
		if _, err := parseSevenZipList([]byte(listing), Limits{}, false); err == nil {
			t.Fatalf("unsafe RAR marker accepted: %s", key)
		}
	}
	bad := []struct{ name, data string }{
		{"traversal", strings.ReplaceAll(sampleSevenZipList, "folder/test.txt", "../escape")},
		{"device", strings.ReplaceAll(sampleSevenZipList, "folder/test.txt", "NUL.txt")},
		{"command_like", strings.ReplaceAll(sampleSevenZipList, "folder/test.txt", "-o/tmp")},
		{"response_file", strings.ReplaceAll(sampleSevenZipList, "folder/test.txt", "@response")},
		{"encrypted", strings.ReplaceAll(sampleSevenZipList, "Encrypted = -", "Encrypted = +")},
		{"link", strings.ReplaceAll(sampleSevenZipList, "Attributes = A -rw-r--r--", "Attributes = A lrwxrwxrwx")},
		{"hard_link", sampleSevenZipList + "Hard Link = outside\n"},
		{"missing_size", strings.ReplaceAll(sampleSevenZipList, "Size = 5\n", "")},
		{"negative_size", strings.ReplaceAll(sampleSevenZipList, "Size = 5", "Size = -1")},
		{"newline_in_name", strings.ReplaceAll(sampleSevenZipList, "folder/test.txt", "folder/unsafe\nname")},
		{"bad_utf8", sampleSevenZipList + "\xff"},
		{"malformed", "unexpected heading"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseSevenZipList([]byte(tc.data), Limits{}, false); err == nil {
				t.Fatalf("unsafe listing was accepted: %q", tc.data)
			}
		})
	}
	if _, err := parseSevenZipList([]byte(sampleSevenZipList), Limits{MaxFileBytes: 4}, false); err == nil {
		t.Fatal("per-file limit not enforced")
	}
	if _, err := parseSevenZipList([]byte(sampleSevenZipList), Limits{MaxEntries: 1}, false); err == nil {
		t.Fatal("entry count not enforced")
	}
	if _, err := parseSevenZipList([]byte(strings.Repeat(" ", maxSevenZipListing+1)), Limits{}, false); err == nil {
		t.Fatal("listing length limit not enforced")
	}
}

func TestSevenZipDecoderStreamingAndRejection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test stub uses /bin/sh")
	}
	temp := t.TempDir()
	script := filepath.Join(temp, "7zz")
	// The fake executable records the public CLI contract but never handles
	// real RAR/7z bytes. Real decoder integration requires 7zz installed.
	content := `#!/bin/sh
case "$1" in
  l)
    if [ "$AUTOFETCH_TEST_7ZIP_LIST_MODE" = traversal ]; then
      printf 'Path = ../outside.txt\nSize = 5\nAttributes = A -rw-r--r--\nEncrypted = -\n'
    elif [ "$AUTOFETCH_TEST_7ZIP_LIST_MODE" = duplicate ]; then
      printf 'Path = folder/test.txt\nSize = 5\nAttributes = A -rw-r--r--\nEncrypted = -\n\nPath = folder/test.txt\nSize = 5\nAttributes = A -rw-r--r--\nEncrypted = -\n'
    else
      printf 'Path = folder/test.txt\nSize = 5\nAttributes = A -rw-r--r--\nEncrypted = -\n'
    fi ;;
  t) exit 0 ;;
  x)
    case "$AUTOFETCH_TEST_7ZIP_OUTPUT" in
      longer) printf 'hello EXTRA' ;;
      shorter) printf 'hey' ;;
      exitfail) printf 'hello'; exit 2 ;;
      *) printf 'hello' ;;
    esac ;;
  *) exit 3 ;;
esac
`
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", temp+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, ext := range []string{".rar", ".7z"} {
		t.Run(ext, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "fixture"+ext)
			if err := os.WriteFile(src, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			dest, err := Extract(context.Background(), src, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(dest, "folder", "test.txt"))
			if err != nil || string(b) != "hello" {
				t.Fatalf("got %q, %v", b, err)
			}
			if _, err := os.Stat(src); err != nil {
				t.Fatal("original missing")
			}
			if _, err := Extract(context.Background(), src, Limits{}); err == nil {
				t.Fatal("overwrite accepted")
			}
		})
	}
	for _, mode := range []string{"shorter", "longer", "exitfail"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AUTOFETCH_TEST_7ZIP_OUTPUT", mode)
			src := filepath.Join(t.TempDir(), "bad.7z")
			if err := os.WriteFile(src, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Extract(context.Background(), src, Limits{}); err == nil {
				t.Fatal("truncated/oversized stream accepted")
			}
			if _, err := os.Stat(src + ".extracted"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial result published: %v", err)
			}
		})
	}
	for _, mode := range []string{"traversal", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AUTOFETCH_TEST_7ZIP_LIST_MODE", mode)
			src := filepath.Join(t.TempDir(), "unsafe.7z")
			if err := os.WriteFile(src, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Extract(context.Background(), src, Limits{}); err == nil {
				t.Fatal("unsafe/duplicate archive listing accepted")
			}
			if _, err := os.Stat(src + ".extracted"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial result published: %v", err)
			}
		})
	}
}

func TestSevenZipMissingToolFailsClosed(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	src := filepath.Join(t.TempDir(), "missing.rar")
	if err := os.WriteFile(src, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(context.Background(), src, Limits{}); err == nil || !strings.Contains(err.Error(), "archive_7zip_missing") {
		t.Fatalf("missing tool not reported: %v", err)
	}
	if _, err := os.Stat(src + ".extracted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial result published: %v", err)
	}
}

func TestUnsupportedMultipartVolumesNotAutoQueued(t *testing.T) {
	for _, name := range []string{"release.part02.rar", "release.part2.RAR", "release.7z.002", "release.r00"} {
		if Supported(name) {
			t.Fatalf("multipart volume unexpectedly queued: %s", name)
		}
	}
	for _, name := range []string{"release.part01.rar", "release.7z.001", "release.rar", "release.7z", "release.zip", "release.tar.gz"} {
		if !Supported(name) {
			t.Fatalf("supported archive not recognized: %s", name)
		}
	}
}
