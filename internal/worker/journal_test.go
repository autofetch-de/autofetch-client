package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJournalPersistsAndVerifiesCompletedTransfer(t *testing.T) {
	root := t.TempDir()
	journal := DownloadJournal{Dir: filepath.Join(root, "journal")}
	target := filepath.Join(root, "download.rar")
	if err := os.WriteFile(target, []byte("original data"), 0600); err != nil {
		t.Fatal(err)
	}
	size, hash, err := hashLocalFile(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	rec := DownloadReceipt{Version: 1, ClientID: "client-a", JobID: "job-1", InstructionHash: "fpr-a", TargetPath: target, Phase: "transferred", Bytes: size, SHA256: hash, UpdatedAt: time.Now().UTC()}
	if err := journal.Write(rec); err != nil {
		t.Fatal(err)
	}
	got, exists, err := journal.Load("client-a", "job-1")
	if err != nil || !exists {
		t.Fatalf("load %v %v", exists, err)
	}
	if verified, err := journal.VerifyCompleted(context.Background(), got, "fpr-a", target); err != nil || !verified {
		t.Fatalf("verify=%v err=%v", verified, err)
	}
	if _, exists, err := journal.Load("other-client", "job-1"); err != nil || exists {
		t.Fatalf("client isolation: %v %v", exists, err)
	}
	if _, err := journal.VerifyCompleted(context.Background(), got, "fpr-b", target); err == nil {
		t.Fatal("wrong source accepted")
	}
	if _, err := journal.VerifyCompleted(context.Background(), got, "fpr-a", filepath.Join(root, "other.rar")); err == nil {
		t.Fatal("wrong target accepted")
	}
	if err := os.WriteFile(target, []byte("tampered data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.VerifyCompleted(context.Background(), got, "fpr-a", target); err == nil {
		t.Fatal("tampered contents accepted")
	}
	path, err := journal.filename("client-a", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("receipt permissions: %v %v", info, err)
	}
}

func TestJournalInvalidOrInterruptedState(t *testing.T) {
	root := t.TempDir()
	journal := DownloadJournal{Dir: filepath.Join(root, "journal")}
	rec := DownloadReceipt{Version: 1, ClientID: "a", JobID: "b", InstructionHash: "fp", TargetPath: filepath.Join(root, "target"), Phase: "downloading"}
	if err := journal.Write(rec); err != nil {
		t.Fatal(err)
	}
	got, found, err := journal.Load("a", "b")
	if err != nil || !found {
		t.Fatal(err)
	}
	if verified, err := journal.VerifyCompleted(context.Background(), got, "fp", rec.TargetPath); err != nil || verified {
		t.Fatalf("unfinished record: %v %v", verified, err)
	}
	p, _ := journal.filename("a", "b")
	if err := os.WriteFile(p, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Load("a", "b"); err == nil {
		t.Fatal("corrupt receipt accepted")
	}
}

func TestSafeDownloadPath(t *testing.T) {
	root := t.TempDir()
	p, err := safeDownloadPath(root, "ebooks/fiction", " file.rar")
	if err != nil || p != filepath.Join(root, "ebooks", "fiction", " file.rar") {
		t.Fatalf("path=%q error=%v", p, err)
	}
	for _, tt := range []struct{ dir, file string }{
		{"../escape", "a.rar"}, {"/tmp", "x"}, {"ebooks", "../x"}, {"ebooks", "/etc/passwd"}, {"ebooks", "a/b"}, {"ebooks", "a\\b"}, {"..", "x"}, {"ebooks", ""}, {"ebooks", "C:\\boot.ini"},
	} {
		if _, err := safeDownloadPath(root, tt.dir, tt.file); err == nil {
			t.Errorf("unsafe path accepted: dir=%q file=%q", tt.dir, tt.file)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "redirect")); err == nil {
		if _, err := safeDownloadPath(root, "redirect", "a.rar"); err == nil {
			t.Fatal("symlink accepted")
		}
	}
}

func TestInstructionFingerprintDoesNotExposeURL(t *testing.T) {
	s := "https://example.com/file?secret=sensitive-value"
	fpr, err := instructionFingerprint(map[string]string{"url": s})
	if err != nil || len(fpr) != 64 || strings.Contains(fpr, "sensitive-value") {
		t.Fatalf("fingerprint failed: %q %v", fpr, err)
	}
}

func TestPostprocessedReceiptPreventsRedownloadAfterCleanup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "archive.zip")
	data := []byte("downloaded")
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	sum := hex.EncodeToString(digest[:])
	j := DownloadJournal{Dir: filepath.Join(dir, "journal")}
	if err := j.BeginAttempt("client", "job", "fingerprint", target, "attempt"); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkTransferred("client", "job", "fingerprint", target, int64(len(data)), sum); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(dir, "archive.zip.extracted")
	if err := os.Mkdir(result, 0700); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkPostProcessed("client", "job", target, sum, result); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	receipt, found, err := j.Load("client", "job")
	if err != nil || !found || receipt.Phase != "postprocessed" {
		t.Fatalf("receipt %v %v %#v", found, err, receipt)
	}
	verified, err := j.VerifyCompleted(context.Background(), receipt, "fingerprint", target)
	if err != nil || !verified {
		t.Fatalf("re-lease should not redownload %v %v", verified, err)
	}
	if err := os.RemoveAll(result); err != nil {
		t.Fatal(err)
	}
	if ok, err := j.VerifyCompleted(context.Background(), receipt, "fingerprint", target); ok || err == nil {
		t.Fatal("missing output silently accepted")
	}
}

func TestJournalPostprocessedSingleFileAfterOriginalRemoved(t *testing.T) {
	root := t.TempDir()
	journal := DownloadJournal{Dir: filepath.Join(root, "journal")}
	source := filepath.Join(root, "film.rar")
	dest := filepath.Join(root, "film.mkv")
	if err := os.WriteFile(source, []byte("archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	size, hash, err := hashLocalFile(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	rec := DownloadReceipt{Version: 1, ClientID: "c", JobID: "j", InstructionHash: "fingerprint", TargetPath: source, Phase: "transferred", Bytes: size, SHA256: hash}
	if err := journal.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := journal.MarkPostProcessed("c", "j", source, hash, dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := journal.Load("c", "j")
	if !ok || err != nil || rec.ArchiveDestinationKind != "file" {
		t.Fatalf("receipt: %+v %v", rec, err)
	}
	verified, err := journal.VerifyCompleted(context.Background(), rec, "fingerprint", source)
	if err != nil || !verified {
		t.Fatalf("file should satisfy previously completed download: %v %v", verified, err)
	}
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	if ok, err := journal.VerifyCompleted(context.Background(), rec, "fingerprint", source); ok || err == nil {
		t.Fatal("missing output silently accepted")
	}
}
