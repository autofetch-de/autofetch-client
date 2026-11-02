package download

// Explicit, user-initiated NickServ enrollment. This deliberately has no
// integration with the download/lease path: a 477 never creates an account.

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type NickServAction string

const (
	NickServRegister NickServAction = "register"
	NickServConfirm  NickServAction = "confirm"
	NickServVerify   NickServAction = "verify"
)

type NickServRequest struct {
	Host     string
	Port     int
	Nick     string
	Password string
	Email    string
	Code     string
	Action   NickServAction
	// OnProgress receives bounded, sanitized NickServ activity during an explicit action.
	// It is never invoked with IRC commands, passwords, email addresses or codes.
	OnProgress func(NickServProgress)
}

// NickServProgress is an optional UI-only progress notification.
type NickServProgress struct {
	Kind        string // connecting, connected, ready, sent, notice, waiting, retry, identified
	Message     string // only set for a sanitized NickServ notice
	WaitSeconds int    // displayed countdown; client decides when to update it
}

func (r NickServRequest) report(kind, message string, seconds int) {
	if r.OnProgress != nil {
		r.OnProgress(NickServProgress{Kind: kind, Message: message, WaitSeconds: seconds})
	}
}

type NickServResult struct {
	Identified bool // Only set after a positive NickServ identification acknowledgement.
	Pending    bool // Registration request accepted; further action may be necessary.
}

// NewNickServPassword generates a credential without relying on the timestamp.
func NewNickServPassword() (string, error) { return generateRegistrationPassword() }

func validIRCAtom(value string) bool {
	return len(value) > 0 && len(value) <= 128 && !strings.ContainsAny(value, " \t\r\n\x00:")
}

// ManualNickServ never joins a channel, sends a file request, or retries a
// registration. It only uses strict CA-verified TLS, including a locally
// opted-in legacy CN exception when that certificate is CA-verified.
func ManualNickServ(ctx context.Context, req NickServRequest) (NickServResult, error) {
	if !validIRCAtom(req.Nick) || !validIRCAtom(req.Password) || strings.ContainsAny(req.Host, " \r\n\t\x00") || req.Host == "" || req.Port < 1 || req.Port > 65535 {
		return NickServResult{}, errors.New("invalid NickServ configuration")
	}
	if req.Action == NickServRegister {
		address, err := mail.ParseAddress(strings.TrimSpace(req.Email))
		if err != nil || address.Address != strings.TrimSpace(req.Email) || !validIRCAtom(req.Email) {
			return NickServResult{}, errors.New("valid registration email required")
		}
	} else if req.Action == NickServConfirm {
		if !validIRCAtom(req.Code) {
			return NickServResult{}, errors.New("invalid confirmation code")
		}
	} else if req.Action != NickServVerify {
		return NickServResult{}, errors.New("invalid NickServ action")
	}
	// Only the explicit registration action needs a longer-lived connection.
	// Verification and confirmation retain their previous short timeout.
	timeout := 22 * time.Second
	connectionTimeout := 21 * time.Second
	if req.Action == NickServRegister {
		timeout = 80 * time.Second
		connectionTimeout = 79 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req.report("connecting", "", 0)
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 8 * time.Second}, Config: ircTLSConfig(req.Host)}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(req.Host, strconv.Itoa(req.Port)))
	if err != nil {
		return NickServResult{}, fmt.Errorf("secure IRC connection failed: %w", err)
	}
	defer conn.Close()
	req.report("connected", "", 0)
	stop := closeConnOnContext(ctx, conn)
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(connectionTimeout))
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	return manualNickServExchangeContext(ctx, rw, req)
}

// Kept separate from the network dialer so the response handling can be tested
// with a deterministic IRC stream. The reader runs independently to keep
// answering IRC PINGs during network-mandated NickServ dwell time.
func manualNickServExchange(rw *bufio.ReadWriter, req NickServRequest) (NickServResult, error) {
	return manualNickServExchangeContext(context.Background(), rw, req)
}

type nickServLine struct {
	text string
	err  error
}

func manualNickServExchangeContext(ctx context.Context, rw *bufio.ReadWriter, req NickServRequest) (NickServResult, error) {
	reading, stopReading := context.WithCancel(ctx)
	defer stopReading()
	lines := make(chan nickServLine)
	go func() {
		for {
			text, err := rw.ReadString('\n')
			select {
			case lines <- nickServLine{text: text, err: err}:
			case <-reading.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	read := func() (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case item := <-lines:
			return item.text, item.err
		}
	}

	if err := writeIRCLine(rw, "NICK "+req.Nick); err != nil {
		return NickServResult{}, err
	}
	if err := writeIRCLine(rw, "USER autofetch 0 * :autofetch manual registration"); err != nil {
		return NickServResult{}, err
	}
	nickReadyAt := time.Time{}
	for nickReadyAt.IsZero() {
		line, err := read()
		if err != nil {
			return NickServResult{}, fmt.Errorf("IRC registration handshake failed: %w", err)
		}
		if strings.HasPrefix(line, "PING ") {
			_ = writeIRCLine(rw, "PONG "+strings.TrimSpace(strings.TrimPrefix(line, "PING ")))
			continue
		}
		_, command, _ := parseIRCLine(line)
		switch command {
		case "001":
			nickReadyAt = time.Now()
			req.report("ready", "", 0)
		case "433", "436", "437":
			return NickServResult{}, errors.New("nickname is not available; choose another nick")
		case "ERROR":
			return NickServResult{}, errors.New("IRC network rejected connection")
		}
	}
	command := "IDENTIFY " + req.Password
	switch req.Action {
	case NickServRegister:
		command = "REGISTER " + req.Password + " " + req.Email
	case NickServConfirm:
		command = "CONFIRM " + req.Code
	}
	if err := writeIRCPrivmsg(rw, "NickServ", command); err != nil {
		return NickServResult{}, err
	}
	req.report("sent", "", 0)
	result := NickServResult{}
	identifiedRequested := req.Action == NickServVerify
	lastNickServNotice := ""
	var retryTimer *time.Timer
	var retryAt <-chan time.Time
	retriedAfterDwell := false
	defer func() {
		if retryTimer != nil {
			retryTimer.Stop()
		}
	}()
	for {
		// A 30-second registration hold applies to the *current IRC session*,
		// not wall-clock time between HTTP requests. Keep the same connection
		// open and perform at most one retry within this explicitly requested
		// registration, without generating another nick or password.
		select {
		case <-ctx.Done():
			return result, unconfirmedNickServError(lastNickServNotice)
		case <-retryAt:
			retryAt = nil
			retriedAfterDwell = true
			if err := writeIRCPrivmsg(rw, "NickServ", command); err != nil {
				return result, fmt.Errorf("NickServ retry on the existing IRC connection failed: %w", err)
			}
			req.report("retry", "", 0)
			continue
		case item := <-lines:
			line, err := item.text, item.err
			if err != nil {
				if result.Pending {
					return result, nil
				}
				return result, unconfirmedNickServError(lastNickServNotice)
			}
			if strings.HasPrefix(line, "PING ") {
				_ = writeIRCLine(rw, "PONG "+strings.TrimSpace(strings.TrimPrefix(line, "PING ")))
				continue
			}
			if !isNickServSource(line) {
				continue
			}
			body := strings.ToLower(nickServText(line))
			lastNickServNotice = redactedNickServNotice(body, req)
			if lastNickServNotice != "" {
				req.report("notice", lastNickServNotice, 0)
			}
			if req.Action == NickServVerify && (strings.Contains(body, "isn't registered") ||
				strings.Contains(body, "is not registered") || strings.Contains(body, "not registered")) {
				return result, errors.New("NickServ confirms this nick is not registered. The saved credentials have not been changed; do not use Verify until registration succeeds.")
			}
			if req.Action == NickServRegister {
				if wait, ok := nickServRegisterWait(line); ok &&
					(strings.Contains(body, "using this nick for at least") ||
						(strings.Contains(body, "wait") && (strings.Contains(body, "before registering") || strings.Contains(body, "to register")))) {
					if retriedAfterDwell {
						return result, fmt.Errorf("NickServ still requires a waiting period after one retry. The existing nick and password are unchanged. Notice: %s", lastNickServNotice)
					}
					if wait > 65*time.Second {
						return result, fmt.Errorf("NickServ requires more than 65 seconds of connection time, exceeding this registration session limit. Notice: %s", lastNickServNotice)
					}
					if retryAt == nil {
						// 001 is the latest safe lower bound for when the
						// server accepted our nick. Add a small margin.
						remaining := time.Until(nickReadyAt.Add(wait + 2*time.Second))
						if remaining < time.Second {
							remaining = time.Second
						}
						retryTimer = time.NewTimer(remaining)
						retryAt = retryTimer.C
						req.report("waiting", "", int(remaining.Round(time.Second)/time.Second))
					}
					continue
				}
			}
			if isNickServIdentifySuccess(line) || body == "+r" {
				req.report("identified", "", 0)
				return NickServResult{Identified: true}, nil
			}
			if isNickServIdentifyFailure(line) || isNickServRegisterFailure(line) && !isNickServAlreadyRegistered(line) {
				return result, fmt.Errorf("NickServ rejected the request. Notice: %s", lastNickServNotice)
			}
			if isNickServAlreadyRegistered(line) && req.Action == NickServRegister {
				return result, errors.New("nick is already registered; use existing NickServ credentials")
			}
			if req.Action == NickServRegister && isNickServRegisterSuccess(line) {
				result.Pending = true
				if !identifiedRequested {
					if err := writeIRCPrivmsg(rw, "NickServ", "IDENTIFY "+req.Password); err != nil {
						return result, err
					}
					identifiedRequested = true
				}
				continue
			}
			if req.Action == NickServConfirm && (strings.Contains(body, "confirmed") || strings.Contains(body, "success") || strings.Contains(body, "verified") || strings.Contains(body, "activated")) {
				result.Pending = true
				if !identifiedRequested {
					if err := writeIRCPrivmsg(rw, "NickServ", "IDENTIFY "+req.Password); err != nil {
						return result, err
					}
					identifiedRequested = true
				}
				continue
			}
			if strings.Contains(body, "email") || strings.Contains(body, "confirmation") || strings.Contains(body, "activate") || strings.Contains(body, "verify") || strings.Contains(body, "code") {
				result.Pending = true
				if req.Action == NickServRegister || req.Action == NickServConfirm {
					return result, nil
				}
			}
		}
	}
}

var nickServNoticeURL = regexp.MustCompile(`(?i)https?://[^\s]+`)
var nickServNoticeEmail = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
var nickServNoticeCode = regexp.MustCompile(`(?i)\b(code|token|password|passcode|key)\s*(?:is\s+|[:=]\s*)([^\s]+)`)
var nickServNoticeConfirm = regexp.MustCompile(`(?i)\b(confirm|verify|activate)\s+([a-z0-9_\-]{6,})\b`)

// Only show a bounded, sanitized NickServ notice. Never disclose credentials,
// registration email, or activation code, even if the server echoes them.
func redactedNickServNotice(body string, req NickServRequest) string {
	body = strings.ToLower(body)
	for _, secret := range []string{req.Password, req.Email, req.Code} {
		if secret != "" {
			body = strings.ReplaceAll(body, strings.ToLower(secret), "[redacted]")
		}
	}
	// Also hide server-generated confirmation URLs/codes and unrelated emails.
	// Unlike credentials supplied by the user, these cannot be known in advance.
	body = nickServNoticeURL.ReplaceAllString(body, "[link redacted]")
	body = nickServNoticeEmail.ReplaceAllString(body, "[email redacted]")
	body = nickServNoticeCode.ReplaceAllString(body, "$1 [redacted]")
	body = nickServNoticeConfirm.ReplaceAllString(body, "$1 [redacted]")
	clean := make([]rune, 0, len(body))
	for _, char := range body {
		if len(clean) >= 240 {
			break
		}
		if unicode.IsControl(char) {
			clean = append(clean, ' ')
		} else {
			clean = append(clean, char)
		}
	}
	return strings.TrimSpace(string(clean))
}

func unconfirmedNickServError(lastNotice string) error {
	const guidance = "NickServ registration outcome unconfirmed. Do not register again; return to IRC setup and use Verify for the saved nick."
	if lastNotice == "" {
		return errors.New(guidance + " No NickServ reply was recognized.")
	}
	return fmt.Errorf("%s Last NickServ notice: %s", guidance, lastNotice)
}
