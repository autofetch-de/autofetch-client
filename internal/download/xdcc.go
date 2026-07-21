package download

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	internalirc "github.com/autofetch-de/autofetch-client/internal/irc"
)

var ErrXDCCFilenameMismatch = errors.New("xdcc_filename_mismatch")
var ErrFilenameMismatch = ErrXDCCFilenameMismatch
var ErrIRCRegisteredNickRequired = errors.New("irc_registered_nick_required")
var ErrReverseDCCTimeout = errors.New("reverse_dcc_timeout")
var ErrReverseDCCDisabled = errors.New("reverse_dcc_disabled")
var ErrXDCCTransferIncomplete = errors.New("xdcc_transfer_incomplete")
var ErrIRCGLine = errors.New("irc_gline")

func closeConnOnContext(ctx context.Context, conn net.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

type XDCCTransferIncompleteError struct {
	Received int64
	Expected int64
}

func (e *XDCCTransferIncompleteError) Error() string {
	return fmt.Sprintf("xdcc_transfer_incomplete: received=%d expected=%d", e.Received, e.Expected)
}

func (e *XDCCTransferIncompleteError) Unwrap() error { return ErrXDCCTransferIncomplete }

type IRCGLineError struct {
	Message    string
	RetryAfter time.Duration
}

func (e *IRCGLineError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("irc_gline: retry_after=%s message=%q", e.RetryAfter, e.Message)
	}
	return fmt.Sprintf("irc_gline: message=%q", e.Message)
}

func (e *IRCGLineError) Unwrap() error { return ErrIRCGLine }

func parseIRCGLine(line string) error {
	clean := strings.TrimSpace(stripIRCFormatting(line))
	lower := strings.ToLower(clean)
	if !strings.Contains(lower, "g-lined") && !strings.Contains(lower, "g:line") &&
		!(strings.Contains(lower, "banned") && strings.Contains(lower, "closing link")) {
		return nil
	}

	retryAfter := time.Duration(0)
	for _, token := range strings.Fields(lower) {
		token = strings.Trim(token, "()[]{}.,;:!?")
		var amount int
		switch {
		case strings.HasSuffix(token, "days"):
			if _, err := fmt.Sscanf(token, "%ddays", &amount); err == nil && amount > 0 {
				retryAfter = time.Duration(amount) * 24 * time.Hour
			}
		case strings.HasSuffix(token, "day"):
			if _, err := fmt.Sscanf(token, "%dday", &amount); err == nil && amount > 0 {
				retryAfter = time.Duration(amount) * 24 * time.Hour
			}
		case strings.HasSuffix(token, "hours"):
			if _, err := fmt.Sscanf(token, "%dhours", &amount); err == nil && amount > 0 {
				retryAfter = time.Duration(amount) * time.Hour
			}
		case strings.HasSuffix(token, "hour"):
			if _, err := fmt.Sscanf(token, "%dhour", &amount); err == nil && amount > 0 {
				retryAfter = time.Duration(amount) * time.Hour
			}
		}
		if retryAfter > 0 {
			break
		}
	}

	return &IRCGLineError{Message: clean, RetryAfter: retryAfter}
}

var publicIPCache struct {
	sync.Mutex
	ip      net.IP
	fetched time.Time
}

func ircDebugf(format string, args ...any) {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AUTOFETCH_IRC_DEBUG")))
	if v == "1" || v == "true" || v == "yes" || v == "debug" {
		log.Printf(format, args...)
	}
}

type XDCCFilenameMismatchError struct {
	Expected string
	Offered  string
}

func (e *XDCCFilenameMismatchError) Error() string {
	return fmt.Sprintf("xdcc_filename_mismatch: expected=%q offered=%q", e.Expected, e.Offered)
}

func (e *XDCCFilenameMismatchError) Unwrap() error { return ErrXDCCFilenameMismatch }

func OfferedFilename(err error) string {
	var e *XDCCFilenameMismatchError
	if errors.As(err, &e) {
		return e.Offered
	}
	return ""
}

type XDCCOptions struct {
	Nick    string
	Host    string
	Port    int
	TLS     bool
	Network string

	JoinChannels []string
	Channel      string
	Bot          string
	Package      int

	ExpectedFilename string
	Username         string
	Realname         string

	NickServEnabled  bool
	NickServPassword string
	NickServCommand  string

	SASLEnabled  bool
	SASLUsername string
	SASLPassword string

	AutoRegister         bool
	RegistrationEmail    string
	RegistrationPassword string

	OnNickSelected func(string)
	OnRegistered   func(nick, password string)

	IRCHandshakeTimeout time.Duration
	OfferTimeout        time.Duration
	DCCConnectTimeout   time.Duration

	// Optional reverse/passive DCC settings. These can also be supplied via
	// AUTOFETCH_DCC_PUBLIC_IP, AUTOFETCH_DCC_LISTEN_HOST,
	// AUTOFETCH_DCC_PORT_MIN and AUTOFETCH_DCC_PORT_MAX.
	ReverseDCCEnabled bool
	DCCPublicIP       string
	DCCListenHost     string
	DCCPortMin        int
	DCCPortMax        int
}

func DownloadXDCCToFile(ctx context.Context, opt XDCCOptions, destPath string, bytesPerSec int64, prog *Progress) (*Result, *Progress, error) {
	opt.Nick = internalirc.SanitizeNick(strings.TrimSpace(opt.Nick))
	if opt.Nick == "" {
		opt.Nick = "guest"
	}
	opt.Username = strings.TrimSpace(opt.Username)
	if opt.Username == "" {
		opt.Username = opt.Nick
	}
	opt.Realname = strings.TrimSpace(opt.Realname)
	if opt.Realname == "" {
		opt.Realname = "client"
	}
	opt.Host = strings.TrimSpace(opt.Host)
	opt.Network = strings.TrimSpace(opt.Network)
	opt.Channel = strings.TrimSpace(opt.Channel)
	opt.Bot = strings.TrimSpace(opt.Bot)
	opt.ExpectedFilename = strings.TrimSpace(opt.ExpectedFilename)
	opt.RegistrationEmail = strings.TrimSpace(opt.RegistrationEmail)
	opt.RegistrationPassword = strings.TrimSpace(opt.RegistrationPassword)
	if len(opt.JoinChannels) == 0 && opt.Channel != "" {
		opt.JoinChannels = []string{opt.Channel}
	}
	if opt.Channel != "" {
		found := false
		for _, ch := range opt.JoinChannels {
			if strings.EqualFold(strings.TrimSpace(ch), strings.TrimSpace(opt.Channel)) {
				found = true
				break
			}
		}
		if !found {
			opt.JoinChannels = append(opt.JoinChannels, opt.Channel)
		}
	}
	for i := range opt.JoinChannels {
		opt.JoinChannels[i] = internalirc.NormalizeChannel(strings.TrimSpace(opt.JoinChannels[i]))
	}
	opt.Channel = internalirc.NormalizeChannel(opt.Channel)
	if (opt.Host == "" && opt.Network == "") || opt.Bot == "" || opt.ExpectedFilename == "" || opt.Package <= 0 {
		return nil, prog, fmt.Errorf("xdcc_missing_fields")
	}
	if opt.IRCHandshakeTimeout <= 0 {
		opt.IRCHandshakeTimeout = 20 * time.Second
	}
	if opt.OfferTimeout <= 0 {
		opt.OfferTimeout = 60 * time.Second
	}
	if opt.DCCConnectTimeout <= 0 {
		opt.DCCConnectTimeout = 20 * time.Second
	}
	if v := strings.TrimSpace(os.Getenv("AUTOFETCH_DCC_REVERSE_ENABLED")); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			opt.ReverseDCCEnabled = true
		case "0", "false", "no", "off":
			opt.ReverseDCCEnabled = false
		}
	}
	if strings.TrimSpace(opt.DCCPublicIP) == "" {
		opt.DCCPublicIP = strings.TrimSpace(os.Getenv("AUTOFETCH_DCC_PUBLIC_IP"))
	}
	if strings.TrimSpace(opt.DCCListenHost) == "" {
		opt.DCCListenHost = strings.TrimSpace(os.Getenv("AUTOFETCH_DCC_LISTEN_HOST"))
	}
	if opt.DCCPortMin <= 0 {
		if v := strings.TrimSpace(os.Getenv("AUTOFETCH_DCC_PORT_MIN")); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				opt.DCCPortMin = n
			}
		}
	}
	if opt.DCCPortMax <= 0 {
		if v := strings.TrimSpace(os.Getenv("AUTOFETCH_DCC_PORT_MAX")); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				opt.DCCPortMax = n
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return nil, prog, err
	}

	// Open exactly one IRC session per leased job attempt. Reconnecting several
	// times in quick succession can trigger IRC flood/reconnect protection and
	// potentially get clients temporarily banned. Retry scheduling belongs to
	// the server, which already applies a substantially longer delay.
	res, p, err := runXDCCSession(ctx, opt, destPath, bytesPerSec, prog)
	if err != nil {
		downloaded := int64(0)
		expected := int64(0)
		if p != nil {
			downloaded = p.Downloaded.Load()
			expected = p.Expected
		}
		log.Printf(
			"XDCC session failed: host=%s channel=%s bot=%s package=%d downloaded=%d expected=%d err=%v",
			opt.Host,
			opt.Channel,
			opt.Bot,
			opt.Package,
			downloaded,
			expected,
			err,
		)
		return nil, p, err
	}
	return res, p, nil
}

func isAuthRequiredMessage(line string) bool {
	l := strings.ToLower(strings.TrimSpace(line))
	return strings.Contains(l, "registered nick") ||
		strings.Contains(l, "must be identified") ||
		strings.Contains(l, "identify with nickserv") ||
		strings.Contains(l, "only identified users") ||
		strings.Contains(l, "you need to be identified") ||
		strings.Contains(l, "you are not identified") ||
		strings.Contains(l, "this channel requires") ||
		strings.Contains(l, "you must be logged in") ||
		strings.Contains(l, "identify to a registered nick") ||
		strings.Contains(l, "private message this user")
}

func markAutoRegistered(opt XDCCOptions, currentNick, autoRegisteredPassword string, autoRegisteredPersisted *bool, autoRegisterSucceeded *bool, host string) {
	if *autoRegisteredPersisted || strings.TrimSpace(autoRegisteredPassword) == "" {
		return
	}
	*autoRegisteredPersisted = true
	if !*autoRegisterSucceeded {
		*autoRegisterSucceeded = true
		log.Printf("nickserv register succeeded for %s", host)
	}
	if opt.OnRegistered != nil {
		opt.OnRegistered(currentNick, autoRegisteredPassword)
	}
}

func generateRegistrationPassword() string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#$%_-"
	buf := make([]byte, 18)
	seed := time.Now().UnixNano()
	for i := range buf {
		seed = (seed*1664525 + 1013904223) & 0x7fffffff
		buf[i] = alphabet[int(seed)%len(alphabet)]
	}
	return string(buf)
}

func joinAuthRequiredError(line string) error {
	return fmt.Errorf("%w: %s", ErrIRCRegisteredNickRequired, strings.TrimSpace(line))
}
func parseIRCLine(line string) (prefix, command, trailing string) {
	trimmed := strings.TrimSpace(line)
	rest := trimmed
	if strings.HasPrefix(rest, ":") {
		if sp := strings.IndexByte(rest, ' '); sp > 0 {
			prefix = rest[1:sp]
			rest = strings.TrimSpace(rest[sp+1:])
		}
	}
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		command = strings.ToUpper(strings.TrimSpace(rest[:sp]))
		rest = strings.TrimSpace(rest[sp+1:])
	} else {
		command = strings.ToUpper(strings.TrimSpace(rest))
		rest = ""
	}
	if idx := strings.Index(rest, " :"); idx >= 0 {
		trailing = strings.TrimSpace(rest[idx+2:])
	} else if strings.HasPrefix(rest, ":") {
		trailing = strings.TrimSpace(rest[1:])
	}
	return prefix, command, trailing
}

func ircPrefixNick(prefix string) string {
	if prefix == "" {
		return ""
	}
	nick := prefix
	if i := strings.IndexAny(nick, "!@"); i >= 0 {
		nick = nick[:i]
	}
	return strings.ToLower(strings.TrimSpace(nick))
}

func stripIRCFormatting(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 0x02 || r == 0x0F || r == 0x16 || r == 0x1D || r == 0x1F:
			continue
		case r == 0x03:
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func nickServText(line string) string {
	_, _, trailing := parseIRCLine(line)
	text := line
	if strings.TrimSpace(trailing) != "" {
		text = trailing
	}
	text = stripIRCFormatting(text)
	return strings.ToLower(strings.TrimSpace(text))
}

func isNickServSource(line string) bool {
	prefix, command, trailing := parseIRCLine(line)
	source := ircPrefixNick(prefix)
	low := strings.ToLower(strings.TrimSpace(line))
	trail := strings.ToLower(strings.TrimSpace(trailing))
	if source == "nickserv" || source == "services" || source == "service" || source == "atheme" || strings.Contains(source, "nickserv") {
		return true
	}
	if command == "NOTICE" && (strings.Contains(trail, "nickserv") || strings.Contains(low, " nickserv ")) {
		return true
	}
	return strings.Contains(low, "nickserv")
}

func isNickServRegisterSuccess(line string) bool {
	l := nickServText(line)
	if !isNickServSource(line) {
		return false
	}
	if strings.Contains(l, "isn't registered") || strings.Contains(l, "is not registered") {
		return false
	}
	return strings.Contains(l, "nickname is now registered") ||
		strings.Contains(l, "nick is now registered") ||
		strings.Contains(l, "nickname registered.") ||
		strings.Contains(l, "nick registered.") ||
		strings.Contains(l, "nickname registered ") ||
		strings.Contains(l, "nick registered ") ||
		strings.Contains(l, "nickname ") && strings.Contains(l, " registered.") ||
		strings.Contains(l, "nickname ") && strings.Contains(l, " registered ") ||
		strings.Contains(l, "nick ") && strings.Contains(l, " registered.") ||
		strings.Contains(l, "nick ") && strings.Contains(l, " registered ") ||
		strings.Contains(l, "has been registered") ||
		strings.Contains(l, "account registered") ||
		strings.Contains(l, "registration successful") ||
		strings.Contains(l, "registered successfully")
}

func isNickServAlreadyRegistered(line string) bool {
	l := nickServText(line)
	if !isNickServSource(line) {
		return false
	}
	return strings.Contains(l, "already registered") ||
		strings.Contains(l, "already exists") ||
		strings.Contains(l, "nickname is registered") ||
		strings.Contains(l, "nick is registered")
}

func isNickServIdentifyFailure(line string) bool {
	l := nickServText(line)
	if !isNickServSource(line) {
		return false
	}
	return strings.Contains(l, "incorrect password") ||
		strings.Contains(l, "password incorrect") ||
		strings.Contains(l, "invalid password") ||
		strings.Contains(l, "authentication failed") ||
		strings.Contains(l, "identify failed") ||
		strings.Contains(l, "you are not identified") ||
		strings.Contains(l, "you are not logged in")
}

func isNickServPromptToIdentify(line string) bool {
	l := nickServText(line)
	if !isNickServSource(line) {
		return false
	}
	return strings.Contains(l, "identify") ||
		strings.Contains(l, "authenticate") ||
		strings.Contains(l, "registered nick") ||
		strings.Contains(l, "this nickname is registered") ||
		strings.Contains(l, "please choose a different nick")
}

func isNickServRegisterFailure(line string) bool {
	l := nickServText(line)
	if !isNickServSource(line) {
		return false
	}
	return strings.Contains(l, "invalid email") ||
		strings.Contains(l, "registration failed") ||
		strings.Contains(l, "cannot register") ||
		strings.Contains(l, "not enough parameters") ||
		strings.Contains(l, "too many registrations") ||
		strings.Contains(l, "email address is invalid")
}

func isNickServIdentifySuccess(line string) bool {
	l := nickServText(line)
	if !isNickServSource(line) {
		return false
	}
	return strings.Contains(l, "you are now identified") ||
		strings.Contains(l, "you are now logged in") ||
		strings.Contains(l, "password accepted") ||
		strings.Contains(l, "now identified") ||
		strings.Contains(l, "successfully identified") ||
		strings.Contains(l, "successfully logged in") ||
		strings.Contains(l, "you are now logged in as")
}

func nickServRegisterWait(line string) (time.Duration, bool) {
	l := nickServText(line)
	if !isNickServSource(line) {
		return 0, false
	}
	if !strings.Contains(l, "using this nick for at least") && !strings.Contains(l, "wait") {
		return 0, false
	}
	wait := parseWaitDuration(l, 30*time.Second)
	if wait <= 0 {
		wait = 30 * time.Second
	}
	return wait, true
}

func readIRCLineWithDeadline(ctx context.Context, conn net.Conn, rw *bufio.ReadWriter, deadline time.Time) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		next := time.Until(deadline)
		if next <= 0 {
			return "", fmt.Errorf("nickserv_handshake_timeout")
		}
		chunk := 2 * time.Second
		if next < chunk {
			chunk = next
		}
		_ = conn.SetReadDeadline(time.Now().Add(chunk))
		line, err := rw.ReadString('\n')
		if err == nil {
			_ = conn.SetReadDeadline(time.Time{})
			return strings.TrimSpace(line), nil
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			continue
		}
		_ = conn.SetReadDeadline(time.Time{})
		return "", err
	}
}

func readIRCLineForPhase(ctx context.Context, conn net.Conn, rw *bufio.ReadWriter, deadline time.Time, phase string, lastMessage *string) (string, error) {
	line, err := readIRCLineWithDeadline(ctx, conn, rw, deadline)
	if err != nil {
		last := ""
		if lastMessage != nil {
			last = strings.TrimSpace(*lastMessage)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			if last != "" {
				return "", fmt.Errorf("irc_connection_closed_during_%s: last_message=%q: %w", phase, last, err)
			}
			return "", fmt.Errorf("irc_connection_closed_during_%s: %w", phase, err)
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return "", fmt.Errorf("irc_%s_timeout", phase)
		}
		return "", fmt.Errorf("irc_%s_failed: %w", phase, err)
	}
	line = strings.TrimSpace(line)
	if lastMessage != nil && line != "" {
		*lastMessage = line
	}
	return line, nil
}

func logRelevantIRCLine(phase, line, bot string) {
	prefix, command, trailing := parseIRCLine(line)
	source := ircPrefixNick(prefix)
	bot = strings.ToLower(strings.TrimSpace(bot))
	switch command {
	case "ERROR":
		log.Printf("IRC ERROR during %s: %s", phase, strings.TrimSpace(trailing))
	case "NOTICE":
		log.Printf("IRC NOTICE during %s from %s: %s", phase, source, strings.TrimSpace(trailing))
	case "PRIVMSG":
		if bot != "" && source == bot {
			log.Printf("IRC PRIVMSG during %s from %s: %s", phase, source, strings.TrimSpace(trailing))
		}
	default:
		if len(command) == 3 && command[0] >= '0' && command[0] <= '9' {
			ircDebugf("IRC numeric during %s: %s", phase, line)
		}
	}
}

func waitForChannelJoins(ctx context.Context, conn net.Conn, rw *bufio.ReadWriter, writeLine func(string) error, currentNick string, channels []string, timeout time.Duration, bot string) error {
	pending := map[string]string{}
	for _, channel := range channels {
		channel = strings.TrimSpace(channel)
		if channel == "" {
			continue
		}
		key := strings.ToLower(channel)
		if _, exists := pending[key]; exists {
			continue
		}
		log.Printf("Joining channel: %s", channel)
		if err := writeLine("JOIN " + channel); err != nil {
			return fmt.Errorf("irc_join_send_failed: channel=%s: %w", channel, err)
		}
		pending[key] = channel
	}
	if len(pending) == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	lastMessage := ""
	for len(pending) > 0 {
		line, err := readIRCLineForPhase(ctx, conn, rw, deadline, "channel_join", &lastMessage)
		if err != nil {
			return err
		}
		if glineErr := parseIRCGLine(line); glineErr != nil {
			return glineErr
		}
		if strings.HasPrefix(line, "PING ") {
			if err := writeLine("PONG " + strings.TrimPrefix(line, "PING ")); err != nil {
				return err
			}
			continue
		}
		logRelevantIRCLine("channel_join", line, bot)
		prefix, command, _ := parseIRCLine(line)
		if command == "JOIN" && strings.EqualFold(ircPrefixNick(prefix), currentNick) {
			for key, channel := range pending {
				if strings.Contains(strings.ToLower(line), strings.ToLower(channel)) {
					delete(pending, key)
					log.Printf("Joined channel: %s", channel)
				}
			}
			continue
		}
		if command == "366" {
			for key, channel := range pending {
				if strings.Contains(strings.ToLower(line), strings.ToLower(channel)) {
					delete(pending, key)
					log.Printf("Joined channel: %s", channel)
				}
			}
			continue
		}
		switch command {
		case "403", "405", "471", "473", "474", "475", "477", "486":
			return fmt.Errorf("irc_join_rejected: %s", line)
		case "ERROR":
			return fmt.Errorf("irc_error_during_channel_join: %s", line)
		}
	}
	return nil
}

func performNickServHandshake(ctx context.Context, conn net.Conn, rw *bufio.ReadWriter, host, email, password string, offerDeadline *time.Time) error {
	log.Printf("attempting nickserv register for %s", host)
	sendRegister := func() error {
		return writeIRCPrivmsg(rw, "NickServ", fmt.Sprintf("REGISTER %s %s", password, email))
	}
	if err := sendRegister(); err != nil {
		return err
	}
	handshakeDeadline := time.Now().Add(30 * time.Second)
	if offerDeadline != nil && handshakeDeadline.After(*offerDeadline) {
		*offerDeadline = handshakeDeadline
	}
	identifySent := false
	registeredAck := false
	for {
		line, err := readIRCLineWithDeadline(ctx, conn, rw, handshakeDeadline)
		if err != nil {
			return err
		}
		if strings.HasPrefix(line, "PING ") {
			_ = writeIRCLine(rw, "PONG "+strings.TrimPrefix(line, "PING "))
			continue
		}
		if isNickServSource(line) {
			ircDebugf("nickserv handshake line from %s: %s", host, nickServText(line))
		}
		switch {
		case func() bool {
			wait, ok := nickServRegisterWait(line)
			if !ok {
				return false
			}
			log.Printf("nickserv register requires dwell time on %s; waiting %s before retry", host, wait.Round(time.Second))
			if next := time.Now().Add(wait + 20*time.Second); next.After(handshakeDeadline) {
				handshakeDeadline = next
			}
			if offerDeadline != nil {
				if next := time.Now().Add(wait + 20*time.Second); next.After(*offerDeadline) {
					*offerDeadline = next
				}
			}
			select {
			case <-ctx.Done():
				return true
			case <-time.After(wait):
			}
			log.Printf("retrying nickserv register for %s", host)
			if err := sendRegister(); err != nil {
				return true
			}
			return true
		}():
			if err := ctx.Err(); err != nil {
				return err
			}
			continue
		case isNickServIdentifySuccess(line):
			if !registeredAck {
				log.Printf("nickserv identify acknowledged for %s", host)
			} else {
				log.Printf("nickserv register and identify succeeded for %s", host)
			}
			return nil
		case isNickServRegisterSuccess(line):
			registeredAck = true
			log.Printf("nickserv register acknowledged for %s", host)
			if !identifySent {
				if err := writeIRCPrivmsg(rw, "NickServ", fmt.Sprintf("IDENTIFY %s", password)); err != nil {
					return err
				}
				identifySent = true
			}
		case registeredAck && nickServText(line) == "+r":
			log.Printf("nickserv marked nick as registered/identified for %s", host)
			return nil
		case isNickServAlreadyRegistered(line):
			log.Printf("nickserv register reported existing nick for %s; attempting identify", host)
			if !identifySent {
				if err := writeIRCPrivmsg(rw, "NickServ", fmt.Sprintf("IDENTIFY %s", password)); err != nil {
					return err
				}
				identifySent = true
			}
		case isNickServIdentifyFailure(line):
			return fmt.Errorf("nickserv identify failed: %s", strings.TrimSpace(line))
		case isNickServRegisterFailure(line):
			if isNickServAlreadyRegistered(line) {
				continue
			}
			return fmt.Errorf("nickserv register failed: %s", strings.TrimSpace(line))
		case isNickServPromptToIdentify(line):
			if !identifySent {
				if err := writeIRCPrivmsg(rw, "NickServ", fmt.Sprintf("IDENTIFY %s", password)); err != nil {
					return err
				}
				identifySent = true
			}
		}
	}
}

func performNickServIdentifyHandshake(ctx context.Context, conn net.Conn, rw *bufio.ReadWriter, host, password string, offerDeadline *time.Time) error {
	log.Printf("attempting nickserv identify for %s", host)
	if err := writeIRCPrivmsg(rw, "NickServ", fmt.Sprintf("IDENTIFY %s", password)); err != nil {
		return err
	}
	handshakeDeadline := time.Now().Add(20 * time.Second)
	if offerDeadline != nil && handshakeDeadline.After(*offerDeadline) {
		*offerDeadline = handshakeDeadline
	}
	for {
		line, err := readIRCLineWithDeadline(ctx, conn, rw, handshakeDeadline)
		if err != nil {
			return err
		}
		if strings.HasPrefix(line, "PING ") {
			_ = writeIRCLine(rw, "PONG "+strings.TrimPrefix(line, "PING "))
			continue
		}
		if isNickServSource(line) {
			ircDebugf("nickserv handshake line from %s: %s", host, nickServText(line))
		}
		switch {
		case isNickServIdentifySuccess(line):
			log.Printf("nickserv identify acknowledged for %s", host)
			return nil
		case nickServText(line) == "+r":
			log.Printf("nickserv marked nick as identified for %s", host)
			return nil
		case isNickServPromptToIdentify(line):
			continue
		case isNickServIdentifyFailure(line):
			return fmt.Errorf("nickserv identify failed: %s", strings.TrimSpace(line))
		case isNickServAlreadyRegistered(line):
			continue
		}
	}
}

func writeIRCLine(rw *bufio.ReadWriter, line string) error {
	_, err := rw.WriteString(line + "\r\n")
	if err != nil {
		return err
	}
	return rw.Flush()
}

func writeIRCPrivmsg(rw *bufio.ReadWriter, target, msg string) error {
	return writeIRCLine(rw, fmt.Sprintf("PRIVMSG %s :%s", target, msg))
}

func runXDCCSession(ctx context.Context, opt XDCCOptions, destPath string, bytesPerSec int64, prog *Progress) (*Result, *Progress, error) {
	host, port, useTLS, err := resolveIRCAddress(opt)
	if err != nil {
		return nil, prog, err
	}
	log.Printf("IRC job: host=%s port=%d tls=%t channel=%s bot=%s package=%d", host, port, useTLS, opt.Channel, opt.Bot, opt.Package)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: opt.IRCHandshakeTimeout}
	var conn net.Conn
	if useTLS {
		c, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: host})
		if err != nil {
			return nil, prog, err
		}
		conn = c
	} else {
		c, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, prog, err
		}
		conn = c
	}
	defer conn.Close()
	stopConnCancel := closeConnOnContext(ctx, conn)
	defer stopConnCancel()

	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	writeLine := func(line string) error { return writeIRCLine(rw, line) }

	if opt.SASLEnabled {
		_ = writeLine("CAP REQ :sasl")
	}
	currentNick := opt.Nick
	if err := writeLine("NICK " + currentNick); err != nil {
		return nil, prog, err
	}
	if err := writeLine("USER " + opt.Username + " 0 * :" + opt.Realname); err != nil {
		return nil, prog, err
	}
	candidates := internalirc.NickCandidates(currentNick)
	nickIdx := 0
	saslRequested := false
	autoRegisterTried := false
	autoRegisterSucceeded := false
	autoRegisteredPassword := ""
	autoRegisteredPersisted := false
	handshakeTimeout := opt.IRCHandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = 30 * time.Second
	}
	handshakeDeadline := time.Now().Add(handshakeTimeout)
	lastHandshakeMessage := ""
	for {
		line, err := readIRCLineForPhase(ctx, conn, rw, handshakeDeadline, "handshake", &lastHandshakeMessage)
		if err != nil {
			return nil, prog, err
		}
		logRelevantIRCLine("handshake", line, opt.Bot)
		if glineErr := parseIRCGLine(line); glineErr != nil {
			return nil, prog, glineErr
		}
		if strings.HasPrefix(line, "PING ") {
			_ = writeLine("PONG " + strings.TrimPrefix(line, "PING "))
			continue
		}
		if strings.Contains(line, " 433 ") || strings.Contains(line, " 436 ") || strings.Contains(line, " 437 ") {
			nickIdx++
			if nickIdx >= len(candidates) {
				candidates = append(candidates, fmt.Sprintf("%s|%d", currentNick, nickIdx+1))
			}
			currentNick = internalirc.SanitizeNick(candidates[nickIdx])
			_ = writeLine("NICK " + currentNick)
			continue
		}
		if opt.SASLEnabled && !saslRequested && strings.Contains(line, " CAP ") && strings.Contains(strings.ToUpper(line), " ACK ") && strings.Contains(strings.ToLower(line), "sasl") {
			_ = writeLine("AUTHENTICATE PLAIN")
			saslRequested = true
			continue
		}
		if opt.SASLEnabled && strings.HasPrefix(line, "AUTHENTICATE +") {
			user := opt.SASLUsername
			if strings.TrimSpace(user) == "" {
				user = currentNick
			}
			payload := "\x00" + user + "\x00" + opt.SASLPassword
			_ = writeLine("AUTHENTICATE " + base64.StdEncoding.EncodeToString([]byte(payload)))
			continue
		}
		if opt.SASLEnabled && (strings.Contains(line, " 903 ") || strings.Contains(line, " 900 ")) {
			_ = writeLine("CAP END")
			continue
		}
		if opt.SASLEnabled && (strings.Contains(line, " 904 ") || strings.Contains(line, " 905 ") || strings.Contains(line, " 906 ") || strings.Contains(line, " 907 ")) {
			_ = writeLine("CAP END")
			return nil, prog, fmt.Errorf("sasl_auth_failed")
		}
		if strings.Contains(line, " 001 ") {
			break
		}
	}
	if opt.OnNickSelected != nil {
		opt.OnNickSelected(currentNick)
	}
	if opt.NickServEnabled && strings.TrimSpace(opt.NickServPassword) != "" && !opt.SASLEnabled {
		cmd := strings.TrimSpace(opt.NickServCommand)
		if cmd == "" || strings.EqualFold(cmd, "IDENTIFY") {
			if err := performNickServIdentifyHandshake(ctx, conn, rw, host, opt.NickServPassword, nil); err != nil {
				return nil, prog, err
			}
			log.Printf("initial nickserv identify succeeded for %s", host)
		} else {
			select {
			case <-ctx.Done():
				return nil, prog, ctx.Err()
			case <-time.After(2 * time.Second):
			}
			_ = writeLine(fmt.Sprintf("PRIVMSG NickServ :%s %s", cmd, opt.NickServPassword))
		}
	}
	joinChannels := append([]string(nil), opt.JoinChannels...)
	if strings.TrimSpace(opt.Channel) != "" {
		joinChannels = append(joinChannels, opt.Channel)
	}
	if err := waitForChannelJoins(ctx, conn, rw, writeLine, currentNick, joinChannels, opt.IRCHandshakeTimeout, opt.Bot); err != nil {
		return nil, prog, err
	}
	if err := writeLine(fmt.Sprintf("PRIVMSG %s :XDCC SEND %d", opt.Bot, opt.Package)); err != nil {
		return nil, prog, fmt.Errorf("xdcc_request_send_failed: %w", err)
	}
	log.Printf("XDCC request sent: bot=%s package=%d", opt.Bot, opt.Package)
	offerTimeout := opt.OfferTimeout
	if offerTimeout <= 0 {
		offerTimeout = 90 * time.Second
	}
	offerDeadline := time.Now().Add(offerTimeout)
	lastOfferMessage := ""
	for {
		if time.Now().After(offerDeadline) {
			return nil, prog, fmt.Errorf("xdcc_offer_timeout")
		}
		line, err := readIRCLineForPhase(ctx, conn, rw, offerDeadline, "waiting_for_offer", &lastOfferMessage)
		if err != nil {
			return nil, prog, err
		}
		logRelevantIRCLine("waiting_for_offer", line, opt.Bot)
		if glineErr := parseIRCGLine(line); glineErr != nil {
			return nil, prog, glineErr
		}
		if strings.HasPrefix(line, "PING ") {
			_ = writeLine("PONG " + strings.TrimPrefix(line, "PING "))
			continue
		}
		lower := strings.ToLower(line)
		if autoRegisterTried && !autoRegisterSucceeded && isNickServRegisterSuccess(line) {
			autoRegisterSucceeded = true
			log.Printf("nickserv register succeeded for %s", host)
		}
		if autoRegisterTried && isNickServRegisterFailure(line) {
			log.Printf("nickserv register did not succeed for %s: %s", host, strings.TrimSpace(line))
		}
		if !autoRegisteredPersisted && autoRegisteredPassword != "" && isNickServIdentifySuccess(line) {
			markAutoRegistered(opt, currentNick, autoRegisteredPassword, &autoRegisteredPersisted, &autoRegisterSucceeded, host)
		}
		if isAuthRequiredMessage(line) || strings.Contains(line, " 477 ") || strings.Contains(line, " 486 ") {
			if strings.TrimSpace(opt.NickServPassword) != "" && !opt.SASLEnabled {
				log.Printf("bot requires identified nick on %s; retrying nickserv identify", host)
				if err := performNickServIdentifyHandshake(ctx, conn, rw, host, opt.NickServPassword, &offerDeadline); err == nil {
					offerDeadline = time.Now().Add(opt.OfferTimeout)
					log.Printf("retrying xdcc request after successful identification on %s", host)
					_ = writeLine(fmt.Sprintf("PRIVMSG %s :XDCC SEND %d", opt.Bot, opt.Package))
					continue
				}
			}
			if opt.AutoRegister && strings.TrimSpace(opt.RegistrationEmail) != "" && !autoRegisterTried && !opt.SASLEnabled && strings.TrimSpace(opt.SASLPassword) == "" && strings.TrimSpace(opt.NickServPassword) == "" {
				autoRegisterTried = true
				autoRegisteredPassword = opt.RegistrationPassword
				if autoRegisteredPassword == "" {
					autoRegisteredPassword = generateRegistrationPassword()
				}
				log.Printf("bot requires identified nick on %s; attempting nickserv register", host)
				if err := performNickServHandshake(ctx, conn, rw, host, opt.RegistrationEmail, autoRegisteredPassword, &offerDeadline); err != nil {
					log.Printf("nickserv register did not succeed for %s: %v", host, err)
					return nil, prog, joinAuthRequiredError(line)
				}
				autoRegisterSucceeded = true
				markAutoRegistered(opt, currentNick, autoRegisteredPassword, &autoRegisteredPersisted, &autoRegisterSucceeded, host)
				offerDeadline = time.Now().Add(opt.OfferTimeout)
				log.Printf("retrying xdcc request after successful identification on %s", host)
				_ = writeLine(fmt.Sprintf("PRIVMSG %s :XDCC SEND %d", opt.Bot, opt.Package))
				continue
			}
			if autoRegisterTried {
				log.Printf("nickserv register did not succeed for %s: registration or identification still required", host)
			}
			return nil, prog, joinAuthRequiredError(line)
		}
		if autoRegisterTried && !isAuthRequiredMessage(line) && (strings.Contains(lower, strings.ToLower(strings.TrimSpace(opt.Bot))) || strings.Contains(lower, "queued") || strings.Contains(lower, "slot") || strings.Contains(lower, "sending you") || strings.Contains(lower, "dcc send") || strings.Contains(lower, "must be in the channel for at least")) {
			markAutoRegistered(opt, currentNick, autoRegisteredPassword, &autoRegisteredPersisted, &autoRegisterSucceeded, host)
		}
		if strings.Contains(lower, "must be in the channel for at least") {
			wait := parseWaitDuration(lower, 120*time.Second)
			if next := time.Now().Add(wait + 15*time.Second); next.After(offerDeadline) {
				offerDeadline = next
			}
			select {
			case <-ctx.Done():
				return nil, prog, ctx.Err()
			case <-time.After(wait):
			}
			_ = writeLine(fmt.Sprintf("PRIVMSG %s :XDCC SEND %d", opt.Bot, opt.Package))
			continue
		}
		if strings.Contains(lower, "queued") || strings.Contains(lower, "you are in queue") || strings.Contains(lower, "all slots are full") || strings.Contains(lower, "try again later") {
			wait := parseWaitDuration(lower, 45*time.Second)
			if next := time.Now().Add(wait + 15*time.Second); next.After(offerDeadline) {
				offerDeadline = next
			}
			select {
			case <-ctx.Done():
				return nil, prog, ctx.Err()
			case <-time.After(wait):
			}
			_ = writeLine(fmt.Sprintf("PRIVMSG %s :XDCC SEND %d", opt.Bot, opt.Package))
			continue
		}
		if !strings.Contains(line, "DCC SEND") {
			continue
		}
		offer, err := parseDCCSendLine(line)
		if err != nil {
			continue
		}
		offered := strings.TrimSpace(offer.Filename)
		expected := strings.TrimSpace(opt.ExpectedFilename)
		match, reason := filenameMatchDetailed(expected, offered)
		if !match {
			return nil, nil, &XDCCFilenameMismatchError{Expected: expected, Offered: offered}
		}
		if reason != "exact" {
			log.Printf("XDCC filename accepted via fuzzy match: expected=%q offered=%q rule=%s", expected, offered, reason)
		}
		if offer.Port == 0 && strings.TrimSpace(offer.Token) != "" && !opt.ReverseDCCEnabled {
			log.Printf("reverse dcc offer from %s on %s rejected: reverse dcc is disabled", opt.Bot, host)
			return nil, prog, reverseDCCDisabledError()
		}
		markAutoRegistered(opt, currentNick, autoRegisteredPassword, &autoRegisteredPersisted, &autoRegisterSucceeded, host)
		if prog == nil {
			prog = NewProgress(offer.Size)
		} else {
			prog.Expected = offer.Size
			if prog.Start.IsZero() {
				prog.Start = time.Now()
			}
			prog.Downloaded.Store(0)
		}
		res, err := dccDownload(ctx, rw, conn, opt, offer, destPath, prog, opt.DCCConnectTimeout, bytesPerSec)
		if err != nil {
			return nil, prog, err
		}
		return res, prog, nil
	}
}

type dccOffer struct {
	Filename string
	IP       net.IP
	Port     int
	Size     int64
	Token    string
}

func resolveIRCAddress(opt XDCCOptions) (host string, port int, useTLS bool, err error) {
	if opt.Host != "" {
		if opt.Port <= 0 {
			return "", 0, false, fmt.Errorf("missing irc.port")
		}
		return opt.Host, opt.Port, opt.TLS, nil
	}
	if opt.Network == "" {
		return "", 0, false, fmt.Errorf("missing irc.host and no legacy network fallback")
	}
	return parseNetwork(opt.Network)
}

func parseNetwork(v string) (host string, port int, useTLS bool, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", 0, false, fmt.Errorf("empty_network")
	}
	useTLS = false
	port = 6667
	if strings.HasPrefix(strings.ToLower(v), "ircs://") {
		useTLS = true
		port = 6697
		v = v[len("ircs://"):]
	} else if strings.HasPrefix(strings.ToLower(v), "irc://") {
		v = v[len("irc://"):]
	}
	v = strings.TrimSuffix(v, "/")
	if h, p, e := net.SplitHostPort(v); e == nil {
		host = h
		pi, e2 := strconv.Atoi(p)
		if e2 != nil {
			return "", 0, false, fmt.Errorf("invalid_port")
		}
		return host, pi, useTLS, nil
	}
	return v, port, useTLS, nil
}

func parseDCCSendLine(line string) (*dccOffer, error) {
	start := strings.IndexByte(line, 0x01)
	end := strings.LastIndexByte(line, 0x01)
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no_ctcp")
	}
	payload := strings.TrimSpace(line[start+1 : end])
	if !strings.HasPrefix(payload, "DCC SEND") {
		return nil, fmt.Errorf("not_dcc_send")
	}
	rest := strings.TrimSpace(strings.TrimPrefix(payload, "DCC SEND"))
	if rest == "" {
		return nil, fmt.Errorf("dcc_send_empty")
	}
	var filename string
	if strings.HasPrefix(rest, "\"") {
		rest = rest[1:]
		i := strings.Index(rest, "\"")
		if i < 0 {
			return nil, fmt.Errorf("bad_quote")
		}
		filename = rest[:i]
		rest = strings.TrimSpace(rest[i+1:])
	} else {
		parts := strings.SplitN(rest, " ", 2)
		if len(parts) < 2 {
			return nil, fmt.Errorf("bad_dcc")
		}
		filename = parts[0]
		rest = strings.TrimSpace(parts[1])
	}
	fields := strings.Fields(rest)
	if len(fields) < 3 {
		return nil, fmt.Errorf("bad_fields")
	}
	ip, err := parseDCCIP(fields[0])
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		size = 0
	}
	token := ""
	if len(fields) >= 4 {
		token = strings.TrimSpace(fields[3])
	}
	return &dccOffer{Filename: filename, IP: ip, Port: port, Size: size, Token: token}, nil
}

func parseDCCIP(v string) (net.IP, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, fmt.Errorf("empty_ip")
	}
	if strings.Count(v, ".") == 3 {
		ip := net.ParseIP(v)
		if ip == nil {
			return nil, fmt.Errorf("bad_ip")
		}
		return ip.To4(), nil
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(n))
	return net.IP(b), nil
}

func parseWaitDuration(s string, fallback time.Duration) time.Duration {
	fields := strings.Fields(strings.ToLower(s))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		if i+1 < len(fields) {
			switch fields[i+1] {
			case "second", "seconds", "sec", "secs":
				return time.Duration(n) * time.Second
			case "minute", "minutes", "min", "mins":
				return time.Duration(n) * time.Minute
			}
		}
	}
	return fallback
}

func filenameMatchDetailed(expected, offered string) (bool, string) {
	eClean := cleanFilename(expected)
	oClean := cleanFilename(offered)
	if eClean == "" || oClean == "" {
		return false, "empty"
	}
	if eClean == oClean {
		return true, "exact"
	}
	if filenameExt(expected) != filenameExt(offered) {
		return false, "extension_mismatch"
	}
	eBase := stripFilenameExt(eClean)
	oBase := stripFilenameExt(oClean)
	if eBase == oBase {
		return true, "base_exact"
	}
	eCore := coreFilenameBase(eBase)
	oCore := coreFilenameBase(oBase)
	if eCore != "" && eCore == oCore {
		return true, "core_exact"
	}
	eToks := significantFilenameTokens(eBase)
	oToks := significantFilenameTokens(oBase)
	shared := sharedTokenCount(eToks, oToks)
	required := minInt(len(eToks), len(oToks))
	if required >= 4 && shared >= required-1 {
		return true, "token_high_overlap"
	}
	if eCore != "" && oCore != "" && (strings.Contains(oCore, eCore) || strings.Contains(eCore, oCore)) {
		shorter := eCore
		if len(oCore) < len(shorter) {
			shorter = oCore
		}
		if len(shorter) >= 12 {
			return true, "core_contains"
		}
	}
	return false, "different"
}

func cleanFilename(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.ReplaceAll(v, " ", "_")
	for strings.Contains(v, "__") {
		v = strings.ReplaceAll(v, "__", "_")
	}
	return v
}

func filenameExt(v string) string {
	v = cleanFilename(v)
	idx := strings.LastIndexByte(v, '.')
	if idx < 0 || idx == len(v)-1 {
		return ""
	}
	return v[idx+1:]
}

func stripFilenameExt(v string) string {
	v = cleanFilename(v)
	idx := strings.LastIndexByte(v, '.')
	if idx < 0 {
		return v
	}
	return v[:idx]
}

func coreFilenameBase(v string) string {
	v = stripBracketSegments(v)
	v = strings.ReplaceAll(v, "_", "")
	v = strings.ReplaceAll(v, "-", "")
	v = strings.ReplaceAll(v, ".", "")
	v = strings.TrimSpace(v)
	return v
}

func stripBracketSegments(v string) string {
	var b strings.Builder
	depthParen := 0
	depthBracket := 0
	depthBrace := 0
	for _, r := range v {
		switch r {
		case '(':
			depthParen++
			continue
		case ')':
			if depthParen > 0 {
				depthParen--
			}
			continue
		case '[':
			depthBracket++
			continue
		case ']':
			if depthBracket > 0 {
				depthBracket--
			}
			continue
		case '{':
			depthBrace++
			continue
		case '}':
			if depthBrace > 0 {
				depthBrace--
			}
			continue
		}
		if depthParen == 0 && depthBracket == 0 && depthBrace == 0 {
			b.WriteRune(r)
		}
	}
	out := b.String()
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	return strings.Trim(out, "_-. ")
}

func significantFilenameTokens(v string) []string {
	v = strings.NewReplacer("(", "_", ")", "_", "[", "_", "]", "_", "{", "_", "}", "_", ".", "_", "-", "_", " ", "_").Replace(cleanFilename(v))
	raw := strings.Split(v, "_")
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, tok := range raw {
		tok = strings.TrimSpace(tok)
		if len(tok) < 3 {
			continue
		}
		if isMostlyDigits(tok) {
			continue
		}
		if _, ok := seen[tok]; ok {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out
}

func isMostlyDigits(v string) bool {
	if v == "" {
		return false
	}
	digits := 0
	for _, r := range v {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits*2 >= len(v)
}

func sharedTokenCount(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(a))
	for _, v := range a {
		set[v] = struct{}{}
	}
	shared := 0
	for _, v := range b {
		if _, ok := set[v]; ok {
			shared++
		}
	}
	return shared
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func dccDownload(ctx context.Context, rw *bufio.ReadWriter, ircConn net.Conn, opt XDCCOptions, offer *dccOffer, destPath string, prog *Progress, timeout time.Duration, bytesPerSec int64) (*Result, error) {
	var conn net.Conn
	var err error
	if offer.Port == 0 && strings.TrimSpace(offer.Token) != "" {
		if !opt.ReverseDCCEnabled {
			return nil, reverseDCCDisabledError()
		}
		conn, err = acceptReverseDCC(ctx, rw, ircConn, opt, offer, maxDuration(timeout, 90*time.Second))
		if err != nil {
			return nil, err
		}
	} else {
		addr := net.JoinHostPort(offer.IP.String(), strconv.Itoa(offer.Port))
		dialer := &net.Dialer{Timeout: timeout}
		conn, err = dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
	}
	defer conn.Close()
	stopConnCancel := closeConnOnContext(ctx, conn)
	defer stopConnCancel()
	return receiveDCCFile(ctx, conn, destPath, prog, bytesPerSec)
}

func receiveDCCFile(ctx context.Context, conn net.Conn, destPath string, prog *Progress, bytesPerSec int64) (*Result, error) {
	partPath := destPath + ".part"
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	buf := make([]byte, 32*1024)
	var written int64
	reader := NewRateLimitedReader(conn, bytesPerSec)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		n, rerr := reader.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return nil, werr
			}
			written += int64(n)
			prog.Downloaded.Store(written)

			ack := make([]byte, 4)
			binary.BigEndian.PutUint32(ack, uint32(written))
			nAck, ackErr := conn.Write(ack)
			if ackErr != nil {
				return nil, fmt.Errorf("xdcc_ack_write_failed: %w", ackErr)
			}
			if nAck != len(ack) {
				return nil, fmt.Errorf("xdcc_ack_write_failed: %w", io.ErrShortWrite)
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				if prog.Expected > 0 && written != prog.Expected {
					return nil, &XDCCTransferIncompleteError{Received: written, Expected: prog.Expected}
				}
				break
			}
			return nil, rerr
		}
	}

	if prog.Expected > 0 && written != prog.Expected {
		return nil, &XDCCTransferIncompleteError{Received: written, Expected: prog.Expected}
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	closed = true
	if err := os.Rename(partPath, destPath); err != nil {
		return nil, err
	}
	_ = os.Remove(destPath + ".part.meta.json")
	return &Result{FilePath: destPath, Bytes: written}, nil
}

func acceptReverseDCC(ctx context.Context, rw *bufio.ReadWriter, ircConn net.Conn, opt XDCCOptions, offer *dccOffer, timeout time.Duration) (net.Conn, error) {
	ln, err := listenReverseDCC(opt)
	if err != nil {
		return nil, err
	}
	defer func() {
		if ln != nil {
			_ = ln.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	ipNum, ipText := detectReverseDCCAdvertiseIP(ircConn, opt)
	payload := formatDCCSendReply(offer, ipNum, port)
	log.Printf("accepting reverse dcc from %s on local port %d advertise_ip=%s token=%s", opt.Bot, port, ipText, offer.Token)
	if err := writeIRCPrivmsg(rw, opt.Bot, payload); err != nil {
		return nil, err
	}
	if tcpLn, ok := ln.(*net.TCPListener); ok {
		_ = tcpLn.SetDeadline(time.Now().Add(timeout))
	}
	acceptCh := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		c, err := ln.Accept()
		acceptCh <- struct {
			conn net.Conn
			err  error
		}{c, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-acceptCh:
		if res.err != nil {
			return nil, wrapReverseDCCAcceptError(res.err, ipText, port)
		}
		_ = ln.Close()
		ln = nil
		log.Printf("reverse dcc connection accepted from %s", res.conn.RemoteAddr())
		return res.conn, nil
	}
}

func listenReverseDCC(opt XDCCOptions) (net.Listener, error) {
	host := strings.TrimSpace(opt.DCCListenHost)
	if host == "" {
		host = "0.0.0.0"
	}
	minPort, maxPort := opt.DCCPortMin, opt.DCCPortMax
	if minPort > 0 && maxPort > 0 {
		if minPort > maxPort {
			minPort, maxPort = maxPort, minPort
		}
		var lastErr error
		for p := minPort; p <= maxPort; p++ {
			ln, err := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(p)))
			if err == nil {
				return ln, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, fmt.Errorf("reverse_dcc_listen_range_failed: %w", lastErr)
		}
	}
	return net.Listen("tcp4", net.JoinHostPort(host, "0"))
}

func detectReverseDCCAdvertiseIP(ircConn net.Conn, opt XDCCOptions) (uint32, string) {
	if v := strings.TrimSpace(opt.DCCPublicIP); v != "" {
		ip := net.ParseIP(v)
		if ip4 := ip.To4(); ip4 != nil {
			return binary.BigEndian.Uint32(ip4), ip4.String()
		}
		log.Printf("ignoring invalid DCC public IP %q", v)
	}
	if ip, err := detectPublicIPv4(context.Background()); err == nil {
		return binary.BigEndian.Uint32(ip), ip.String()
	} else {
		log.Printf("could not auto-detect public IP for reverse DCC: %v", err)
	}
	if ta, ok := ircConn.LocalAddr().(*net.TCPAddr); ok {
		if ip4 := ta.IP.To4(); ip4 != nil && !ip4.IsUnspecified() {
			return binary.BigEndian.Uint32(ip4), ip4.String()
		}
	}
	return 0, "0.0.0.0"
}

func detectPublicIPv4(ctx context.Context) (net.IP, error) {
	publicIPCache.Lock()
	if publicIPCache.ip != nil && time.Since(publicIPCache.fetched) < 30*time.Minute {
		ip := append(net.IP(nil), publicIPCache.ip...)
		publicIPCache.Unlock()
		return ip, nil
	}
	publicIPCache.Unlock()

	urls := []string{
		strings.TrimSpace(os.Getenv("AUTOFETCH_DCC_PUBLIC_IP_URL")),
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}
	client := &http.Client{Timeout: 4 * time.Second}
	var lastErr error
	for _, url := range urls {
		if url == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 128))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(string(body)))
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsPrivate() && !ip4.IsLoopback() && !ip4.IsUnspecified() {
			publicIPCache.Lock()
			publicIPCache.ip = append(net.IP(nil), ip4...)
			publicIPCache.fetched = time.Now()
			publicIPCache.Unlock()
			return ip4, nil
		}
		lastErr = fmt.Errorf("%s returned non-public IPv4 %q", url, strings.TrimSpace(string(body)))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no public IP detection services configured")
	}
	return nil, lastErr
}

func wrapReverseDCCAcceptError(err error, advertisedIP string, port int) error {
	if err == nil {
		return nil
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return reverseDCCTimeoutError(advertisedIP, port)
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return reverseDCCTimeoutError(advertisedIP, port)
	}
	return err
}

func ReverseDCCHelpMessage(advertisedIP string, port int) string {
	return fmt.Sprintf("reverse_dcc_port_forward_required: port=%d advertised_ip=%s", port, strings.TrimSpace(advertisedIP))
}

func reverseDCCTimeoutError(advertisedIP string, port int) error {
	return fmt.Errorf("%w: %s", ErrReverseDCCTimeout, ReverseDCCHelpMessage(advertisedIP, port))
}

func ReverseDCCDisabledMessage() string {
	return "reverse_dcc_disabled"
}

func reverseDCCDisabledError() error {
	return fmt.Errorf("%w: %s", ErrReverseDCCDisabled, ReverseDCCDisabledMessage())
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func formatDCCSendReply(offer *dccOffer, ipNum uint32, port int) string {
	filename := offer.Filename
	if strings.ContainsAny(filename, " ") {
		filename = strconv.Quote(filename)
	}
	return fmt.Sprintf("\001DCC SEND %s %d %d %d %s\001", filename, ipNum, port, offer.Size, offer.Token)
}
