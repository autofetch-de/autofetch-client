package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func assertDownloadMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != want {
		t.Fatalf("%s: got %04o, want %04o", path, st.Mode().Perm(), want)
	}
}

func TestDCCCompletedAndInterruptedFileModes(t *testing.T) {
	dir := t.TempDir()
	unfinished := filepath.Join(dir, "unfinished.mkv")
	if _, err := receiveDCCFile(context.Background(), newMemoryConn([]byte("partial")), unfinished, NewProgress(100), 0); err == nil {
		t.Fatal("premature EOF incorrectly accepted")
	}
	assertDownloadMode(t, unfinished+".part", 0600)
	if _, err := os.Lstat(unfinished); !os.IsNotExist(err) {
		t.Fatalf("unfinished target published: %v", err)
	}

	completed := filepath.Join(dir, "completed.mkv")
	if _, err := receiveDCCFile(context.Background(), newMemoryConn([]byte("done")), completed, NewProgress(4), 0); err != nil {
		t.Fatal(err)
	}
	assertDownloadMode(t, completed, 0644)
	if _, err := os.Lstat(completed + ".part"); !os.IsNotExist(err) {
		t.Fatalf("part still present after completed download: %v", err)
	}
}

func TestDCCRecoveredCompletePartialPublishesReadable(t *testing.T) {
	data := []byte("ready for recovery")
	dest, opt, offer := dccResumeFixture(t, "recovered.mkv", int64(len(data)))
	if err := os.WriteFile(dest+".part", data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	meta := dccPartialMeta{
		Kind: "dcc_resume", Version: 1,
		SourceSHA256:  dccSourceFingerprint(opt, offer),
		ExpectedBytes: int64(len(data)), Checkpoint: int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]),
	}
	if err := writeDCCMeta(dccMetaPath(dest), meta); err != nil {
		t.Fatal(err)
	}
	assertDownloadMode(t, dest+".part", 0600)
	assertDownloadMode(t, dccMetaPath(dest), 0600)

	res, err := dccDownload(context.Background(), nil, nil, opt, "", offer, dest, NewProgress(int64(len(data))), 0, 0)
	if err != nil || res == nil || res.Bytes != int64(len(data)) {
		t.Fatalf("recovered complete .part: %+v %v", res, err)
	}
	assertDownloadMode(t, dest, 0644)
	if _, err := os.Lstat(dccMetaPath(dest)); !os.IsNotExist(err) {
		t.Fatalf("private sidecar should be removed: %v", err)
	}
}

func TestHTTPAndSubtitleFileModes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("sample payload"))
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "film.mkv")
	if _, _, err := DownloadToFile(context.Background(), server.Client(), server.URL, path, int64(len("sample payload")), 0, nil); err != nil {
		t.Fatal(err)
	}
	assertDownloadMode(t, path, 0644)
	subtitle := filepath.Join(dir, "sub.srt")
	if err := DownloadText(context.Background(), server.Client(), server.URL, subtitle); err != nil {
		t.Fatal(err)
	}
	assertDownloadMode(t, subtitle, 0644)
}
