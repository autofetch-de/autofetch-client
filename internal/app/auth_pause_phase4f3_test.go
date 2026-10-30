package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
)

func TestPausedClientCanStartUIWithoutPollingAndOnlyRePairClearsBlock(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	now := time.Now().UTC()
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "client.json"), ServerBaseURL: server.URL, ClientID: "paused-a", ClientToken: "secret", DownloadDir: t.TempDir(), AuthPausedAt: &now}
	service, state, err := Bootstrap(&cfg, buildinfo.Info{Version: "dev", Platform: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("paused client contacted server during bootstrap")
	}
	if err := service.Start(); err == nil || !strings.Contains(err.Error(), "re-pair required") {
		t.Fatalf("unexpected restart: %v", err)
	}
	if state.Snapshot().Running {
		t.Fatal("paused client is running")
	}
	if requests.Load() != 0 {
		t.Fatal("paused client contacted server")
	}
	if err := config.ClearCredentials(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AuthPausedAt != nil {
		t.Fatal("explicit re-pair did not clear pause")
	}
}

func TestOldRevocationMarkerMigratesToNonPollingPause(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	now := time.Now().UTC()
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "client.json"), ServerBaseURL: server.URL, ClientID: "revoked-a", ClientToken: "secret", DownloadDir: t.TempDir(), RevokedAt: &now}
	service, _, err := Bootstrap(&cfg, buildinfo.Info{Version: "dev", Platform: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthPausedAt == nil {
		t.Fatal("legacy revocation did not migrate to paused state")
	}
	if err := service.Start(); err == nil {
		t.Fatal("legacy revoked client resumed")
	}
	if requests.Load() != 0 {
		t.Fatal("revoked client contacted server")
	}
}
