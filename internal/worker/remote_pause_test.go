package worker

import (
	"context"
	"encoding/json"
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

// A pause response from the server must interrupt the active HTTP transfer,
// retain .part, and acknowledge PAUSED without issuing any new transfer itself.
func TestRemotePauseRetainsPartAndAcknowledges(t *testing.T) {
	started := make(chan struct{}, 1)
	completion := make(chan api.CompleteRequest, 1)
	var files atomic.Int32
	var leases atomic.Int32
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/client/v1/jobs/lease":
			if leases.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(api.LeaseResponse{Job: &api.LeasedJob{
					JobID: "pause-job", Type: "download", Lease: api.Lease{AttemptID: "pause-attempt"},
					Payload: api.JobPayload{Download: &api.DownloadPayload{Instruction: api.DownloadInstruction{
						Mode: "single", VideoURL: serverURL + "/file", ExpectedSize: 1000000,
						Output: &api.InstructionOutput{FilenameVideo: "sample.bin"},
					}}},
				}})
			} else {
				_ = json.NewEncoder(w).Encode(api.LeaseResponse{RetryAfterSeconds: 30})
			}
		case "/file":
			files.Add(1)
			_, _ = io.WriteString(w, "begin of incomplete transfer")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case started <- struct{}{}:
			default:
			}
			<-req.Context().Done()
		case "/api/client/v1/jobs/pause-job/heartbeat":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"job_paused"}`))
		case "/api/client/v1/jobs/pause-job/complete":
			var body api.CompleteRequest
			_ = json.NewDecoder(req.Body).Decode(&body)
			select {
			case completion <- body:
			default:
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	dir := t.TempDir()
	client := api.New(server.URL, "client-1", "token")
	r := &Runner{API: client, DLHTTP: server.Client(), ClientID: "client-1", DownloadDir: dir,
		JournalDir: filepath.Join(dir, "journal"), HeartbeatInterval: time.Second, HeartbeatExtend: 600}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- r.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(4 * time.Second):
		t.Fatal("transfer not started")
	}
	select {
	case got := <-completion:
		if got.AttemptID != "pause-attempt" || got.Result.Status != "PAUSED" || got.Result.Error != "job_paused" {
			t.Fatalf("unexpected pause acknowledgement: %+v", got)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("pause not acknowledged")
	}
	part := filepath.Join(dir, "sample.bin.part")
	if info, err := os.Stat(part); err != nil || info.Size() == 0 {
		t.Fatalf("pause lost .part: %v %v", info, err)
	}
	if files.Load() != 1 {
		t.Fatalf("unexpected transfer count %d", files.Load())
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(4 * time.Second):
		t.Fatal("runner did not exit")
	}
}
