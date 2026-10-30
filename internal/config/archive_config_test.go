package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveOptInPersistsAndDefaultsOff(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "client.json")
	var existing Config
	existing.ConfigPath = p
	if err := loadFromFile(&existing, p); err != nil {
		t.Fatal(err)
	}
	if existing.ExtractArchives {
		t.Fatal("archive extraction enabled by default")
	}
	cfg := Config{ConfigPath: p, ServerBaseURL: "https://autofetch.de", ClientName: "test", DownloadDir: dir, ExtractArchives: true, ExtractNestedDepth: 2, ExtractLayout: "same", ExtractDeleteOriginal: true}
	if err := Persist(cfg); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm()&0077 != 0 {
		t.Fatalf("config permissions: %v %v", fi, err)
	}
	var reloaded Config
	reloaded.ConfigPath = p
	if err := loadFromFile(&reloaded, p); err != nil {
		t.Fatal(err)
	}
	if !reloaded.ExtractArchives || reloaded.ExtractNestedDepth != 2 || reloaded.ExtractLayout != "same" || !reloaded.ExtractDeleteOriginal {
		t.Fatalf("archive settings lost: enabled=%v nested=%d", reloaded.ExtractArchives, reloaded.ExtractNestedDepth)
	}
}
