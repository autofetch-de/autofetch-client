package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstanceIDPersistedAndPreservedAcrossRePair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autofetch", "client.json")
	cfg := Config{ConfigPath: path, ServerBaseURL: "https://autofetch.de", ClientName: "test", ClientID: "client-1", ClientToken: "token-1", DownloadDir: "./downloads"}
	if err := EnsureInstanceID(&cfg); err != nil {
		t.Fatal(err)
	}
	original := cfg.InstanceID
	if !installationUUIDPattern.MatchString(original) {
		t.Fatalf("not v4 UUID: %q", original)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("missing persisted configuration")
	}

	reloaded := Config{ConfigPath: path}
	if err := loadFromFile(&reloaded, path); err != nil {
		t.Fatal(err)
	}
	if reloaded.InstanceID != original {
		t.Fatalf("restart rotated identity: %s", reloaded.InstanceID)
	}
	if reloaded.ClientID != cfg.ClientID || reloaded.ClientToken != cfg.ClientToken {
		t.Fatal("pairing credentials were modified")
	}
	if err := EnsureInstanceID(&reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.InstanceID != original {
		t.Fatal("second EnsureInstanceID rotated identity")
	}
	if err := ClearCredentials(&reloaded); err != nil {
		t.Fatal(err)
	}
	rePaired := Config{ConfigPath: path}
	if err := loadFromFile(&rePaired, path); err != nil {
		t.Fatal(err)
	}
	if rePaired.InstanceID != original {
		t.Fatal("re-pair rotated identity")
	}
	if rePaired.ClientID != "" || rePaired.ClientToken != "" {
		t.Fatal("credentials not cleared")
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("client.json permissions: %v, err %v", st, err)
	}
}

func TestInstallationIDsAreUnique(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 40; i++ {
		cfg := Config{ConfigPath: filepath.Join(t.TempDir(), "client.json")}
		if err := EnsureInstanceID(&cfg); err != nil {
			t.Fatal(err)
		}
		if ids[cfg.InstanceID] {
			t.Fatalf("duplicate UUID: %s", cfg.InstanceID)
		}
		ids[cfg.InstanceID] = true
	}
}

func TestInvalidInstanceIDFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	for _, invalid := range []string{"bad-id", "00000000-0000-1000-8000-000000000000", "00000000-0000-4000-0000-000000000000"} {
		cfg := Config{ConfigPath: path, InstanceID: invalid}
		if err := EnsureInstanceID(&cfg); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
		if cfg.InstanceID != invalid {
			t.Fatalf("silently rotated %q", invalid)
		}
	}
}

func TestIdentityFailureDoesNotClaimPersistence(t *testing.T) {
	// Passing a directory as config path forces the final rename to fail.
	cfg := Config{ConfigPath: t.TempDir()}
	if err := EnsureInstanceID(&cfg); err == nil {
		t.Fatal("expected persistence failure")
	}
	if cfg.InstanceID != "" {
		t.Fatal("kept identity that was not persisted")
	}
}
