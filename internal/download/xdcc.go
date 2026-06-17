package download

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ErrXDCCFilenameMismatch is returned when the DCC SEND offer filename
// does not match the expected filename from the job instruction.
var ErrXDCCFilenameMismatch = errors.New("xdcc_filename_mismatch")

// ErrFilenameMismatch is a backwards-compatible alias used by older caller code.
// (runner.go checks this name)
var ErrFilenameMismatch = ErrXDCCFilenameMismatch

// XDCCFilenameMismatchError carries expected and offered filenames.
// It unwraps to ErrXDCCFilenameMismatch so errors.Is(err, ErrXDCCFilenameMismatch) works.
type XDCCFilenameMismatchError struct {
	Expected string
	Offered  string
}

func (e *XDCCFilenameMismatchError) Error() string {
	return fmt.Sprintf("xdcc_filename_mismatch: expected=%q offered=%q", e.Expected, e.Offered)
}

func (e *XDCCFilenameMismatchError) Unwrap() error {
	return ErrXDCCFilenameMismatch
}

// OfferedFilename extracts the offered filename from a mismatch error (if present).
func OfferedFilename(err error) string {
	var e *XDCCFilenameMismatchError
	if errors.As(err, &e) {
		return e.Offered
	}
	return ""
}

type XDCCOptions struct {
	Nick string

	Host    string
	Port    int
	TLS     bool
	Network string // legacy fallback: host[:port] or irc://host:port or ircs://host:port

	JoinChannels []string
	Channel      string
	Bot          string
	Package      int

	ExpectedFilename string

	// Optional timeouts
	IRCHandshakeTimeout time.Duration
	OfferTimeout        time.Duration
	DCCConnectTimeout   time.Duration
}

func DownloadXDCCToFile(ctx context.Context, opt XDCCOptions, destPath string, bytesPerSec int64, prog *Progress) (*Result, *Progress, error) {
	opt.Nick = strings.TrimSpace(opt.Nick)
	if opt.Nick == "" {
		opt.Nick = "autofetch"
	}
	opt.Host = strings.TrimSpace(opt.Host)
	opt.Network = strings.TrimSpace(opt.Network)
	opt.Channel = strings.TrimSpace(opt.Channel)
	opt.Bot = strings.TrimSpace(opt.Bot)
	opt.ExpectedFilename = strings.TrimSpace(opt.ExpectedFilename)
	if len(opt.JoinChannels) == 0 && opt.Channel != "" {
		opt.JoinChannels = []string{opt.Channel}
	}
	if (opt.Host == "" && opt.Network == "") || opt.Bot == "" || opt.ExpectedFilename == "" || opt.Package <= 0 {
		return nil, prog, fmt.Errorf("xdcc_missing_fields")
	}
	if opt.IRCHandshakeTimeout <= 0 {
		opt.IRCHandshakeTimeout = 15 * time.Second
	}
	if opt.OfferTimeout <= 0 {
		opt.OfferTimeout = 45 * time.Second
	}
	if opt.DCCConnectTimeout <= 0 {
		opt.DCCConnectTimeout = 20 * time.Second
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return nil, prog, err
	}

	// Connect IRC
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

	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	writeLine := func(line string) error {
		_, err := rw.WriteString(line + "\r\n")
		if err != nil {
			return err
		}
		return rw.Flush()
	}

	// Basic registration
	if err := writeLine("NICK " + opt.Nick); err != nil {
		return nil, prog, err
	}
	if err := writeLine("USER " + opt.Nick + " 0 * :autofetch"); err != nil {
		return nil, prog, err
	}

	// Wait for welcome (001) and handle PINGs.
	ctxHello, cancelHello := context.WithTimeout(ctx, opt.IRCHandshakeTimeout)
	defer cancelHello()
	for {
		select {
		case <-ctxHello.Done():
			return nil, prog, fmt.Errorf("irc_handshake_timeout")
		default:
		}
		line, err := rw.ReadString('\n')
		if err != nil {
			return nil, prog, err
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PING ") {
			_ = writeLine("PONG " + strings.TrimPrefix(line, "PING "))
			continue
		}
		if strings.Contains(line, " 001 ") {
			break
		}
		// If nickname is in use, fall back to nick_
		if strings.Contains(line, " 433 ") {
			opt.Nick = opt.Nick + "_"
			_ = writeLine("NICK " + opt.Nick)
		}
	}

	// Join channels in order (best-effort)
	for _, ch := range opt.JoinChannels {
		ch = strings.TrimSpace(ch)
		if ch == "" {
			continue
		}
		log.Printf("Joining channel: %s", ch)
		_ = writeLine("JOIN " + ch)
	}

	// Request XDCC
	log.Printf("Requesting XDCC pack %d", opt.Package)
	_ = writeLine(fmt.Sprintf("PRIVMSG %s :XDCC SEND %d", opt.Bot, opt.Package))

	// Wait for DCC SEND offer
	ctxOffer, cancelOffer := context.WithTimeout(ctx, opt.OfferTimeout)
	defer cancelOffer()
	var offer *dccOffer
	for offer == nil {
		select {
		case <-ctxOffer.Done():
			return nil, prog, fmt.Errorf("xdcc_offer_timeout")
		default:
		}
		line, err := rw.ReadString('\n')
		if err != nil {
			return nil, prog, err
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PING ") {
			_ = writeLine("PONG " + strings.TrimPrefix(line, "PING "))
			continue
		}
		if !strings.Contains(line, "DCC SEND") {
			continue
		}
		o, err := parseDCCSendLine(line)
		if err != nil {
			continue
		}
		offer = o
	}

	// Validate offered filename matches expected.
	// Some XDCC bots preserve spaces in DCC SEND ("test file.pdf"), while the
	// server-side expected name may already use common filename separators
	// ("test_file.pdf" or "test-file.pdf"). Treat whitespace, underscores,
	// and dashes as equivalent separators while still rejecting different words.
	offered := strings.TrimSpace(offer.Filename)
	expected := strings.TrimSpace(opt.ExpectedFilename)
	if !xdccFilenamesMatch(expected, offered) {
		return nil, nil, &XDCCFilenameMismatchError{
			Expected: expected,
			Offered:  offered,
		}
	}

	// Download via DCC
	if prog == nil {
		prog = NewProgress(offer.Size)
	} else {
		prog.Expected = offer.Size
		if prog.Start.IsZero() {
			prog.Start = time.Now()
		}
		prog.Downloaded.Store(0)
	}
	res, err := dccDownload(ctx, offer, destPath, prog, opt.DCCConnectTimeout, bytesPerSec)
	if err != nil {
		return nil, prog, err
	}
	return res, prog, nil
}

func xdccFilenamesMatch(expected, offered string) bool {
	expected = strings.TrimSpace(expected)
	offered = strings.TrimSpace(offered)
	if expected == offered {
		return true
	}
	return normalizeXDCCFilenameForMatch(expected) == normalizeXDCCFilenameForMatch(offered)
}

func normalizeXDCCFilenameForMatch(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	var b strings.Builder
	lastWasSeparator := false
	for _, r := range name {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			if !lastWasSeparator {
				b.WriteRune(' ')
				lastWasSeparator = true
			}
			continue
		}
		b.WriteRune(r)
		lastWasSeparator = false
	}
	return strings.ToLower(strings.TrimSpace(b.String()))
}

type dccOffer struct {
	Filename string
	IP       net.IP
	Port     int
	Size     int64
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
	// Accept:
	// - irc://host:port (plain)
	// - ircs://host:port (TLS)
	// - host:port
	// - host
	//
	// Defaults:
	// - irc: 6667
	// - ircs: 6697
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
		useTLS = false
		port = 6667
		v = v[len("irc://"):]
	}

	// Strip possible trailing slash
	v = strings.TrimSuffix(v, "/")

	// host[:port]
	if h, p, e := net.SplitHostPort(v); e == nil {
		host = h
		pi, e2 := strconv.Atoi(p)
		if e2 != nil {
			return "", 0, false, fmt.Errorf("invalid_port")
		}
		port = pi
		return host, port, useTLS, nil
	}

	// If it contains ":" but SplitHostPort failed, it might be missing port brackets etc.
	// Keep it simple: treat as host only.
	host = v
	return host, port, useTLS, nil
}

// parseDCCSendLine parses a CTCP DCC SEND offer line.
// It supports quoted or unquoted filenames.
func parseDCCSendLine(line string) (*dccOffer, error) {
	// Extract CTCP payload: \x01DCC SEND ...\x01
	start := strings.IndexByte(line, 0x01)
	end := strings.LastIndexByte(line, 0x01)
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no_ctcp")
	}
	payload := line[start+1 : end]
	payload = strings.TrimSpace(payload)
	if !strings.HasPrefix(payload, "DCC SEND") {
		return nil, fmt.Errorf("not_dcc_send")
	}

	rest := strings.TrimSpace(strings.TrimPrefix(payload, "DCC SEND"))
	if rest == "" {
		return nil, fmt.Errorf("dcc_send_empty")
	}

	// Tokenize: handle optional quoted filename.
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
		// filename up to first space
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

	ipStr := fields[0]
	portStr := fields[1]
	sizeStr := fields[2]

	ip, err := parseDCCIP(ipStr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		// sometimes size is missing; treat as unknown (0)
		size = 0
	}

	return &dccOffer{Filename: filename, IP: ip, Port: port, Size: size}, nil
}

func parseDCCIP(v string) (net.IP, error) {
	// DCC IP is often a 32-bit integer (in decimal) representing IPv4.
	// Sometimes bots send dotted-quad already.
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

func dccDownload(ctx context.Context, offer *dccOffer, destPath string, prog *Progress, timeout time.Duration, bytesPerSec int64) (*Result, error) {
	addr := net.JoinHostPort(offer.IP.String(), strconv.Itoa(offer.Port))
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	partPath := destPath + ".part"

	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()

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

			// ACK: send bytes received (unsigned 32bit, big endian)
			ack := make([]byte, 4)
			binary.BigEndian.PutUint32(ack, uint32(written))
			_, _ = conn.Write(ack)
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return nil, rerr
		}
	}

	// Finalize: rename .part -> final
	if err := os.Rename(partPath, destPath); err != nil {
		return nil, err
	}
	_ = os.Remove(destPath + ".part.meta.json") // not used for xdcc, but harmless cleanup

	return &Result{FilePath: destPath, Bytes: written}, nil
}
