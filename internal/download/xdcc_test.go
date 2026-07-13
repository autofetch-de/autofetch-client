package download

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type memoryConn struct {
	reader *bytes.Reader
	writes bytes.Buffer
}

func newMemoryConn(data []byte) *memoryConn            { return &memoryConn{reader: bytes.NewReader(data)} }
func (c *memoryConn) Read(p []byte) (int, error)       { return c.reader.Read(p) }
func (c *memoryConn) Write(p []byte) (int, error)      { return c.writes.Write(p) }
func (c *memoryConn) Close() error                     { return nil }
func (c *memoryConn) LocalAddr() net.Addr              { return dummyAddr("local") }
func (c *memoryConn) RemoteAddr() net.Addr             { return dummyAddr("remote") }
func (c *memoryConn) SetDeadline(time.Time) error      { return nil }
func (c *memoryConn) SetReadDeadline(time.Time) error  { return nil }
func (c *memoryConn) SetWriteDeadline(time.Time) error { return nil }

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }

func TestReceiveDCCFileRejectsPrematureEOF(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dest := filepath.Join(dir, "file.bin")
	conn := newMemoryConn([]byte("partial"))
	prog := NewProgress(100)

	res, err := receiveDCCFile(context.Background(), conn, dest, prog, 0)
	if res != nil {
		t.Fatalf("expected no result, got %#v", res)
	}
	if !errors.Is(err, ErrXDCCTransferIncomplete) {
		t.Fatalf("expected ErrXDCCTransferIncomplete, got %v", err)
	}
	var incomplete *XDCCTransferIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("expected XDCCTransferIncompleteError, got %T", err)
	}
	if incomplete.Received != 7 || incomplete.Expected != 100 {
		t.Fatalf("unexpected sizes: received=%d expected=%d", incomplete.Received, incomplete.Expected)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("final file must not exist, stat err=%v", statErr)
	}
	part, readErr := os.ReadFile(dest + ".part")
	if readErr != nil {
		t.Fatalf("partial file missing: %v", readErr)
	}
	if string(part) != "partial" {
		t.Fatalf("unexpected partial content %q", part)
	}
	if got := prog.Downloaded.Load(); got != 7 {
		t.Fatalf("unexpected progress %d", got)
	}
	if conn.writes.Len() != 4 {
		t.Fatalf("expected one four-byte ACK, got %d bytes", conn.writes.Len())
	}
}

func TestReceiveDCCFileCompletesOnlyAtExpectedSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dest := filepath.Join(dir, "file.bin")
	payload := []byte("complete")
	conn := newMemoryConn(payload)
	prog := NewProgress(int64(len(payload)))

	res, err := receiveDCCFile(context.Background(), conn, dest, prog, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || res.Bytes != int64(len(payload)) {
		t.Fatalf("unexpected result %#v", res)
	}
	got, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("final file missing: %v", readErr)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("unexpected final content %q", got)
	}
	if _, statErr := os.Stat(dest + ".part"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial file should have been renamed, stat err=%v", statErr)
	}
}

func TestMemoryConnReadEndsWithEOF(t *testing.T) {
	// Guard the test double behavior used by the transfer tests.
	c := newMemoryConn([]byte("x"))
	buf := make([]byte, 1)
	if n, err := c.Read(buf); n != 1 || err != nil {
		t.Fatalf("first read: n=%d err=%v", n, err)
	}
	if n, err := c.Read(buf); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("second read: n=%d err=%v", n, err)
	}
}

func TestParseIRCGLineWithRetryDuration(t *testing.T) {
	t.Parallel()
	line := "ERROR :Closing Link: nick[host] (Banned (G-Lined): You have recieved a\x034 1day G:Line \x03for Repeatedly not following the rules. (Not idling in #zw-chat))"

	err := parseIRCGLine(line)
	if err == nil {
		t.Fatal("expected G-Line error")
	}
	if !errors.Is(err, ErrIRCGLine) {
		t.Fatalf("expected ErrIRCGLine, got %v", err)
	}
	var gline *IRCGLineError
	if !errors.As(err, &gline) {
		t.Fatalf("expected IRCGLineError, got %T", err)
	}
	if gline.RetryAfter != 24*time.Hour {
		t.Fatalf("unexpected retry duration: %s", gline.RetryAfter)
	}
	if !strings.Contains(gline.Message, "Not idling in #zw-chat") {
		t.Fatalf("unexpected message: %q", gline.Message)
	}
}

func TestParseIRCGLineIgnoresUnrelatedError(t *testing.T) {
	t.Parallel()
	if err := parseIRCGLine("ERROR :Closing Link: Ping timeout"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
