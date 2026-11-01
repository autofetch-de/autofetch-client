package archive

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func assertArchiveMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != want {
		t.Fatalf("%s: got %04o, want %04o", path, st.Mode().Perm(), want)
	}
}

func TestExtractedArchiveReadableButMetadataPrivate(t *testing.T) {
	src := zipFixture(t, "example.zip",
		entry{name: "folder/film.mkv", content: "movie"},
		entry{name: "folder/Extras/subtitle.srt", content: "subtitle"})
	original, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := ExtractVerified(context.Background(), src, testDigest(t, src), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{dest, filepath.Join(dest, "folder"), filepath.Join(dest, "folder/Extras")} {
		assertArchiveMode(t, dir, 0755)
	}
	for _, f := range []string{"folder/film.mkv", "folder/Extras/subtitle.srt"} {
		assertArchiveMode(t, filepath.Join(dest, f), 0644)
	}
	assertArchiveMode(t, filepath.Join(dest, markerName), 0600)
	after, err := os.Stat(src)
	if err != nil || after.Mode().Perm() != original.Mode().Perm() {
		t.Fatalf("original archive permissions changed: %v", err)
	}
}

func TestPublishSameDirectoryFileAndTreeModes(t *testing.T) {
	t.Run("single file", func(t *testing.T) {
		src := zipFixture(t, "single.zip", entry{name: "nested/film.mkv", content: "movie"})
		extracted, err := ExtractVerified(context.Background(), src, testDigest(t, src), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		dest, err := PublishSameDirectory(context.Background(), src, extracted)
		if err != nil {
			t.Fatal(err)
		}
		assertArchiveMode(t, dest, 0644)
		assertArchiveMode(t, filepath.Join(extracted, markerName), 0600)
	})
	t.Run("several files", func(t *testing.T) {
		src := zipFixture(t, "multiple.zip", entry{name: "nested/film.mkv", content: "movie"}, entry{name: "nested/subdir/sub.srt", content: "subtitle"})
		extracted, err := ExtractVerified(context.Background(), src, testDigest(t, src), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		dest, err := PublishSameDirectory(context.Background(), src, extracted)
		if err != nil {
			t.Fatal(err)
		}
		assertArchiveMode(t, dest, 0755)
		assertArchiveMode(t, filepath.Join(dest, "subdir"), 0755)
		assertArchiveMode(t, filepath.Join(dest, "film.mkv"), 0644)
		assertArchiveMode(t, filepath.Join(dest, "subdir/sub.srt"), 0644)
		assertArchiveMode(t, filepath.Join(extracted, markerName), 0600)
	})
}

func TestResumedArchivePublishLeavesUnrelatedFilesPrivate(t *testing.T) {
	src := zipFixture(t, "resume.zip",
		entry{name: "one.txt", content: "one"},
		entry{name: "two.txt", content: "two"})
	extracted, err := ExtractVerified(context.Background(), src, testDigest(t, src), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := loadOrCreatePublishPlan(src, extracted)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(filepath.Dir(src), plan.Target)
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	intent, err := os.ReadFile(filepath.Join(extracted, publishIntent))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, publishInProgress), intent, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(extracted, "one.txt"), filepath.Join(dest, "one.txt")); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(dest, "personal-private.txt")
	if err := os.WriteFile(extra, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishSameDirectory(context.Background(), src, extracted); err != nil {
		t.Fatal(err)
	}
	assertArchiveMode(t, dest, 0755)
	assertArchiveMode(t, filepath.Join(dest, "one.txt"), 0644)
	assertArchiveMode(t, filepath.Join(dest, "two.txt"), 0644)
	assertArchiveMode(t, extra, 0600)
}
