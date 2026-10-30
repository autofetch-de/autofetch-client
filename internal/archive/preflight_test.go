package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveOutputEstimateAndProgress(t *testing.T) {
	const payload = "hello safely extracted"
	cases := []struct {
		name   string
		create func(*testing.T) string
	}{
		{"zip", func(t *testing.T) string {
			return zipFixture(t, "sample.zip", entry{name: "file.txt", content: payload})
		}},
		{"tar", func(t *testing.T) string {
			return tarFixture(t, "sample.tar", entry{name: "file.txt", content: payload})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.create(t)
			estimated, err := estimateOutputBytes(context.Background(), source, Limits{})
			if err != nil || estimated != int64(len(payload)) {
				t.Fatalf("estimate %d: %v", estimated, err)
			}
			raw, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			var events []Progress
			dest, err := ExtractVerifiedNestedProgress(context.Background(), source, hex.EncodeToString(digest[:]), Limits{}, 0, func(p Progress) { events = append(events, p) })
			if err != nil {
				t.Fatal(err)
			}
			written, err := os.ReadFile(filepath.Join(dest, "file.txt"))
			if err != nil || string(written) != payload {
				t.Fatalf("content %q: %v", written, err)
			}
			var extraction, publishing bool
			for _, event := range events {
				if event.Phase == "extracting" && event.Bytes == int64(len(payload)) && event.TotalBytes == estimated {
					extraction = true
				}
				if event.Phase == "publishing" {
					publishing = true
				}
			}
			if !extraction || !publishing {
				t.Fatalf("missing progress: %#v", events)
			}
		})
	}
}

func TestPreflightRejectsInsufficientSpaceBeforeCreatingStaging(t *testing.T) {
	dir := t.TempDir()
	if err := checkExtractionSpace(dir, 1<<60); err == nil || !strings.Contains(err.Error(), "archive_insufficient_disk_space") {
		t.Fatalf("unrealistic space estimate not rejected: %v", err)
	}
	if err := checkExtractionSpace(dir, 0); err != nil {
		t.Fatalf("empty extraction should fit: %v", err)
	}
}

func TestPreflightRejectsOversizedArchiveFromMetadata(t *testing.T) {
	source := tarFixture(t, "small.tar", entry{name: "big.txt", content: "0123456789"})
	_, err := estimateOutputBytes(context.Background(), source, Limits{MaxTotalBytes: 5})
	if err == nil || !strings.Contains(err.Error(), "archive_uncompressed_limit") {
		t.Fatalf("unexpected preflight result %v", err)
	}
}
