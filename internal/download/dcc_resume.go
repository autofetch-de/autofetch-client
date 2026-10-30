package download

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DCC RESUME is an optional, bot-negotiated extension to active DCC SEND.
// Metadata contains only source fingerprints and local content digests, never
// credentials, IRC commands, or the downloaded content itself.
const dccCheckpointInterval = 4 * 1024 * 1024

type dccPartialMeta struct {
	Kind          string `json:"kind"`
	Version       int    `json:"version"`
	SourceSHA256  string `json:"source_sha256"`
	ExpectedBytes int64  `json:"expected_bytes"`
	Checkpoint    int64  `json:"checkpoint_bytes"`
	SHA256        string `json:"checkpoint_sha256,omitempty"`
}

func dccSourceFingerprint(opt XDCCOptions, offer *dccOffer) string {
	// All source-affecting fields are explicitly included. Do not key partials
	// only by filenames; one output path can be reused for different bots.
	components := []string{opt.Host, strconv.Itoa(opt.Port), strconv.FormatBool(opt.TLS),
		opt.TLSVerification, opt.TLSSPKIPinSHA256, opt.Network, opt.Channel,
		opt.Bot, strconv.Itoa(opt.Package), opt.DownloadMethod, opt.SourceID,
		opt.CommandTargetKind, opt.CommandTarget, opt.DownloadCommand,
		opt.ExpectedFilename, offer.Filename}
	sum := sha256.Sum256([]byte(strings.Join(components, "\x00")))
	return hex.EncodeToString(sum[:])
}

func dccMetaPath(dest string) string { return dest + ".part.meta.json" }

func writeDCCMeta(path string, meta dccPartialMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dcc-meta-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Preserve unverified partials rather than overwriting or appending to them.
// Older clients' .part files have no DCC source/size metadata.
func quarantineDCCPartial(dest string) error {
	part := dest + ".part"
	_, partErr := os.Lstat(part)
	_, metaErr := os.Lstat(dccMetaPath(dest))
	if os.IsNotExist(partErr) && os.IsNotExist(metaErr) {
		return nil
	}
	if partErr != nil && !os.IsNotExist(partErr) {
		return partErr
	}
	if metaErr != nil && !os.IsNotExist(metaErr) {
		return metaErr
	}
	backup := fmt.Sprintf("%s.unverified-%d", part, time.Now().UnixNano())
	if partErr == nil {
		if err := os.Rename(part, backup); err != nil {
			return err
		}
	}
	if metaErr == nil {
		if err := os.Rename(dccMetaPath(dest), backup+".meta.json"); err != nil {
			return err
		}
	}
	log.Printf("DCC partial cannot be safely resumed; retained previous data at %s", backup)
	return nil
}

func hashDCCPrefix(path string, n int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyN(h, f, n); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// prepareDCCPartial validates source identity, announced size and every byte
// of the last durable checkpoint. A crash can leave uncheckpointed tail bytes;
// those bytes are discarded before any attempt to append.
func prepareDCCPartial(dest string, opt XDCCOptions, offer *dccOffer) (int64, dccPartialMeta, error) {
	meta := dccPartialMeta{Kind: "dcc_resume", Version: 1, SourceSHA256: dccSourceFingerprint(opt, offer), ExpectedBytes: offer.Size}
	part := dest + ".part"
	stat, err := os.Lstat(part)
	if os.IsNotExist(err) {
		// Remove a metadata sidecar orphaned by a completed rename/crash.
		if _, e := os.Lstat(dccMetaPath(dest)); e == nil {
			if e = os.Remove(dccMetaPath(dest)); e != nil {
				return 0, meta, e
			}
		}
		return 0, meta, nil
	}
	if err != nil {
		return 0, meta, err
	}
	raw, readErr := os.ReadFile(dccMetaPath(dest))
	var saved dccPartialMeta
	if !stat.Mode().IsRegular() || readErr != nil || json.Unmarshal(raw, &saved) != nil ||
		saved.Kind != meta.Kind || saved.Version != meta.Version ||
		saved.SourceSHA256 != meta.SourceSHA256 || offer.Size <= 0 ||
		saved.ExpectedBytes != offer.Size || saved.Checkpoint < 0 ||
		saved.Checkpoint > stat.Size() || saved.Checkpoint > offer.Size ||
		(saved.Checkpoint > 0 && len(saved.SHA256) != 64) {
		if err := quarantineDCCPartial(dest); err != nil {
			return 0, meta, err
		}
		return 0, meta, nil
	}
	if saved.Checkpoint > 0 {
		digest, err := hashDCCPrefix(part, saved.Checkpoint)
		if err != nil {
			return 0, meta, err
		}
		if digest != saved.SHA256 {
			if err := quarantineDCCPartial(dest); err != nil {
				return 0, meta, err
			}
			return 0, meta, nil
		}
	}
	if stat.Size() > saved.Checkpoint {
		// Only truncate after confirming the checkpoint's hash.
		if err := os.Truncate(part, saved.Checkpoint); err != nil {
			return 0, meta, err
		}
	}
	return saved.Checkpoint, saved, nil
}

func parseDCCAccept(line string) (string, int, int64, error) {
	start, end := strings.IndexByte(line, '\x01'), strings.LastIndexByte(line, '\x01')
	if start < 0 || end <= start {
		return "", 0, 0, fmt.Errorf("dcc_resume_not_ctcp")
	}
	payload := strings.TrimSpace(line[start+1 : end])
	if !strings.HasPrefix(payload, "DCC ACCEPT ") {
		return "", 0, 0, fmt.Errorf("dcc_resume_not_accept")
	}
	rest := strings.TrimPrefix(payload, "DCC ACCEPT ")
	var filename string
	if strings.HasPrefix(rest, "\"") {
		rest = rest[1:]
		idx := strings.IndexByte(rest, '"')
		if idx < 0 {
			return "", 0, 0, fmt.Errorf("dcc_resume_bad_filename")
		}
		filename, rest = rest[:idx], rest[idx+1:]
	} else {
		fields := strings.SplitN(rest, " ", 2)
		if len(fields) != 2 {
			return "", 0, 0, fmt.Errorf("dcc_resume_bad_fields")
		}
		filename, rest = fields[0], fields[1]
	}
	fields := strings.Fields(rest)
	if len(fields) != 2 {
		return "", 0, 0, fmt.Errorf("dcc_resume_bad_fields")
	}
	port, e1 := strconv.Atoi(fields[0])
	offset, e2 := strconv.ParseInt(fields[1], 10, 64)
	if e1 != nil || e2 != nil || port <= 0 || port > 65535 || offset <= 0 {
		return "", 0, 0, fmt.Errorf("dcc_resume_bad_position")
	}
	return filename, port, offset, nil
}

// An ACCEPT is trustworthy only as part of this IRC session, coming from the
// selected bot, addressing our actual nickname and matching the SEND offer.
func negotiateDCCResume(ctx context.Context, ircConn net.Conn, rw *bufio.ReadWriter,
	bot, nick string, offer *dccOffer, offset int64, timeout time.Duration) (bool, error) {
	if offer.Port <= 0 || offer.Port > 65535 || offset <= 0 || offset >= offer.Size {
		return false, nil
	}
	if strings.ContainsAny(offer.Filename, "\r\n\x00\x01\"") {
		return false, nil
	}
	request := fmt.Sprintf("\x01DCC RESUME %q %d %d\x01", offer.Filename, offer.Port, offset)
	if err := writeIRCPrivmsg(rw, bot, request); err != nil {
		return false, err
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	deadline := time.Now().Add(timeout)
	defer ircConn.SetReadDeadline(time.Time{})
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		line, err := readIRCLineWithDeadline(ctx, ircConn, rw, deadline)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if err.Error() == "nickserv_handshake_timeout" {
				return false, nil
			}
			return false, err
		}
		if strings.HasPrefix(line, "PING ") {
			if err := writeIRCLine(rw, "PONG "+strings.TrimPrefix(line, "PING ")); err != nil {
				return false, err
			}
			continue
		}
		if !isSelectedDCCOffer(line, bot, nick) || !strings.Contains(line, "DCC ACCEPT") {
			continue
		}
		name, port, n, err := parseDCCAccept(line)
		if err != nil || name != offer.Filename || port != offer.Port || n != offset {
			return false, fmt.Errorf("dcc_resume_accept_mismatch")
		}
		return true, nil
	}
}

// Called when a normal (non-resume-capable) DCC session needs to restart from
// byte zero. Existing partial data is preserved separately, not clobbered.
func restartDCCFromZero(dest string) error {
	return quarantineDCCPartial(dest)
}
