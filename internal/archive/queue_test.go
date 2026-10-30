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

func TestQueueRestartAndCompletion(t *testing.T) {
	src := zipFixture(t, "data.zip", entry{name: "hello.txt", content: "hi"})
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	dir := filepath.Join(t.TempDir(), "queue")
	q := &Queue{Dir: dir}
	if err := q.Enqueue("client", "job", src, digest); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue("client", "job", src, digest); err != nil {
		t.Fatal("idempotency", err)
	}
	q2 := &Queue{Dir: dir} // simulate a process restart
	ok, err := q2.ProcessOne(context.Background(), "other-client", Limits{})
	if err != nil || ok {
		t.Fatalf("other client unexpectedly processed: %v %v", ok, err)
	}
	ok, err = q2.ProcessOne(context.Background(), "client", Limits{})
	if err != nil || !ok {
		t.Fatalf("not processed: %v %v", ok, err)
	}
	hist, err := q2.History("client", 100)
	if err != nil || len(hist) != 1 || hist[0].Status != "completed" {
		t.Fatalf("history %+v %v", hist, err)
	}
	if hist[0].Phase != "completed" || hist[0].ProgressBytes != 2 || hist[0].ProgressTotalBytes != 2 {
		t.Fatalf("progress was not persisted: %+v", hist[0])
	}
	ok, err = q2.ProcessOne(context.Background(), "client", Limits{})
	if err != nil || ok {
		t.Fatalf("reprocessed completed item: %v %v", ok, err)
	}
	if err := q2.Enqueue("client", "job", src, digest); err != nil {
		t.Fatal(err)
	}
	if err := q2.Enqueue("client", "job", src, strings.Repeat("0", 64)); err == nil {
		t.Fatal("source changed was accepted")
	}
}
func TestQueueFailureDoesNotDeleteDownloadOrRetryAutomatically(t *testing.T) {
	src := zipFixture(t, "bad.zip", entry{name: "../../escape", content: "bad"})
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	q := &Queue{Dir: filepath.Join(t.TempDir(), "queue")}
	if err := q.Enqueue("client", "job", src, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	ok, err := q.ProcessOne(context.Background(), "client", Limits{})
	if err == nil || !ok {
		t.Fatalf("unexpected extraction success: %v %v", ok, err)
	}
	hist, err := q.History("client", 100)
	if err != nil || len(hist) != 1 || hist[0].Status != "failed" {
		t.Fatalf("wrong failure state: %+v %v", hist, err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("original archive missing", err)
	}
	ok, err = q.ProcessOne(context.Background(), "client", Limits{})
	if err != nil || ok {
		t.Fatalf("unwanted repeat: %v %v", ok, err)
	}
}
func TestQueueRecoversInterruptedExtraction(t *testing.T) {
	src := zipFixture(t, "good.zip", entry{name: "a", content: "hello"})
	b, _ := os.ReadFile(src)
	sum := sha256.Sum256(b)
	q := &Queue{Dir: filepath.Join(t.TempDir(), "queue")}
	if err := q.Enqueue("client", "job", src, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	path, _ := q.itemPath("client", "job")
	it, _, _ := q.load(path)
	it.Status = "extracting"
	if err := q.save(path, it); err != nil {
		t.Fatal(err)
	}
	ok, err := q.ProcessOne(context.Background(), "client", Limits{})
	if err != nil || !ok {
		t.Fatalf("failed to resume extraction: %v %v", ok, err)
	}
}
