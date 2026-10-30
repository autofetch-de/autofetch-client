package worker

import (
	"context"
	"errors"
	"github.com/autofetch-de/autofetch-client/internal/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHistorySurvivesRestartAndTracksReportOutcomes(t *testing.T) {
	j := DownloadJournal{Dir: filepath.Join(t.TempDir(), "receipts")}
	target := filepath.Join(t.TempDir(), "example.rar")
	if err := os.WriteFile(target, []byte("example payload"), 0600); err != nil {
		t.Fatal(err)
	}
	n, hash, err := hashLocalFile(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.BeginAttempt("paired-client", "job-1", "fingerprint", target, "attempt-1"); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkTransferred("paired-client", "job-1", "fingerprint", target, n, hash); err != nil {
		t.Fatal(err)
	}
	if err := j.SetReportState("paired-client", "job-1", "attempt-1", "SUCCESS", "", "pending"); err != nil {
		t.Fatal(err)
	}
	// A process restart and a fresh server lease must NOT erase transferred evidence.
	restarted := DownloadJournal{Dir: j.Dir}
	if err := restarted.BeginAttempt("paired-client", "job-1", "fingerprint", target, "attempt-2"); err != nil {
		t.Fatal(err)
	}
	rec, found, err := restarted.Load("paired-client", "job-1")
	if err != nil || !found {
		t.Fatal(err)
	}
	verified, err := restarted.VerifyCompleted(context.Background(), rec, "fingerprint", target)
	if err != nil || !verified {
		t.Fatalf("file redownload risk: %v %v", verified, err)
	}
	if rec.Phase != "transferred" || len(rec.Attempts) != 2 || rec.Attempts[0].ReportState != "pending" {
		t.Fatalf("history lost: %+v", rec)
	}
	if err := restarted.SetReportState("paired-client", "job-1", "attempt-2", "SUCCESS", "", "acknowledged"); err != nil {
		t.Fatal(err)
	}
	list, err := restarted.History(context.Background(), "paired-client", 20)
	if err != nil || len(list) != 1 || list[0].AttemptCount != 2 || list[0].LastResult != "SUCCESS" || list[0].ReportState != "acknowledged" {
		t.Fatalf("history=%+v err=%v", list, err)
	}
	other, err := restarted.History(context.Background(), "other-client", 20)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-pairing records: %+v err=%v", other, err)
	}
}
func TestHistoryInterruptedAndBounded(t *testing.T) {
	j := DownloadJournal{Dir: filepath.Join(t.TempDir(), "journal")}
	for i := 0; i < 45; i++ {
		id := time.Unix(int64(i), 0).UTC().Format("150405")
		if err := j.BeginAttempt("a", "b", "fp", "/tmp/test", id); err != nil {
			t.Fatal(err)
		}
	}
	rec, ok, err := j.Load("a", "b")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if len(rec.Attempts) != maxRecordedAttempts {
		t.Fatalf("attempts=%d", len(rec.Attempts))
	}
	if rec.Attempts[len(rec.Attempts)-2].Result != "INTERRUPTED" {
		t.Fatalf("did not record interrupted attempt: %+v", rec.Attempts[len(rec.Attempts)-2])
	}
	if err := j.BeginAttempt("a", "b", "different", "/tmp/test", "new"); err == nil {
		t.Fatal("changed instruction accepted")
	}
}
func TestHistoryRedactsServerErrorsAndSkipsCorruption(t *testing.T) {
	j := DownloadJournal{Dir: filepath.Join(t.TempDir(), "journal")}
	if err := j.BeginAttempt("a", "b", "fp", "/tmp/file.rar", "attempt"); err != nil {
		t.Fatal(err)
	}
	secret := "http_500: https://example.test/download?token=private-secret"
	if err := j.SetReportState("a", "b", "attempt", "RETRYABLE_ERROR", secret, "lease_lost"); err != nil {
		t.Fatal(err)
	}
	list, err := j.History(context.Background(), "a", 20)
	if err != nil || len(list) != 1 || list[0].LastErrorCode != "http_500" || list[0].ReportState != "lease_lost" {
		t.Fatalf("history=%+v error=%v", list, err)
	}
	path, _ := j.filename("a", "b")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-secret") {
		t.Fatal("secret leaked to disk")
	}
	if err := os.WriteFile(filepath.Join(j.Dir, "corrupt.json"), []byte("oops"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.History(context.Background(), "a", 20); err != nil {
		t.Fatal(err)
	}
}

// A 409 means the old lease no longer belongs to this attempt. In particular,
// the worker must not record it as an acknowledged successful download.
func TestCompletionConflictIsNotAcknowledged(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.Path, "/complete") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"attempt_not_open"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	j := DownloadJournal{Dir: filepath.Join(t.TempDir(), "journal")}
	if err := j.BeginAttempt("client", "job", "fp", "/tmp/file", "attempt-1"); err != nil {
		t.Fatal(err)
	}
	r := Runner{API: api.New(server.URL, "client", "token"), ClientID: "client", JournalDir: j.Dir}
	job := &api.LeasedJob{JobID: "job", Lease: api.Lease{AttemptID: "attempt-1"}}
	err := r.completeWithRetry(context.Background(), job, "SUCCESS", "", nil)
	if !errors.Is(err, errLeaseLost) {
		t.Fatalf("late completion treated as success: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("conflict retried %d times", calls.Load())
	}
	rec, found, err := j.Load("client", "job")
	if err != nil || !found {
		t.Fatal(err)
	}
	if rec.Attempts[0].ReportState != "lease_lost" {
		t.Fatalf("wrong acknowledgement: %+v", rec.Attempts[0])
	}
}
