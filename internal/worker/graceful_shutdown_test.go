package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
)

// Ctrl+C cancels the transfer context. The last completion request must NOT
// reuse that canceled context, otherwise the server retains the lease.
func TestCanceledTransferReportsShutdownWithFreshContext(t *testing.T) {
	started := make(chan struct{}, 1)
	completed := make(chan api.CompleteRequest, 1)
	var serverURL string
	leaseCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client/v1/jobs/lease":
			leaseCount++
			if leaseCount == 1 {
				_ = json.NewEncoder(w).Encode(api.LeaseResponse{Job: &api.LeasedJob{
					JobID: "graceful-job", Type: "download", Lease: api.Lease{AttemptID: "graceful-attempt"},
					Payload: api.JobPayload{Download: &api.DownloadPayload{Instruction: api.DownloadInstruction{
						Mode: "single", VideoURL: serverURL + "/file", ExpectedSize: 200000,
						Output: &api.InstructionOutput{FilenameVideo: "sample.bin"},
					}}},
				}})
			} else {
				_ = json.NewEncoder(w).Encode(api.LeaseResponse{RetryAfterSeconds: 30})
			}
		case "/file":
			_, _ = io.WriteString(w, "partial test payload")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case started <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		case "/api/client/v1/jobs/graceful-job/complete":
			var req api.CompleteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode completion: %v", err)
			}
			select {
			case completed <- req:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	dir := t.TempDir()
	client := api.New(serverURL, "client-test", "secret")
	runner := &Runner{API: client, DLHTTP: server.Client(), ClientID: "client-test", DownloadDir: dir,
		JournalDir: filepath.Join(dir, "journal"), HeartbeatInterval: time.Hour, HeartbeatExtend: 600}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("transfer never started")
	}
	// Wait until the first bytes are durably present, rather than interrupting
	// the request before the downloader has created its .part file.
	partPath := filepath.Join(dir, "sample.bin.part")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if st, err := os.Stat(partPath); err == nil && st.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("partial file never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case c := <-completed:
		if c.AttemptID != "graceful-attempt" || c.Result.Status != "RETRYABLE_ERROR" || c.Result.Error != "client_shutdown" {
			t.Fatalf("unexpected release completion: %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no shutdown completion")
	}
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
	if _, err := os.Stat(filepath.Join(dir, "sample.bin.part")); err != nil {
		t.Fatalf("resume data should survive stop: %v", err)
	}
}
