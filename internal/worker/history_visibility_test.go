package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHideHistoryKeepsReceiptsAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	j := DownloadJournal{Dir: filepath.Join(t.TempDir(), "journal")}
	if err := j.BeginAttempt("a", "job1", "fp", "/tmp/sample.rar", "attempt-1"); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginAttempt("a", "job2", "fp", "/tmp/another.rar", "attempt-2"); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginAttempt("b", "job3", "fp", "/tmp/other.rar", "attempt-3"); err != nil {
		t.Fatal(err)
	}

	if err := j.HideHistory(ctx, "a", "job1", false); err != nil {
		t.Fatal(err)
	}
	restarted := DownloadJournal{Dir: j.Dir}
	items, err := restarted.History(ctx, "a", 100)
	if err != nil || len(items) != 1 || items[0].JobID != "job2" {
		t.Fatalf("hidden entry reappeared: %#v %v", items, err)
	}
	// The receipts are part of the download protocol and may never be removed.
	if rec, ok, err := restarted.Load("a", "job1"); err != nil || !ok || len(rec.Attempts) != 1 {
		t.Fatalf("deleted receipt: %#v %v %v", rec, ok, err)
	}
	if items, err := restarted.History(ctx, "b", 100); err != nil || len(items) != 1 {
		t.Fatalf("cross-client side effect: %#v %v", items, err)
	}

	// A new server-leased attempt is fresh activity and can reappear.
	time.Sleep(time.Millisecond)
	if err := restarted.BeginAttempt("a", "job1", "fp", "/tmp/sample.rar", "attempt-4"); err != nil {
		t.Fatal(err)
	}
	items, err = restarted.History(ctx, "a", 100)
	if err != nil || len(items) != 2 {
		t.Fatalf("new activity hidden: %#v %v", items, err)
	}

	if err := restarted.HideHistory(ctx, "a", "", true); err != nil {
		t.Fatal(err)
	}
	items, err = restarted.History(ctx, "a", 100)
	if err != nil || len(items) != 0 {
		t.Fatalf("clear all did not hide records: %#v %v", items, err)
	}
	if _, ok, err := restarted.Load("a", "job2"); err != nil || !ok {
		t.Fatalf("clear removed technical record: %v %v", ok, err)
	}
	if err := restarted.BeginAttempt("a", "job4", "fp", "/tmp/new.tar", "attempt-5"); err != nil {
		t.Fatal(err)
	}
	items, err = restarted.History(ctx, "a", 100)
	if err != nil || len(items) != 1 || items[0].JobID != "job4" {
		t.Fatalf("new record hidden after clear: %#v %v", items, err)
	}
	path, err := restarted.visibilityPath("a")
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("visibility file permissions: %#v %v", st, err)
	}
}

func TestHideHistoryRequiresExistingJobAndValidIdentity(t *testing.T) {
	j := DownloadJournal{Dir: filepath.Join(t.TempDir(), "journal")}
	ctx := context.Background()
	if err := j.HideHistory(ctx, "client", "missing", false); err == nil {
		t.Fatal("missing job accepted")
	}
	if err := j.HideHistory(ctx, "", "", true); err == nil {
		t.Fatal("anonymous history changed")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := j.HideHistory(cancelled, "client", "", true); err == nil {
		t.Fatal("cancellation ignored")
	}
}
