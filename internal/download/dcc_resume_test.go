package download

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func dccResumeFixture(t *testing.T, filename string, size int64) (string, XDCCOptions, *dccOffer) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), filename)
	opt := XDCCOptions{Host: "irc.example.net", Port: 6697, TLS: true, DownloadMethod: "xdcc_numeric", Bot: "packbot", Channel: "#files", Package: 42, ExpectedFilename: filename, ResumeTimeout: 100 * time.Millisecond}
	offer := &dccOffer{Filename: filename, IP: net.ParseIP("127.0.0.1"), Port: 12345, Size: size}
	return dest, opt, offer
}

func seedDCCPartial(t *testing.T, dest string, opt XDCCOptions, offer *dccOffer, data []byte) {
	t.Helper()
	res, err := receiveDCCStream(context.Background(), newMemoryConn(data), dest, NewProgress(offer.Size), 0, 0,
		&dccPartialMeta{Kind: "dcc_resume", Version: 1, SourceSHA256: dccSourceFingerprint(opt, offer), ExpectedBytes: offer.Size})
	if res != nil || !errors.Is(err, ErrXDCCTransferIncomplete) {
		t.Fatalf("seed partial: res=%v err=%v", res, err)
	}
}

func TestDCCResumePartialMetadataAndSource(t *testing.T) {
	dest, opt, offer := dccResumeFixture(t, "book.bin", 10)
	seedDCCPartial(t, dest, opt, offer, []byte("prefix"))
	offset, _, err := prepareDCCPartial(dest, opt, offer)
	if err != nil || offset != 6 {
		t.Fatalf("expected resumable 6 bytes: %d, %v", offset, err)
	}
	// The same filename from a different bot must not be concatenated.
	different := opt
	different.Bot = "otherbot"
	offset, _, err = prepareDCCPartial(dest, different, offer)
	if err != nil || offset != 0 {
		t.Fatalf("different bot should force restart: %d %v", offset, err)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatalf("stale partial not quarantined: %v", err)
	}
	matches, _ := filepath.Glob(dest + ".part.unverified-*")
	if len(matches) == 0 {
		t.Fatal("missing preserved old partial")
	}
}

func TestDCCResumeRejectsTamperedAndChangedSize(t *testing.T) {
	dest, opt, offer := dccResumeFixture(t, "document.bin", 10)
	seedDCCPartial(t, dest, opt, offer, []byte("partial"))
	if err := os.WriteFile(dest+".part", []byte("hacked!"), 0600); err != nil {
		t.Fatal(err)
	}
	offset, _, err := prepareDCCPartial(dest, opt, offer)
	if err != nil || offset != 0 {
		t.Fatalf("tampered partial must not resume: %d, %v", offset, err)
	}
	seedDCCPartial(t, dest, opt, offer, []byte("prefix"))
	changed := *offer
	changed.Size++
	offset, _, err = prepareDCCPartial(dest, opt, &changed)
	if err != nil || offset != 0 {
		t.Fatalf("different size must not resume: %d, %v", offset, err)
	}
}

func TestDCCResumePreservesOldUnverifiedPartial(t *testing.T) {
	dest, opt, offer := dccResumeFixture(t, "legacy.bin", 100)
	if err := os.WriteFile(dest+".part", []byte("older client"), 0600); err != nil {
		t.Fatal(err)
	}
	offset, _, err := prepareDCCPartial(dest, opt, offer)
	if err != nil || offset != 0 {
		t.Fatalf("unexpected legacy resume %d %v", offset, err)
	}
	matches, _ := filepath.Glob(dest + ".part.unverified-*")
	if len(matches) != 1 {
		t.Fatalf("legacy partial not preserved: %v", matches)
	}
	b, _ := os.ReadFile(matches[0])
	if string(b) != "older client" {
		t.Fatalf("legacy content corrupted: %q", b)
	}
}

func TestDCCResumeTruncatesUncheckpointedTail(t *testing.T) {
	dest, opt, offer := dccResumeFixture(t, "tail.bin", 20)
	seedDCCPartial(t, dest, opt, offer, []byte("good"))
	f, err := os.OpenFile(dest+".part", os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte("garbage"))
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	offset, _, err := prepareDCCPartial(dest, opt, offer)
	if err != nil || offset != 4 {
		t.Fatalf("expected original checkpoint, got %d %v", offset, err)
	}
	b, err := os.ReadFile(dest + ".part")
	if err != nil || string(b) != "good" {
		t.Fatalf("tail wasn't truncated: %q %v", b, err)
	}
}

func TestDCCAcceptParsing(t *testing.T) {
	for _, tc := range []struct {
		msg, name string
		port      int
		offset    int64
	}{
		{`\x01DCC ACCEPT "with spaces.rar" 42001 5000000\x01`, "with spaces.rar", 42001, 5000000},
		{`\x01DCC ACCEPT simple.rar 42001 120\x01`, "simple.rar", 42001, 120},
	} {
		name, p, n, err := parseDCCAccept(strings.ReplaceAll(tc.msg, `\x01`, "\x01"))
		if err != nil || name != tc.name || p != tc.port || n != tc.offset {
			t.Fatalf("parse %q: %q %d %d %v", tc.msg, name, p, n, err)
		}
	}
	for _, bad := range []string{"\x01DCC ACCEPT a 1234 -1\x01", "\x01DCC ACCEPT a 0 1\x01", "\x01DCC SEND a 42 1\x01"} {
		if _, _, _, err := parseDCCAccept(bad); err == nil {
			t.Fatalf("accepted malformed: %q", bad)
		}
	}
}

func TestNegotiateDCCResumeRequiresExactBotNickFilePortOffset(t *testing.T) {
	good := ":packbot!u@host PRIVMSG actualNick :\x01DCC ACCEPT \"file name.bin\" 12345 5\x01\r\n"
	cases := []struct {
		name, answer    string
		wantOK, wantErr bool
	}{
		{"correct", good, true, false},
		{"wrong bot ignored", ":intruder!u@h PRIVMSG actualNick :\x01DCC ACCEPT \"file name.bin\" 12345 5\x01\r\n", false, false},
		{"wrong recipient ignored", ":packbot!u@h PRIVMSG otherNick :\x01DCC ACCEPT \"file name.bin\" 12345 5\x01\r\n", false, false},
		{"wrong offset rejected", strings.Replace(good, "12345 5", "12345 6", 1), false, true},
		{"wrong filename rejected", strings.Replace(good, "file name.bin", "other.bin", 1), false, true},
		{"wrong port rejected", strings.Replace(good, "12345", "12346", 1), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli, srv := net.Pipe()
			defer cli.Close()
			defer srv.Close()
			done := make(chan error, 1)
			go func() {
				rw := bufio.NewReader(srv)
				line, err := rw.ReadString('\n')
				if err != nil {
					done <- err
					return
				}
				if !strings.Contains(line, "DCC RESUME \"file name.bin\" 12345 5") {
					done <- fmt.Errorf("invalid resume request %q", line)
					return
				}
				if tc.answer != "" {
					_, err = io.WriteString(srv, tc.answer)
				}
				done <- err
			}()
			opt := &dccOffer{Filename: "file name.bin", Port: 12345, Size: 10}
			rw := bufio.NewReadWriter(bufio.NewReader(cli), bufio.NewWriter(cli))
			ok, err := negotiateDCCResume(context.Background(), cli, rw, "packbot", "actualNick", opt, 5, 25*time.Millisecond)
			if ok != tc.wantOK || (err != nil) != tc.wantErr {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			<-done
		})
	}
}

func TestDCCResumeLiveSimulatedBot(t *testing.T) {
	for _, method := range []string{"xdcc_numeric", "irc_command"} {
		for _, allowResume := range []bool{true, false} {
			t.Run(fmt.Sprintf("method=%s/accept=%v", method, allowResume), func(t *testing.T) {
				data := []byte("beginning and ending")
				offset := int64(10)
				dest, opt, offer := dccResumeFixture(t, "payload.bin", int64(len(data)))
				if method == "irc_command" {
					opt.DownloadMethod = "irc_command"
					opt.Package = 0
					opt.SourceID = "source-123"
					opt.CommandTargetKind = "channel"
					opt.CommandTarget = "#files"
					opt.DownloadCommand = "!packbot payload.bin"
				}
				seedDCCPartial(t, dest, opt, offer, data[:offset])
				ln, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer ln.Close()
				offer.Port = ln.Addr().(*net.TCPAddr).Port
				// A changed SEND TCP port is normal on each retry. The source proof
				// intentionally does not include the ephemeral DCC port.
				dccDone := make(chan error, 1)
				go func() {
					conn, err := ln.Accept()
					if err != nil {
						dccDone <- err
						return
					}
					defer conn.Close()
					send := data
					if allowResume {
						send = data[offset:]
					}
					if _, err := conn.Write(send); err != nil {
						dccDone <- err
						return
					}
					var ack [4]byte
					_, err = io.ReadFull(conn, ack[:])
					if err == nil {
						expected := len(data)
						if !allowResume {
							expected = len(data)
						}
						if int(binary.BigEndian.Uint32(ack[:])) != expected {
							err = fmt.Errorf("bad ack offset: %d", binary.BigEndian.Uint32(ack[:]))
						}
					}
					dccDone <- err
				}()
				ircCli, ircBot := net.Pipe()
				defer ircCli.Close()
				defer ircBot.Close()
				go func() {
					line, _ := bufio.NewReader(ircBot).ReadString('\n')
					if !strings.Contains(line, "DCC RESUME") {
						return
					}
					if allowResume {
						fmt.Fprintf(ircBot, ":packbot!u@host PRIVMSG Nick :\x01DCC ACCEPT payload.bin %d %d\x01\r\n", offer.Port, offset)
					}
				}()
				prog := NewProgress(int64(len(data)))
				res, err := dccDownload(context.Background(), bufio.NewReadWriter(bufio.NewReader(ircCli), bufio.NewWriter(ircCli)), ircCli, opt, "Nick", offer, dest, prog, 2*time.Second, 0)
				if err != nil {
					t.Fatalf("download error: %v", err)
				}
				if res.Bytes != int64(len(data)) {
					t.Fatalf("size %d", res.Bytes)
				}
				got, err := os.ReadFile(dest)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("result %q err=%v", got, err)
				}
				assertDownloadMode(t, dest, 0644)
				if err := <-dccDone; err != nil {
					t.Fatalf("dcc server: %v", err)
				}
				if _, err := os.Stat(dccMetaPath(dest)); !os.IsNotExist(err) {
					t.Fatalf("meta not removed: %v", err)
				}
			})
		}
	}
}
