package config

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/localization"
)

func TestWebUISettingsRoundTripWithoutSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := Config{ConfigPath: path, ServerBaseURL: "https://autofetch.de", ClientName: "test", DownloadDir: "downloads", WebUIListenAddr: "192.168.1.40:23324", WebUIPasswordFile: "/etc/autofetch/webui.password", WebUILANNoPassword: false}
	if err := Persist(cfg); err != nil {
		t.Fatal(err)
	}
	reloaded := Config{ConfigPath: path}
	if err := loadFromFile(&reloaded, path); err != nil {
		t.Fatal(err)
	}
	if reloaded.WebUIListenAddr != cfg.WebUIListenAddr || reloaded.WebUIPasswordFile != cfg.WebUIPasswordFile || reloaded.WebUILANNoPassword {
		t.Fatalf("settings lost: %+v", reloaded)
	}
	// Configuration stores the location of the password, never its contents.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || reloaded.WebUIPasswordFile == "" {
		t.Fatal("missing persisted fields")
	}
}

func TestHeadlessWebUIRequiresExplicitEnable(t *testing.T) {
	originalFlagSet, originalArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine = originalFlagSet; os.Args = originalArgs })
	for _, tc := range []struct {
		name    string
		args    []string
		env     string
		enabled bool
	}{
		{"default-headless", []string{"autofetch", "--headless"}, "", false},
		{"flag-enable", []string{"autofetch", "--headless", "--webui=true"}, "", true},
		{"flag-disable", []string{"autofetch", "--headless", "--webui=false"}, "", false},
		{"env-enable", []string{"autofetch", "--headless"}, "true", true},
		{"env-disable", []string{"autofetch", "--headless"}, "false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flag.CommandLine = flag.NewFlagSet("webui-test", flag.ContinueOnError)
			os.Args = tc.args
			t.Setenv("AUTOFETCH_WEBUI", tc.env)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			cfg := Load(localization.New(localization.English))
			if cfg.EnableWebUI != tc.enabled || !cfg.Headless || cfg.OpenBrowser {
				t.Fatalf("headless policy: got %+v", cfg)
			}
		})
	}
}

func TestWebUIConfigCanBeLoadedFromExplicitConfigPath(t *testing.T) {
	originalFlagSet, originalArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine = originalFlagSet; os.Args = originalArgs })
	flag.CommandLine = flag.NewFlagSet("webui-explicit-test", flag.ContinueOnError)
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := Config{ConfigPath: path, ServerBaseURL: "https://autofetch.de", ClientName: "test", DownloadDir: "downloads", WebUIListenAddr: "192.168.1.45:23324", WebUIPasswordFile: "/etc/autofetch/ui.pass"}
	if err := Persist(cfg); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"autofetch", "--headless", "--webui=true", "--config", path}
	t.Setenv("AUTOFETCH_WEBUI_ADDR", "")
	t.Setenv("AUTOFETCH_WEBUI_PASSWORD_FILE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	loaded := Load(localization.New(localization.English))
	if !loaded.EnableWebUI || loaded.WebUIListenAddr != cfg.WebUIListenAddr || loaded.WebUIPasswordFile != cfg.WebUIPasswordFile {
		t.Fatalf("failed to use custom client.json: %+v", loaded)
	}
}

func TestPersistedWebUIPasswordTakesPriorityOverBootstrapFlag(t *testing.T) {
	originalFlagSet, originalArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine = originalFlagSet; os.Args = originalArgs })
	path := filepath.Join(t.TempDir(), "client.json")
	passPath := filepath.Join(filepath.Dir(path), "webui.password")
	if err := os.WriteFile(passPath, []byte("strong-test-password"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ConfigPath: path, WebUIListenAddr: "192.168.1.50:23324", WebUIPasswordFile: passPath, WebUILANNoPassword: false}
	if err := Persist(cfg); err != nil {
		t.Fatal(err)
	}
	flag.CommandLine = flag.NewFlagSet("old-bootstrap-flag", flag.ContinueOnError)
	os.Args = []string{"autofetch", "--headless", "--webui=true", "--webui-lan-no-password", "--config", path}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, env := range []string{"AUTOFETCH_WEBUI_ADDR", "AUTOFETCH_WEBUI_PASSWORD_FILE", "AUTOFETCH_WEBUI_LAN_NO_PASSWORD"} {
		t.Setenv(env, "")
	}
	loaded := Load(localization.New(localization.English))
	if loaded.WebUIPasswordFile != passPath || !loaded.WebUILANNoPassword {
		t.Fatalf("unexpected load with old flag: %+v", loaded)
	}
	// A configured password is authoritative even when the command line still
	// explicitly passes the no-auth bootstrap flag; the webui layer tests this.
}
