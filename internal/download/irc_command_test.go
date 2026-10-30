package download

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
)

const testSource = "11111111-2222-3333-4444-555555555555"

func sampleIRCCommand() api.DownloadInstruction {
	return api.DownloadInstruction{
		Mode: "xdcc", DownloadMethod: "irc_command", SourceID: testSource,
		IRC: &api.IRCXDCCInfo{
			Host: "irc.example.org", Port: 6697, TLS: true,
			Channel: "#ebooks", Bot: "BotA", Filename: " file name.epub",
			CommandTargetKind: "channel", CommandTarget: "#ebooks",
			DownloadCommand: "!BotA  file name.epub",
		},
		Output: &api.InstructionOutput{FilenameVideo: "file name.epub"},
	}
}

func TestIRCCommandSchemaValidAndExact(t *testing.T) {
	i := sampleIRCCommand()
	got, err := ParseDownloadInstruction(i, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.IRC.Package != 0 || got.IRC.DownloadMethod != "irc_command" || got.IRC.Filename != " file name.epub" || !got.IRC.TLS {
		t.Fatalf("invalid parsed command: %+v", got.IRC)
	}
	opt := XDCCOptions{Bot: got.IRC.Bot, CommandTarget: got.IRC.CommandTarget, DownloadCommand: got.IRC.DownloadCommand, DownloadMethod: got.IRC.DownloadMethod}
	if req := ircRequestLine(opt); req != "PRIVMSG #ebooks :!BotA  file name.epub" {
		t.Fatalf("bad request: %q", req)
	}
	if req := ircRequestLine(XDCCOptions{Bot: "BotA", Package: 42}); req != "PRIVMSG BotA :XDCC SEND 42" {
		t.Fatalf("legacy request changed: %q", req)
	}
}

func TestIRCCommandRejectsForgedSourceAndCommands(t *testing.T) {
	cases := map[string]func(*api.DownloadInstruction){
		"missing source":    func(i *api.DownloadInstruction) { i.SourceID = "" },
		"no host":           func(i *api.DownloadInstruction) { i.IRC.Host = ""; i.IRC.Network = "ircs://fallback.example" },
		"no port":           func(i *api.DownloadInstruction) { i.IRC.Port = 0 },
		"bot mismatch":      func(i *api.DownloadInstruction) { i.IRC.Bot = "Other" },
		"filename mismatch": func(i *api.DownloadInstruction) { i.IRC.DownloadCommand = "!BotA other.epub" },
		"target mismatch":   func(i *api.DownloadInstruction) { i.IRC.CommandTarget = "#different" },
		"private target unsupported": func(i *api.DownloadInstruction) {
			i.IRC.CommandTargetKind = "bot_private"
			i.IRC.CommandTarget = "BotA"
		},
		"injected newline":           func(i *api.DownloadInstruction) { i.IRC.DownloadCommand += "\r\nPRIVMSG #other :spam" },
		"unknown method":             func(i *api.DownloadInstruction) { i.DownloadMethod = "irc_new" },
		"path traversal output":      func(i *api.DownloadInstruction) { i.Output.Dir = "../../private" },
		"windows absolute output":    func(i *api.DownloadInstruction) { i.Output.Dir = `C:\Windows` },
		"path separator output name": func(i *api.DownloadInstruction) { i.Output.FilenameVideo = "../elsewhere" },
		"bad prerequisite channel":   func(i *api.DownloadInstruction) { i.IRC.PrerequisiteChannels = []string{"#ebooks\r\nJOIN #other"} },
		"numeric pack in command":    func(i *api.DownloadInstruction) { i.IRC.Package = 42 },
		"invalid filename":           func(i *api.DownloadInstruction) { i.IRC.Filename = "../test"; i.IRC.DownloadCommand = "!BotA ../test" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			i := sampleIRCCommand()
			mutate(&i)
			if _, err := ParseDownloadInstruction(i, nil); err == nil {
				t.Fatal("unsafe input was accepted")
			}
		})
	}
}

func TestIRCCommandAcceptsOnlySelectedBotOffer(t *testing.T) {
	expected := ":BotA!user@host PRIVMSG mynick :\x01DCC SEND \"file.epub\" 2130706433 1234 2\x01"
	if !isSelectedDCCOffer(expected, "bota", "mynick") {
		t.Fatal("correct bot offer rejected")
	}
	for _, offered := range []string{
		":Other!user@host PRIVMSG mynick :\x01DCC SEND \"file.epub\" 2130706433 1234 2\x01",
		":BotA!user@host PRIVMSG someoneelse :\x01DCC SEND \"file.epub\" 2130706433 1234 2\x01",
		":BotA!user@host NOTICE mynick :\x01DCC SEND \"file.epub\" 2130706433 1234 2\x01",
	} {
		if isSelectedDCCOffer(offered, "BotA", "mynick") {
			t.Fatalf("forged offer accepted: %q", offered)
		}
	}
}

// The fixture exercises the real IRC session through a local IRC socket and
// a separate DCC TCP transfer. No public IRC server or content network is used.
func TestIRCCommandEndToEndWithMockBot(t *testing.T) {
	ircListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ircListener.Close()
	dccListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dccListener.Close()
	dccPort := dccListener.Addr().(*net.TCPAddr).Port
	done := make(chan error, 2)
	go func() {
		c, e := dccListener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		if _, e := c.Write([]byte("data")); e != nil {
			done <- e
			return
		}
		var ack [4]byte
		_, e = io.ReadFull(c, ack[:])
		done <- e
	}()
	go func() {
		c, e := ircListener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(4 * time.Second))
		in := bufio.NewReader(c)
		sentWelcome := false
		for {
			line, e := in.ReadString('\n')
			if e != nil {
				done <- e
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "USER ") && !sentWelcome {
				sentWelcome = true
				fmt.Fprint(c, ":srv 001 mynick :welcome\r\n")
			}
			if line == "JOIN #ebooks" {
				fmt.Fprint(c, ":mynick!u@h JOIN :#ebooks\r\n")
			}
			if strings.HasPrefix(line, "PRIVMSG ") {
				if line != "PRIVMSG #ebooks :!BotA file.epub" {
					done <- fmt.Errorf("unexpected request %q", line)
					return
				}
				fmt.Fprintf(c, ":Other!u@h PRIVMSG mynick :\x01DCC SEND \"file.epub\" 2130706433 %d 4\x01\r\n", dccPort)
				fmt.Fprintf(c, ":BotA!u@h PRIVMSG mynick :\x01DCC SEND \"file.epub\" 2130706433 %d 4\x01\r\n", dccPort)
				done <- nil
				return
			}
		}
	}()
	dest := filepath.Join(t.TempDir(), "file.epub")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	res, _, err := DownloadXDCCToFile(ctx, XDCCOptions{
		Nick: "mynick", Host: "127.0.0.1", Port: ircListener.Addr().(*net.TCPAddr).Port, TLS: false,
		Channel: "#ebooks", Bot: "BotA", DownloadMethod: "irc_command", SourceID: testSource,
		CommandTargetKind: "channel", CommandTarget: "#ebooks", DownloadCommand: "!BotA file.epub",
		ExpectedFilename: "file.epub", IRCHandshakeTimeout: 2 * time.Second, OfferTimeout: 2 * time.Second,
		AllowNonPublicDCC: true,
	}, dest, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != 4 {
		t.Fatalf("bad size %d", res.Bytes)
	}
	b, e := os.ReadFile(dest)
	if e != nil || string(b) != "data" {
		t.Fatalf("bad content %q error %v", b, e)
	}
	for i := 0; i < 2; i++ {
		if e := <-done; e != nil {
			t.Fatalf("mock failed: %v", e)
		}
	}
}

// A TLS-designated source must never quietly retry on a plaintext socket.
func TestIRCCommandNeverDowngradesTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	observed := make(chan []byte, 1)
	go func() {
		c, e := ln.Accept()
		if e != nil {
			observed <- nil
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 512)
		n, _ := c.Read(buf)
		// Reply with plaintext IRC: TLS negotiation must fail.
		_, _ = c.Write([]byte(":srv 001 mynick :hello\r\n"))
		observed <- append([]byte(nil), buf[:n]...)
	}()
	path := filepath.Join(t.TempDir(), "file.epub")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err = DownloadXDCCToFile(ctx, XDCCOptions{Nick: "mynick", Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, TLS: true,
		Bot: "BotA", Channel: "#ebooks", DownloadMethod: "irc_command", SourceID: testSource,
		CommandTargetKind: "channel", CommandTarget: "#ebooks", DownloadCommand: "!BotA file.epub", ExpectedFilename: "file.epub",
		IRCHandshakeTimeout: time.Second}, path, 0, nil)
	if err == nil {
		t.Fatal("expected TLS handshake failure")
	}
	bytes := <-observed
	if strings.Contains(string(bytes), "PRIVMSG") || strings.Contains(string(bytes), "!BotA") {
		t.Fatal("command sent over plaintext channel")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatalf("download unexpectedly exists: %v", e)
	}
}
