package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
	clientruntime "github.com/autofetch-de/autofetch-client/internal/runtime"
)

func TestRunnerPausesAfterThreeCredentialDenialsAndDoesNotKeepPolling(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"forbidden","message":"invalid client credentials"}`))
	}))
	defer srv.Close()
	cli := api.New(srv.URL, "client-a", "secret")
	root := t.TempDir()
	runner := &Runner{API: cli, ClientID: "client-a", DownloadDir: root, JournalDir: filepath.Join(root, "journal"), AuthRetryDelay: time.Millisecond}
	runner.RuntimeConfig = clientruntime.NewConfigManager(cli, clientruntime.DefaultConfig(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	if err := runner.Run(ctx); !errors.Is(err, ErrClientAuthSuspended) {
		t.Fatalf("want suspended after 3 credential errors, got %v", err)
	}
	if n := requests.Load(); n != 3 {
		t.Fatalf("want exactly 3 config requests and no extra leases, got %d", n)
	}
	time.Sleep(15 * time.Millisecond)
	if n := requests.Load(); n != 3 {
		t.Fatalf("worker kept polling after automatic pause: %d", n)
	}
}

func TestRunnerSuccessfulLeaseResetsAuthDenialCounter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 || n == 2 || n == 4 || n == 5 {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"forbidden","message":"invalid client credentials"}`))
		} else {
			_, _ = w.Write([]byte(`{"job":null,"retry_after_seconds":1}`))
		}
	}))
	defer srv.Close()
	cli := api.New(srv.URL, "client-a", "secret")
	root := t.TempDir()
	runner := &Runner{API: cli, ClientID: "client-a", DownloadDir: root, JournalDir: filepath.Join(root, "journal"), AuthRetryDelay: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2300*time.Millisecond)
	defer cancel()
	err := runner.Run(ctx)
	if errors.Is(err, ErrClientAuthSuspended) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("successful request must reset consecutive auth errors, got %v", err)
	}
	if n := calls.Load(); n < 5 {
		t.Fatalf("expected two separate denial sequences, got %d calls", n)
	}
}
