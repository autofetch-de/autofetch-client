package worker

import (
	"github.com/autofetch-de/autofetch-client/internal/api"
	"github.com/autofetch-de/autofetch-client/internal/archive"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveQueueRequiresServerPolicy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "queue")
	r := Runner{ClientID: "c", ArchiveQueueDir: dir}
	disabled := &api.LeasedJob{JobID: "j"}
	r.enqueueArchive(disabled, "file.zip", "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("legacy local policy created work: %v", err)
	}
	enabled := &api.LeasedJob{JobID: "j", Payload: api.JobPayload{ArchiveProcessing: &api.ArchiveProcessing{Enabled: true, Layout: "same", DeleteOriginal: true, NestedDepth: 2}}}
	r.enqueueArchive(enabled, "file.rar", "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	q := &archive.Queue{Dir: dir, OnlyPlanned: true}
	items, err := q.History("c", 100)
	if err != nil || len(items) != 1 || !items[0].PolicySnapshot || !items[0].DeleteOriginal || items[0].NestedDepth != 2 {
		t.Fatalf("bad server snapshot: %+v %v", items, err)
	}
}
