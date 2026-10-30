package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDigest(t *testing.T, src string) string {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

func TestPublishSameDirectoryAndDeleteOriginal(t *testing.T) {
	src := zipFixture(t, "film.zip", entry{name: "film.mkv", content: "video"}, entry{name: "extras/readme.txt", content: "readme"})
	q := &Queue{Dir: filepath.Join(t.TempDir(), "queue"), BeforeRemove: func(clientID, jobID, source, digest, destination string) error {
		if clientID != "c" || jobID != "j" || source != src || digest != testDigest(t, src) || destination != filepath.Join(filepath.Dir(src), "film") {
			t.Fatal("cleanup identity")
		}
		if _, err := os.Stat(filepath.Join(destination, "film.mkv")); err != nil {
			t.Fatal("published output missing before cleanup", err)
		}
		return nil
	}}
	if err := q.Enqueue("c", "j", src, testDigest(t, src)); err != nil {
		t.Fatal(err)
	}
	done, err := q.ProcessOnePolicy(context.Background(), "c", Limits{}, 0, Policy{Layout: "same", DeleteOriginal: true})
	if !done || err != nil {
		t.Fatalf("process %v %v", done, err)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(src), "film", "film.mkv"))
	if err != nil || string(b) != "video" {
		t.Fatalf("output %q %v", b, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source not removed")
	}
	if _, err := os.Stat(src + ".extracted"); !os.IsNotExist(err) {
		t.Fatal("staging still visible")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(src), markerName)); !os.IsNotExist(err) {
		t.Fatal("extraneous JSON marker")
	}
	items, err := q.History("c", 10)
	if err != nil || len(items) != 1 || !items[0].OriginalDeleted || items[0].CleanupPending {
		t.Fatalf("history %#v %v", items, err)
	}
}

func TestFlatCollisionKeepsArchiveAndOtherFiles(t *testing.T) {
	src := zipFixture(t, "collision.zip", entry{name: "existing.txt", content: "archive"})
	original := filepath.Join(filepath.Dir(src), "existing.txt")
	if err := os.WriteFile(original, []byte("user data"), 0600); err != nil {
		t.Fatal(err)
	}
	q := &Queue{Dir: filepath.Join(t.TempDir(), "q"), BeforeRemove: func(string, string, string, string, string) error { t.Fatal("cleanup should not run"); return nil }}
	if err := q.Enqueue("c", "j", src, testDigest(t, src)); err != nil {
		t.Fatal(err)
	}
	_, err := q.ProcessOnePolicy(context.Background(), "c", Limits{}, 0, Policy{Layout: "same", DeleteOriginal: true})
	if err == nil || !strings.Contains(err.Error(), "archive_destination_exists") {
		t.Fatalf("collision not detected: %v", err)
	}
	data, _ := os.ReadFile(original)
	if string(data) != "user data" {
		t.Fatal("user data overwritten")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("archive removed")
	}
}

func TestCleanupFailurePreservesOriginalAndCanRecover(t *testing.T) {
	src := zipFixture(t, "saved.zip", entry{name: "contents.txt", content: "done"})
	q := &Queue{Dir: filepath.Join(t.TempDir(), "q")}
	if err := q.Enqueue("c", "j", src, testDigest(t, src)); err != nil {
		t.Fatal(err)
	}
	_, err := q.ProcessOnePolicy(context.Background(), "c", Limits{}, 0, Policy{Layout: "same", DeleteOriginal: true})
	if err == nil || !strings.Contains(err.Error(), "archive_cleanup_journal_unavailable") {
		t.Fatalf("missing journal not refused: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("original deleted before journal")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(src), "contents.txt")); err != nil {
		t.Fatal("published file missing")
	}
	q = &Queue{Dir: q.Dir, BeforeRemove: func(string, string, string, string, string) error { return nil }}
	ok, err := q.ProcessOnePolicy(context.Background(), "c", Limits{}, 0, Policy{})
	if !ok || err != nil {
		t.Fatalf("restart cleanup %v %v", ok, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("original not cleaned")
	}
}

func TestFlatPublishInterruptedLinksRestart(t *testing.T) {
	src := zipFixture(t, "restart.zip", entry{name: "one", content: "1"}, entry{name: "two", content: "2"})
	result, err := ExtractVerified(context.Background(), src, testDigest(t, src), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := loadOrCreatePublishPlan(src, result)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != "directory" || plan.Target != "restart" {
		t.Fatalf("plan: %+v", plan)
	}
	dest := filepath.Join(filepath.Dir(src), "restart")
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(result, publishIntent))
	if err := os.WriteFile(filepath.Join(dest, publishInProgress), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(result, "one"), filepath.Join(dest, "one")); err != nil {
		t.Fatal(err)
	}
	got, err := PublishSameDirectory(context.Background(), src, result)
	if err != nil || got != dest {
		t.Fatalf("recovery got %s: %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "two")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("original changed", err)
	}
	if _, err := os.Stat(filepath.Join(dest, publishInProgress)); !os.IsNotExist(err) {
		t.Fatal("transient json visible")
	}
}

func TestSeparateModeDeletesOnlyAfterSuccess(t *testing.T) {
	src := zipFixture(t, "no-delete.zip", entry{name: "file", content: "ok"})
	q := &Queue{Dir: filepath.Join(t.TempDir(), "q")}
	if err := q.Enqueue("c", "j", src, testDigest(t, src)); err != nil {
		t.Fatal(err)
	}
	ok, err := q.ProcessOnePolicy(context.Background(), "c", Limits{}, 0, Policy{})
	if !ok || err != nil {
		t.Fatalf("default extraction: %v %v", ok, err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("original disappeared by default")
	}
	if _, err := os.Stat(src + ".extracted"); err != nil {
		t.Fatal("separate output missing")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(src), "file")); !os.IsNotExist(err) {
		t.Fatal("default flattened output")
	}
}

func TestMultivolumeCleanupRefusesChangedPartBeforeDeletingAny(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "bundle.part01.rar")
	next := filepath.Join(dir, "bundle.part02.rar")
	if err := os.WriteFile(first, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotCleanupFiles(context.Background(), first, testDigest(t, first))
	if err != nil || len(snapshot) != 2 {
		t.Fatalf("snapshot %v %#v", err, snapshot)
	}
	if err := os.WriteFile(next, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupOriginalFiles(context.Background(), snapshot); err == nil {
		t.Fatal("changed sibling accepted")
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal("first volume deleted despite modified second")
	}
	if err := os.WriteFile(next, []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupOriginalFiles(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("first volume not deleted")
	}
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Fatal("second volume not deleted")
	}
	if err := cleanupOriginalFiles(context.Background(), snapshot); err != nil {
		t.Fatalf("resume after completed deletion: %v", err)
	}
}

func TestSmartPublishSingleNestedFileDirectly(t *testing.T) {
	src := zipFixture(t, "release.tar.zip", entry{name: "movie/subtitle.srt", content: "subtitle"})
	root := filepath.Dir(src)
	q := &Queue{Dir: filepath.Join(t.TempDir(), "q"), BeforeRemove: func(clientID, jobID, source, digest, dest string) error {
		if dest != filepath.Join(root, "subtitle.srt") {
			t.Fatalf("unexpected destination %q", dest)
		}
		fi, err := os.Lstat(dest)
		if err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("single-file output not published %v", err)
		}
		return nil
	}}
	if err := q.Enqueue("c", "s", src, testDigest(t, src)); err != nil {
		t.Fatal(err)
	}
	done, err := q.ProcessOnePolicy(context.Background(), "c", Limits{}, 0, Policy{Layout: "same", DeleteOriginal: true})
	if !done || err != nil {
		t.Fatalf("process %v %v", done, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "subtitle.srt"))
	if err != nil || string(content) != "subtitle" {
		t.Fatalf("output: %v %q", err, content)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("original remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "movie")); !os.IsNotExist(err) {
		t.Fatalf("one file should not have enclosing folder: %v", err)
	}
	for _, tmp := range []string{src + ".extracted", filepath.Join(root, markerName), filepath.Join(root, publishIntent)} {
		if _, err := os.Lstat(tmp); !os.IsNotExist(err) {
			t.Fatalf("metadata visible at %s: %v", tmp, err)
		}
	}
}

func TestSmartPublishMultipleFilesSingleFolder(t *testing.T) {
	src := zipFixture(t, "release.zip", entry{name: "video.mkv", content: "movie"}, entry{name: "cover.jpg", content: "poster"})
	root := filepath.Dir(src)
	q := &Queue{Dir: filepath.Join(t.TempDir(), "q")}
	if err := q.Enqueue("c", "m", src, testDigest(t, src)); err != nil {
		t.Fatal(err)
	}
	done, err := q.ProcessOnePolicy(context.Background(), "c", Limits{}, 0, Policy{Layout: "same"})
	if !done || err != nil {
		t.Fatalf("process %v %v", done, err)
	}
	for _, name := range []string{"video.mkv", "cover.jpg"} {
		if _, err := os.Stat(filepath.Join(root, "release", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("file %s not grouped", name)
		}
	}
	for _, name := range []string{markerName, publishIntent, publishInProgress} {
		if _, err := os.Lstat(filepath.Join(root, "release", name)); !os.IsNotExist(err) {
			t.Fatalf("metadata copied %s", name)
		}
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("default must keep source: %v", err)
	}
}

func TestSmartPublishReuseCommonTopFolder(t *testing.T) {
	src := zipFixture(t, "another.zip", entry{name: "AlreadyGrouped/one.txt", content: "1"}, entry{name: "AlreadyGrouped/two.txt", content: "2"})
	result, err := ExtractVerified(context.Background(), src, testDigest(t, src), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	dest, err := PublishSameDirectory(context.Background(), src, result)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(filepath.Dir(src), "AlreadyGrouped")
	if dest != expected {
		t.Fatalf("got %s, want %s", dest, expected)
	}
	if _, err := os.Stat(filepath.Join(expected, "one.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(src), "another")); !os.IsNotExist(err) {
		t.Fatal("redundant folder")
	}
}

func TestSmartPublishNameVariants(t *testing.T) {
	cases := map[string]string{
		"film.rar": "film", "show.part01.rar": "show", "book.7z.001": "book", "things.tar.gz": "things", "things.tgz": "things", "things.tar": "things", "things.zip": "things", "book.7z": "book",
	}
	for name, want := range cases {
		if got := archiveBaseName(name); got != want {
			t.Errorf("%s => %s want %s", name, got, want)
		}
	}
}

func TestSmartPublishSingleFileCollisionKeepsArchive(t *testing.T) {
	src := zipFixture(t, "name.zip", entry{name: "dir/movie.mkv", content: "archive"})
	dest := filepath.Join(filepath.Dir(src), "movie.mkv")
	if err := os.WriteFile(dest, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	q := &Queue{Dir: filepath.Join(t.TempDir(), "q"), BeforeRemove: func(string, string, string, string, string) error { t.Fatal("should never delete"); return nil }}
	if err := q.Enqueue("c", "j", src, testDigest(t, src)); err != nil {
		t.Fatal(err)
	}
	_, err := q.ProcessOnePolicy(context.Background(), "c", Limits{}, 0, Policy{Layout: "same", DeleteOriginal: true})
	if err == nil || !strings.Contains(err.Error(), "archive_destination_exists") {
		t.Fatalf("collision not reported %v", err)
	}
	b, err := os.ReadFile(dest)
	if err != nil || string(b) != "original" {
		t.Fatalf("existing overwritten %s %v", b, err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("archive lost")
	}
}
