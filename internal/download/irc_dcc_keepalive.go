package download

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

// The IRC connection MUST stay joined while DCC is transferring. Without a
// reader responding to PINGs, servers may disconnect the session and evict
// it from prerequisite channels during a large DCC transfer.
// Start this only AFTER DCC RESUME/ACCEPT and reverse DCC negotiation have
// completed: those stages use the same IRC reader synchronously.
func startDCCIRCWatch(ctx context.Context, ircConn net.Conn, rw *bufio.ReadWriter, dccConn net.Conn, nick string, channels []string) func() error {
	watchCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	result := make(chan error, 1)
	required := make(map[string]bool)
	for _, channel := range channels {
		channel = strings.ToLower(strings.TrimSpace(channel))
		if channel != "" {
			required[channel] = true
		}
	}
	go func() {
		var lost error
		defer func() { result <- lost; close(done) }()
		fail := func(err error) {
			lost = err
			_ = dccConn.Close()
		}
		for {
			if watchCtx.Err() != nil {
				return
			}
			_ = ircConn.SetReadDeadline(time.Now().Add(2 * time.Second))
			line, err := rw.ReadString('\n')
			if err != nil {
				if watchCtx.Err() != nil {
					return
				}
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				// A plain DCC transfer can survive a lost IRC connection, but
				// channels with explicit idle requirements must fail closed.
				if len(required) > 0 {
					fail(fmt.Errorf("irc_required_channel_lost: %w", err))
				}
				return
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "PING ") {
				if err := writeIRCLine(rw, "PONG "+strings.TrimPrefix(line, "PING ")); err != nil {
					if watchCtx.Err() == nil && len(required) > 0 {
						fail(fmt.Errorf("irc_required_channel_lost: pong_failed: %w", err))
					}
					return
				}
				continue
			}
			_, command, _ := parseIRCLine(line)
			if command == "KICK" && kickedFromRequiredChannel(line, nick, required) {
				log.Printf("IRC required channel kicked during DCC: %s", line)
				fail(fmt.Errorf("irc_required_channel_lost: kicked_from_prerequisite"))
				return
			}
			if command == "ERROR" && len(required) > 0 {
				fail(fmt.Errorf("irc_required_channel_lost: server_disconnected"))
				return
			}
		}
	}()
	return func() error {
		cancel()
		_ = ircConn.SetReadDeadline(time.Now()) // Unblock IRC read promptly.
		<-done
		return <-result
	}
}

func kickedFromRequiredChannel(line, nick string, required map[string]bool) bool {
	// IRC syntax: [:server] KICK #channel nick :reason
	fields := strings.Fields(line)
	for i := 0; i+2 < len(fields); i++ {
		if strings.EqualFold(fields[i], "KICK") && strings.EqualFold(fields[i+2], nick) {
			return required[strings.ToLower(fields[i+1])]
		}
	}
	return false
}
