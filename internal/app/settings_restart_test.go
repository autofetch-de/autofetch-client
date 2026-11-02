package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/observe"
)

// A running client's worker may still be closing after its cancellation.
// Saving IRC settings must not report an error when persistence succeeded.
func TestUpdateLocalSettingsDefersRestartUntilOldWorkerFinishes(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigPath:  filepath.Join(dir, "client.json"),
		DownloadDir: dir,
		IRC: config.IRCConfig{Networks: []config.IRCNetwork{{
			Host: "irc.example.net", Nick: "saved-nick", Port: 6697, TLS: true,
			NickServ: config.IRCNickServ{Password: "private-secret", RegistrationPending: true},
		}}},
	}
	s := NewService(&cfg, buildinfo.Info{}, nil, observe.NewState("", "", "", 5), nil)
	s.running = true
	oldWorkerDone := make(chan struct{})
	s.runnerDone = oldWorkerDone
	// Cancel the queued restart before allowing the simulated old worker to finish.
	defer func() {
		_ = s.Pause()
		close(oldWorkerDone)
	}()

	updated := cfg.IRC.WithDefaults()
	updated.Networks[0].NickServ = config.IRCNickServ{RequiresRegistration: true}
	if err := s.UpdateLocalSettings(dir, updated, false, ""); err != nil {
		t.Fatalf("persisted settings must not fail because previous worker is still stopping: %v", err)
	}
	if s.running {
		t.Fatal("old worker must be marked stopped while its shutdown completes")
	}
	data, err := os.ReadFile(cfg.ConfigPath)
	if err != nil || !strings.Contains(string(data), `"requires_registration": true`) || strings.Contains(string(data), `"registration_pending": true`) {
		t.Fatalf("reset status not saved, err=%v", err)
	}
}

func TestUpdateLocalSettingsPersistenceFailureDoesNotClearPendingIdentityInMemory(t *testing.T) {
	cfg := config.Config{
		ConfigPath:  "", // deliberately invalid: persistence must fail
		DownloadDir: "/tmp/downloads",
		IRC: config.IRCConfig{Networks: []config.IRCNetwork{{
			Host: "irc.example.net", Nick: "original-nick",
			NickServ: config.IRCNickServ{Password: "still-saved", RegistrationPending: true},
		}}},
	}
	s := NewService(&cfg, buildinfo.Info{}, nil, nil, nil)
	next := cfg.IRC.WithDefaults()
	next.Networks = append([]config.IRCNetwork(nil), next.Networks...)
	next.Networks[0].NickServ = config.IRCNickServ{}
	if err := s.UpdateLocalSettings(cfg.DownloadDir, next, false, ""); err == nil {
		t.Fatal("expected config persistence to fail")
	}
	if cfg.IRC.Networks[0].NickServ.Password != "still-saved" || !cfg.IRC.Networks[0].NickServ.RegistrationPending {
		t.Fatal("failed save must not change the in-memory registration state")
	}
}

// A restart may only begin when the previous worker's completion channel
// is closed. The new settings must be the ones used for that restart.
func TestUpdateLocalSettingsRestartsAfterPreviousWorkerCompletes(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{ConfigPath: filepath.Join(dir, "client.json"), DownloadDir: dir}
	s := NewService(&cfg, buildinfo.Info{}, nil, observe.NewState("", "", "", 5), nil)
	s.running = true
	previousDone := make(chan struct{})
	s.runnerDone = previousDone
	defer s.Pause()
	irc := config.IRCConfig{DefaultNick: "some-nick"}
	if err := s.UpdateLocalSettings(dir, irc, false, ""); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if s.runnerDone != previousDone {
		s.mu.Unlock()
		t.Fatal("replacement runner launched before old worker finished")
	}
	s.mu.Unlock()
	close(previousDone)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		restarted := s.runnerDone != previousDone
		s.mu.Unlock()
		if restarted {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("replacement runner was not scheduled after old worker finished")
}
