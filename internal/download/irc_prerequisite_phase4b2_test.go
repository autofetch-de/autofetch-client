package download

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
)

func TestNumericXDCCPrerequisitesValidatedAndOrdered(t *testing.T) {
	inst := api.DownloadInstruction{Mode: "xdcc", IRC: &api.IRCXDCCInfo{
		Host: "irc.abandoned-irc.net", Port: 6697, TLS: true, Channel: "#zombie-warez", Bot: "Zombie-Ferel", Package: 191,
		PrerequisiteChannels: []string{"#zw-chat"},
	}, Output: &api.InstructionOutput{FilenameVideo: "sample.tar"}}
	parsed, err := ParseDownloadInstruction(inst, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := parsed.IRC.PrerequisiteChannels
	if len(got) != 2 || got[0] != "#zw-chat" || got[1] != "#zombie-warez" {
		t.Fatalf("join order: %#v", got)
	}
	inst.IRC.PrerequisiteChannels = []string{"#zw-chat\r\nPRIVMSG #evil :spam"}
	if _, err := ParseDownloadInstruction(inst, nil); err == nil {
		t.Fatal("numeric XDCC accepted unsafe channel")
	}
}

func TestRequiredJoinsBeforeDownloadCommand(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(remote)
		first, err := reader.ReadString('\n')
		if err != nil {
			done <- err
			return
		}
		second, err := reader.ReadString('\n')
		if err != nil {
			done <- err
			return
		}
		if first != "JOIN #zw-chat\r\n" || second != "JOIN #zombie-warez\r\n" {
			done <- context.Canceled
			return
		}
		_, err = remote.Write([]byte(":nick!u@h JOIN :#zw-chat\r\n:nick!u@h JOIN :#zombie-warez\r\n"))
		done <- err
	}()
	rw := bufio.NewReadWriter(bufio.NewReader(local), bufio.NewWriter(local))
	if err := waitForChannelJoins(ctx, local, rw, func(s string) error { return writeIRCLine(rw, s) }, "nick", []string{"#zw-chat", "#zombie-warez"}, 2*time.Second, "Bot"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestIRCControlDuringDCCRepliesToPingAndRejectsRequiredKick(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	dccLocal, dccRemote := net.Pipe()
	defer dccLocal.Close()
	defer dccRemote.Close()
	rw := bufio.NewReadWriter(bufio.NewReader(local), bufio.NewWriter(local))
	stop := startDCCIRCWatch(context.Background(), local, rw, dccLocal, "nick", []string{"#zw-chat", "#zombie-warez"})
	errCh := make(chan error, 1)
	go func() {
		_, err := remote.Write([]byte("PING :keepalive\r\n"))
		if err != nil {
			errCh <- err
			return
		}
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		reply, err := bufio.NewReader(remote).ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		if !strings.HasPrefix(reply, "PONG :keepalive") {
			errCh <- context.Canceled
			return
		}
		_, err = remote.Write([]byte(":server KICK #zw-chat nick :required-channel\r\n"))
		errCh <- err
	}()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	_ = dccLocal.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 1)
	_, err := dccLocal.Read(b)
	if err == nil {
		t.Fatal("DCC transfer did not stop when required channel was kicked")
	}
	if err := stop(); err == nil || !strings.Contains(err.Error(), "irc_required_channel_lost") {
		t.Fatalf("expected required channel loss, got %v", err)
	}
}
