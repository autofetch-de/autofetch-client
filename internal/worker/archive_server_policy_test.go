package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/archive"
)

func TestNewJobDoesNotReuseOtherJobsFilesOrPartials(t *testing.T) {
	dir := t.TempDir()
	preferred := filepath.Join(dir, "movie.tar.gz")
	if err := os.WriteFile(preferred+".part", []byte("partial from another source"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := chooseNewJobOutput(preferred, "9d5aa1a5-df52-40aa-9000-c09a7d151225")
	if err != nil || next == preferred || filepath.Ext(next) != ".gz" {
		t.Fatalf("unsafe output: %q %v", next, err)
	}
	if filepath.Base(next) != "movie.9d5aa1a5df52.tar.gz" {
		t.Fatalf("archive extension not preserved: %s", next)
	}
	contents, err := os.ReadFile(preferred + ".part")
	if err != nil || string(contents) != "partial from another source" {
		t.Fatal("original partial touched")
	}
}

func TestPlannedArchivePolicyDoesNotChangeOnRetry(t *testing.T) {
	dir := t.TempDir()
	q := &archive.Queue{Dir: filepath.Join(dir, "queue"), OnlyPlanned: true}
	source := filepath.Join(dir, "example.zip")
	digest := hex.EncodeToString(sha256.New().Sum(make([]byte, 0)))
	// Syntactically valid digest is enough at enqueue; file is verified at extraction.
	if err := q.EnqueuePlanned("c", "j", source, digest, archive.Policy{Layout: "same", DeleteOriginal: true}, 2); err != nil {
		t.Fatal(err)
	}
	if err := q.EnqueuePlanned("c", "j", source, digest, archive.Policy{Layout: "separate", DeleteOriginal: false}, 0); err == nil {
		t.Fatal("policy silently changed")
	}
	hist, err := q.History("c", 10)
	if err != nil || len(hist) != 1 || !hist[0].DeleteOriginal || hist[0].NestedDepth != 2 {
		t.Fatalf("mutated plan: %+v %v", hist, err)
	}
}

func TestReceiptDeletedOnlyAfterAcknowledgedSuccess(t *testing.T) {
	dir := t.TempDir()
	j := DownloadJournal{Dir: filepath.Join(dir, "journal")}
	path := filepath.Join(dir, "file.bin")
	data := []byte("bytes")
	os.WriteFile(path, data, 0600)
	hash := sha256.Sum256(data)
	sum := hex.EncodeToString(hash[:])
	if err := j.BeginAttempt("c", "job", "fingerprint", path, "attempt"); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkTransferred("c", "job", "fingerprint", path, int64(len(data)), sum); err != nil {
		t.Fatal(err)
	}
	if deleted, err := j.ForgetAcknowledgedSuccess("c", "job", true); err != nil || deleted {
		t.Fatal("unacknowledged success lost")
	}
	if err := j.SetReportState("c", "job", "attempt", "SUCCESS", "", "acknowledged"); err != nil {
		t.Fatal(err)
	}
	if deleted, err := j.ForgetAcknowledgedSuccess("c", "job", false); err != nil || deleted {
		t.Fatal("archive pending lost")
	}
	if deleted, err := j.ForgetAcknowledgedSuccess("c", "job", true); err != nil || !deleted {
		t.Fatalf("not cleaned %v %v", deleted, err)
	}
	if _, exists, err := j.Load("c", "job"); err != nil || exists {
		t.Fatal("receipt still present")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("file removed with receipt")
	}
}

func TestOldNonresumableReceiptsPrunedWithoutDeletingPartial(t *testing.T) {
	root := t.TempDir()
	j := DownloadJournal{Dir: filepath.Join(root, "journal")}
	file := filepath.Join(root, "partial.bin")
	if err := j.BeginAttempt("c", "job", "fp", file, "attempt"); err != nil {
		t.Fatal(err)
	}
	rec, _, err := j.Load("c", "job")
	if err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-9 * 24 * time.Hour)
	rec.UpdatedAt = older
	rec.Attempts[0].ReportState = "acknowledged"
	rec.Attempts[0].FinishedAt = &older
	if err := j.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file+".part", []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := j.PruneOldUnfinished(context.Background(), "c", time.Now().Add(-7*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("removed resumable: %d %v", n, err)
	}
	if err := os.Remove(file + ".part"); err != nil {
		t.Fatal(err)
	}
	if n, err := j.PruneOldUnfinished(context.Background(), "c", time.Now().Add(-7*24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("did not prune stale: %d %v", n, err)
	}
}
