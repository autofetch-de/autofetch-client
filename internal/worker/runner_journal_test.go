package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
)

// Exercise the actual worker, HTTP transfer and on-disk receipt. In this case
// the server-provided expected size is intentionally stale; the worker must
// trust the complete HTTP response size and still acknowledge success. Each runner
// gets exactly one lease, so the first process cannot accidentally consume the
// second lease before it is stopped. After an acknowledged SUCCESS, a fresh
// server lease of the same job must trigger a fresh download.
func TestRunnerReusesVerifiedTransferAfterRestart(t *testing.T) {
	var hits atomic.Int32
	var attempts atomic.Int32
	var runGeneration atomic.Int32
	var leasedGeneration atomic.Int32
	completed := make(chan api.CompleteResult, 5)
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file":
			hits.Add(1)
			w.Write([]byte("test downloadable payload"))
		case "/api/client/v1/jobs/lease":
			// Only issue one lease per simulated process lifetime. Otherwise
			// the first worker can consume attempt 2 during the 50ms shutdown
			// grace period, leaving the restarted worker with no job.
			generation := runGeneration.Load()
			if generation > 0 && leasedGeneration.CompareAndSwap(generation-1, generation) {
				n := attempts.Add(1)
				json.NewEncoder(w).Encode(api.LeaseResponse{Job: &api.LeasedJob{
					JobID: "same-job", Type: "download", Lease: api.Lease{AttemptID: fmt.Sprintf("attempt-%d", n), ExpiresAt: time.Now().Add(time.Minute)},
					Payload: api.JobPayload{Download: &api.DownloadPayload{Instruction: api.DownloadInstruction{
						Mode: "single", VideoURL: serverURL + "/file", ExpectedSize: int64(len("test downloadable payload")) + 17, Output: &api.InstructionOutput{FilenameVideo: "file.rar"},
					}}},
				}})
			} else {
				json.NewEncoder(w).Encode(api.LeaseResponse{RetryAfterSeconds: 1})
			}
		case "/api/client/v1/jobs/same-job/complete":
			var req api.CompleteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode completion: %v", err)
			}
			if req.Result.Status == "SUCCESS" {
				completed <- req.Result
			}
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	root := t.TempDir()
	runOnce := func() {
		runGeneration.Add(1)
		client := api.New(server.URL, "client-a", "token")
		runner := &Runner{API: client, DLHTTP: server.Client(), ClientID: "client-a", DownloadDir: root, JournalDir: filepath.Join(root, ".local-journal"), HeartbeatInterval: time.Hour, HeartbeatExtend: 600, DedupeTTLSeconds: 1800}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- runner.Run(ctx) }()
		select {
		case result := <-completed:
			if result.Status != "SUCCESS" {
				t.Fatal(result)
			}
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("worker did not complete")
		}
		// Do not cancel while the SUCCESS response is still in flight. The
		// worker must acknowledge it and prune its settled receipt before
		// this process is stopped and the next lease begins.
		journal := DownloadJournal{Dir: filepath.Join(root, ".local-journal")}
		deadline := time.After(5 * time.Second)
		for {
			_, exists, err := journal.Load("client-a", "same-job")
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			if !exists {
				break
			}
			select {
			case <-deadline:
				cancel()
				t.Fatal("successful download receipt was not pruned after server acknowledgement")
			case <-time.After(10 * time.Millisecond):
			}
		}
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not stop")
		}
	}
	runOnce()
	runOnce()
	if hits.Load() != 2 {
		t.Fatalf("new server lease after an acknowledged success is a new download: got %d", hits.Load())
	}
	b, err := os.ReadFile(filepath.Join(root, "file.rar"))
	if err != nil || string(b) != "test downloadable payload" {
		t.Fatalf("file missing: %q %v", b, err)
	}
}

func TestRunnerNeverCallsDownloadForUnverifiedExistingFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.rar"), []byte("some other content"), 0600); err != nil {
		t.Fatal(err)
	}
	var leases atomic.Int32
	var gets atomic.Int32
	completed := make(chan api.CompleteResult, 1)
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file":
			gets.Add(1)
			io.WriteString(w, "new content")
		case "/api/client/v1/jobs/lease":
			if leases.Add(1) > 1 {
				_ = json.NewEncoder(w).Encode(api.LeaseResponse{RetryAfterSeconds: 1})
				return
			}
			json.NewEncoder(w).Encode(api.LeaseResponse{Job: &api.LeasedJob{JobID: "job", Type: "download", Lease: api.Lease{AttemptID: "a"}, Payload: api.JobPayload{Download: &api.DownloadPayload{Instruction: api.DownloadInstruction{Mode: "single", VideoURL: serverURL + "/file", Output: &api.InstructionOutput{FilenameVideo: "file.rar"}}}}}})
		case "/api/client/v1/jobs/job/complete":
			var req api.CompleteRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			select {
			case completed <- req.Result:
			default:
			}
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	client := api.New(server.URL, "client-a", "token")
	runner := &Runner{API: client, DLHTTP: server.Client(), ClientID: "client-a", DownloadDir: root, JournalDir: filepath.Join(root, "journal")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case result := <-completed:
		if result.Status != "SUCCESS" {
			t.Errorf("explicit new job should download to a different filename: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no completion")
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
	if gets.Load() != 1 {
		t.Fatal("new job must download instead of refusing existing file")
	}
	b2, err := os.ReadFile(filepath.Join(root, "file.job.rar"))
	if err != nil || string(b2) != "new content" {
		t.Fatalf("new file unavailable: %q %v", b2, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "file.rar"))
	if string(b) != "some other content" {
		t.Fatal("existing file changed")
	}
}
