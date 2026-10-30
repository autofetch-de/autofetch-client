package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAuthPausePersistsUntilExplicitRePair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	now := time.Now().UTC().Truncate(time.Second)
	cfg := Config{ConfigPath: path, ClientID: "test-client", ClientToken: "test-token", ServerBaseURL: "https://autofetch.de", DownloadDir: ".", AuthPausedAt: &now, InstanceID: "5c6a8b1d-168a-4c0f-8445-f8cabd972dde"}
	if err := Persist(cfg); err != nil {
		t.Fatal(err)
	}
	reloaded := Config{ConfigPath: path}
	if err := loadFromFile(&reloaded, path); err != nil {
		t.Fatal(err)
	}
	if reloaded.AuthPausedAt == nil || !reloaded.AuthPausedAt.Equal(now) {
		t.Fatalf("auth pause not restored: %+v", reloaded.AuthPausedAt)
	}
	if reloaded.ClientID != cfg.ClientID {
		t.Fatal("client lost pairing identity")
	}
	if err := ClearCredentials(&reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.AuthPausedAt != nil || reloaded.ClientID != "" || reloaded.ClientToken != "" {
		t.Fatalf("clear pairing did not remove pause: %+v", reloaded)
	}
	fresh := Config{ConfigPath: path}
	if err := loadFromFile(&fresh, path); err != nil {
		t.Fatal(err)
	}
	if fresh.AuthPausedAt != nil {
		t.Fatal("pause survived explicit re-pair")
	}
	if fresh.InstanceID != cfg.InstanceID {
		t.Fatal("re-pair changed installation identity")
	}
}
