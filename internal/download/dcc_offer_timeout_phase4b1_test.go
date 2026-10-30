package download

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitingForDCCOfferTimeoutIsNotNickServTimeout(t *testing.T) {
	c := newMemoryConn(nil)
	rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
	_, err := readIRCLineForPhase(context.Background(), c, rw, time.Now().Add(-time.Second), "waiting_for_offer", nil)
	if !errors.Is(err, ErrDCCOfferTimeout) {
		t.Fatalf("wrong timeout: %v", err)
	}
	if strings.Contains(err.Error(), "nickserv") {
		t.Fatalf("misleading timeout: %v", err)
	}
}
func TestCancelRejectedXDCCOfferOnlyForNumericBot(t *testing.T) {
	var lines []string
	write := func(s string) error { lines = append(lines, s); return nil }
	if err := cancelRejectedXDCCOffer(XDCCOptions{Bot: "TestBot", DownloadMethod: "xdcc_numeric"}, write); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "PRIVMSG TestBot :XDCC CANCEL" {
		t.Fatalf("unexpected cancellation: %v", lines)
	}
	if err := cancelRejectedXDCCOffer(XDCCOptions{Bot: "TestBot", DownloadMethod: "irc_command"}, write); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("irc_command must not send numeric CANCEL: %v", lines)
	}
}
