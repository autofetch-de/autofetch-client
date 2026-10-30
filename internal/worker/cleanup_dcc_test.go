package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupPreservesRecentDCCPartialAndDeletesExpired(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "book.bin.part")
	if err := os.WriteFile(part, []byte("checkpoint"), 0600); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"kind": "dcc_resume", "version": 1, "source_sha256": "123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0", "expected_bytes": 120, "checkpoint_bytes": 10, "checkpoint_sha256": "123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0"}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(part+".meta.json", b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CleanupPartialsTTL(dir, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("recent DCC partial was removed: %v", err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(part, old, old); err != nil {
		t.Fatal(err)
	}
	if err := CleanupPartialsTTL(dir, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("expired partial survived: %v", err)
	}
}
