package worker

import (
	"path/filepath"
	"testing"
)

func TestDownloadJournalRebindsOnlyFailedSourceAndRetainsHistory(t *testing.T) {
	j := DownloadJournal{Dir: filepath.Join(t.TempDir(), "journal")}
	client, job, target := "client", "job", filepath.Join(t.TempDir(), "file.rar")
	if err := j.BeginAttempt(client, job, "source-A", target, "first"); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginAttempt(client, job, "source-B", target, "new", true); err == nil {
		t.Fatal("unacknowledged transfer must not change source")
	}
	if err := j.SetReportState(client, job, "first", "FAILED", "dcc_private_address_rejected", "acknowledged"); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginAttempt(client, job, "source-B", target, "new"); err == nil {
		t.Fatal("non-fallback lease must not change source")
	}
	if err := j.BeginAttempt(client, job, "source-B", target, "new", true); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := j.Load(client, job)
	if err != nil || !ok {
		t.Fatalf("load %v", err)
	}
	if rec.InstructionHash != "source-B" || len(rec.Attempts) != 2 || rec.Attempts[0].ErrorCode != "dcc_private_address_rejected" {
		t.Fatalf("bad source transition: %+v", rec)
	}
}
func TestDownloadJournalRejectsRebindAfterTransfer(t *testing.T) {
	j := DownloadJournal{Dir: filepath.Join(t.TempDir(), "journal")}
	target := filepath.Join(t.TempDir(), "file")
	if err := j.BeginAttempt("client", "job", "source-A", target, "first"); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkTransferred("client", "job", "source-A", target, 20, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"); err != nil {
		t.Fatal(err)
	}
	if err := j.SetReportState("client", "job", "first", "FAILED", "error", "acknowledged"); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginAttempt("client", "job", "source-B", target, "second", true); err == nil {
		t.Fatal("completed transfer was rebound")
	}
}
