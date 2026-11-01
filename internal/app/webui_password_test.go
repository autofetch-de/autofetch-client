package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
)

func TestWebUIPasswordBootstrapPersistsPrivately(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{ConfigPath: filepath.Join(dir, "client.json"), ClientName: "webui-bootstrap-test", DownloadDir: dir, WebUIListenAddr: "192.168.1.50:23324", WebUILANNoPassword: true}
	service := NewService(&cfg, buildinfo.Info{}, nil, nil, nil)
	password := "a-long-unique-password123"
	if err := service.SetWebUIPassword(password); err != nil {
		t.Fatal(err)
	}
	if cfg.WebUILANNoPassword || cfg.WebUIPasswordFile != filepath.Join(dir, "webui.password") {
		t.Fatalf("config was not secured: %+v", cfg)
	}
	bs, err := os.ReadFile(cfg.WebUIPasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(bs) != password {
		t.Fatal("password not saved byte-for-byte")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(cfg.WebUIPasswordFile)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0600 {
			t.Fatalf("password file mode %o", st.Mode().Perm())
		}
	}
	saved, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), password) || !strings.Contains(string(saved), "webui_password_file") {
		t.Fatal("config persisted password in cleartext or omitted path")
	}
	if strings.Contains(string(saved), `"webui_lan_no_password": true`) {
		t.Fatal("unauthenticated LAN remains enabled in config")
	}
	if err := service.SetWebUIPassword("another-unique-password"); err == nil {
		t.Fatal("second bootstrap should not replace existing password")
	}
	bs, _ = os.ReadFile(cfg.WebUIPasswordFile)
	if string(bs) != password {
		t.Fatal("second setup changed password")
	}
}

func TestWebUIPasswordBootstrapRejectsUnsafeInputs(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "client.json"), WebUILANNoPassword: true}
	service := NewService(&cfg, buildinfo.Info{}, nil, nil, nil)
	for _, secret := range []string{"short", " leading-and-long-password", "a-good-password\n", strings.Repeat("a", 257)} {
		if err := service.SetWebUIPassword(secret); err == nil {
			t.Fatalf("accepted unsafe input %q", secret)
		}
	}
	if cfg.WebUIPasswordFile != "" {
		t.Fatal("invalid input changed configuration")
	}
	path := filepath.Join(filepath.Dir(cfg.ConfigPath), "webui.password")
	if err := os.WriteFile(path, []byte("preexisting-data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.SetWebUIPassword("my-long-safe-password"); err == nil {
		t.Fatal("existing file replaced")
	}
	bs, _ := os.ReadFile(path)
	if string(bs) != "preexisting-data" {
		t.Fatal("existing file overwritten")
	}
}

func TestWebUIPasswordRotationKeepsPrivateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.json")
	cfg := config.Config{ConfigPath: path, DownloadDir: dir, WebUILANNoPassword: true}
	svc := NewService(&cfg, buildinfo.Info{}, nil, nil, nil)
	if err := svc.SetWebUIPassword("first-password-123456"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangeWebUIPassword("second-password-123456"); err != nil {
		t.Fatal(err)
	}
	if cfg.WebUIPasswordFile != filepath.Join(dir, "webui.password") {
		t.Fatal("password file path was lost")
	}
	b, err := os.ReadFile(cfg.WebUIPasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "second-password-123456" {
		t.Fatal("password was not replaced")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(cfg.WebUIPasswordFile)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe file mode: %v %v", info, err)
		}
	}
	cfgBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfgBytes), "password-123456") {
		t.Fatal("password leaked into config")
	}
	if err := svc.ChangeWebUIPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	b, _ = os.ReadFile(cfg.WebUIPasswordFile)
	if string(b) != "second-password-123456" {
		t.Fatal("invalid password changed stored file")
	}
	if err := os.Remove(cfg.WebUIPasswordFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "target"), cfg.WebUIPasswordFile); err == nil {
		if err := svc.ChangeWebUIPassword("a-third-safe-password"); err == nil {
			t.Fatal("symlink accepted")
		}
	}
}
