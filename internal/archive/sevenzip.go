package archive

// 7-Zip is used as a *decoder*, never as a filesystem extractor. All bytes
// are streamed through the same path and size validation as ZIP and TAR.
// That matters because 7z/RAR may contain links, absolute paths or bombs.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxSevenZipListing = 16 << 20

type sevenZipEntry struct {
	name string
	size int64
	dir  bool
}

// The upstream 7-Zip console executable is called 7zz on most Linux/macOS
// distributions and 7z.exe on Windows. 7za is intentionally excluded:
// it doesn't contain the full RAR decoder.
func sevenZipBinary() (string, error) {
	for _, name := range []string{"7zz", "7z"} {
		if found, err := exec.LookPath(name); err == nil {
			return found, nil
		}
	}
	return "", fmt.Errorf("archive_7zip_missing")
}

// parseSevenZipList parses the -slt -ba technical listing. It fails closed
// if a record is incomplete, suspicious, encrypted, or has an unusual link
// attribute. Importantly, paths never become shell commands.
func parseSevenZipList(data []byte, limits Limits, multipart bool) ([]sevenZipEntry, error) {
	limits = limits.withDefaults()
	if len(data) == 0 || len(data) > maxSevenZipListing || !utf8Valid(data) {
		return nil, fmt.Errorf("archive_invalid_7zip_listing")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	blocks := strings.Split(strings.TrimSpace(text), "\n\n")
	// Avoid quadratic solid-block re-decompression when each member is streamed
	// independently. A dedicated solid/multi-volume strategy comes later.
	if len(blocks) > limits.MaxEntries || len(blocks) > 2048 {
		return nil, fmt.Errorf("archive_too_many_entries")
	}
	var entries []sevenZipEntry
	var total int64
	for recordIndex, block := range blocks {
		fields := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			// 7-Zip can print empty metadata as either "Key = " or "Key =".
			// Both occur in real RAR technical listings, including entries
			// with unset file attributes, timestamps and link metadata.
			k, v, ok := strings.Cut(line, " =")
			if !ok || k == "" || strings.TrimSpace(k) != k || (v != "" && !strings.HasPrefix(v, " ")) {
				return nil, fmt.Errorf("archive_invalid_7zip_listing: record=%d malformed_field", recordIndex+1)
			}
			if v != "" {
				v = v[1:] // Strip precisely one separator space; preserve value.
			}
			if _, duplicate := fields[k]; duplicate {
				return nil, fmt.Errorf("archive_invalid_7zip_listing: record=%d duplicate_field", recordIndex+1)
			}
			fields[k] = v
		}
		name, ok := fields["Path"]
		if !ok || name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "@") {
			return nil, fmt.Errorf("archive_unsafe_path")
		}
		if _, err := validName(name); err != nil {
			return nil, err
		}
		if fields["Encrypted"] != "-" { // don't invoke password prompts
			return nil, fmt.Errorf("archive_encrypted_unsupported")
		}
		// RAR5 may expose links and alternate data streams via separate
		// fields rather than the ordinary file attribute flags.
		for _, linkKey := range []string{"Symbolic Link", "Hard Link", "Copy Link", "Reparse", "Anti", "Alternate Stream", "Is Alt Stream"} {
			if v := fields[linkKey]; v != "" && v != "-" {
				return nil, fmt.Errorf("archive_special_entry_rejected")
			}
		}
		for _, key := range []string{"Split Before", "Split After"} {
			v := fields[key]
			if v != "" && v != "-" && !(multipart && v == "+") {
				return nil, fmt.Errorf("archive_special_entry_rejected")
			}
		}
		attrs, hasAttrs := fields["Attributes"]
		// 7-Zip can encode a reparse point in Windows file attributes.
		// The 'L' attribute marks a reparse point, not an ordinary file.
		if attrs != "" && strings.Contains(strings.Fields(attrs)[0], "L") {
			return nil, fmt.Errorf("archive_special_entry_rejected")
		}
		if mode := fields["Mode"]; strings.HasPrefix(mode, "l") || strings.HasPrefix(mode, "c") || strings.HasPrefix(mode, "b") || strings.HasPrefix(mode, "p") || strings.HasPrefix(mode, "s") {
			return nil, fmt.Errorf("archive_special_entry_rejected")
		}
		if !hasAttrs {
			return nil, fmt.Errorf("archive_invalid_7zip_listing")
		}
		// Unix permission metadata is commonly shown as the second section.
		// A link/special inode cannot be materialized as an ordinary file.
		for _, part := range strings.Fields(attrs) {
			if len(part) == 10 && strings.ContainsRune("lcbps", rune(part[0])) {
				return nil, fmt.Errorf("archive_special_entry_rejected")
			}
		}
		isDir := strings.HasPrefix(attrs, "D") || fields["Folder"] == "+"
		if v := fields["Folder"]; v != "" {
			if (v != "+" && v != "-") || (v == "+" && !isDir) || (v == "-" && isDir) {
				return nil, fmt.Errorf("archive_invalid_7zip_listing")
			}
		}
		sizeString, ok := fields["Size"]
		if !ok {
			return nil, fmt.Errorf("archive_invalid_7zip_listing")
		}
		size, err := strconv.ParseInt(sizeString, 10, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("archive_invalid_7zip_listing")
		}
		if isDir && size != 0 {
			return nil, fmt.Errorf("archive_directory_with_data")
		}
		if size > limits.MaxFileBytes || size > limits.MaxTotalBytes-total {
			return nil, fmt.Errorf("archive_uncompressed_limit")
		}
		if !isDir {
			total += size
		}
		entries = append(entries, sevenZipEntry{name: name, size: size, dir: isDir})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("archive_invalid_7zip_listing")
	}
	return entries, nil
}

// Bounded writer avoids unbounded memory use from a malicious or misbehaving
// external decoder. Returning an error also terminates the child process.
type boundedBuffer struct {
	bytes.Buffer
	maximum int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.maximum-b.Len() {
		return 0, fmt.Errorf("archive_7zip_output_limit")
	}
	return b.Buffer.Write(p)
}

func (x *extractor) extractSevenZip(source string, volumes []string) error {
	binary, err := sevenZipBinary()
	if err != nil {
		return err
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	if len(volumes) > 1 || volumeLeader(source) {
		// The listing alone may succeed with missing final volumes. Demand that
		// 7-Zip read and verify the entire volume set BEFORE publishing anything.
		testCmd := exec.CommandContext(x.ctx, binary, "t", "-p-", "-bso0", "-bsp0", absSource)
		testCmd.Stdin = bytes.NewReader(nil)
		testCmd.Stdout = &boundedBuffer{maximum: 8192}
		testCmd.Stderr = &boundedBuffer{maximum: 8192}
		if err := testCmd.Run(); err != nil {
			return fmt.Errorf("archive_volume_incomplete_or_corrupt: %w", err)
		}
	}
	// Prevent password prompts and interactive input. -ba removes the archive
	// header so each Path record corresponds to a member only.
	cmd := exec.CommandContext(x.ctx, binary,
		"l", "-slt", "-ba", "-sccUTF-8", "-p-", "-bsp0", absSource)
	cmd.Stdin = bytes.NewReader(nil)
	result := &boundedBuffer{maximum: maxSevenZipListing}
	cmd.Stdout = result
	stderr := &boundedBuffer{maximum: 8192}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if e := x.ctx.Err(); e != nil {
			return e
		}
		return fmt.Errorf("archive_7zip_list_failed: %w", err)
	}
	entries, err := parseSevenZipList(result.Bytes(), x.limits, volumeLeader(source))
	if err != nil {
		return err
	}
	// Solid archive members don't have trustworthy individual packed sizes,
	// but an aggregate ratio against the complete archive is still useful.
	// Multi-volume series are intentionally out of scope here.
	var compressedBytes int64
	for _, p := range volumes {
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 {
			return fmt.Errorf("archive_invalid_source_size")
		}
		if st.Size() > (1<<40)-compressedBytes {
			return fmt.Errorf("archive_volume_size_limit")
		}
		compressedBytes += st.Size()
	}
	if compressedBytes <= 0 {
		return fmt.Errorf("archive_invalid_source_size")
	}
	var uncompressed uint64
	for _, e := range entries {
		if !e.dir {
			uncompressed += uint64(e.size)
		}
	}
	if uncompressed/uint64(compressedBytes) > x.limits.MaxCompressionRatio {
		return fmt.Errorf("archive_compression_ratio_limit")
	}
	// Reserve every path before opening any output file. This rejects
	// duplicate, case-conflicting, file/dir-conflicting names atomically.
	for _, e := range entries {
		if _, err := x.reserve(e.name, e.dir); err != nil {
			return err
		}
	}
	// reserve() has already counted everything; don't count twice when
	// writing, but retain canonical path records for collision checking.
	x.count = 0
	x.names = map[string]string{}
	x.spellings = map[string]string{}
	for _, e := range entries {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		if e.dir {
			if err := x.writeDir(e.name); err != nil {
				return err
			}
			continue
		}
		if err := x.extractSevenZipFile(binary, absSource, e); err != nil {
			return err
		}
	}
	return nil
}

func (x *extractor) extractSevenZipFile(binary, source string, entry sevenZipEntry) error {
	// -spd disables 7-Zip wildcards. The listed, validated entry is an exact
	// member selector, and stdout is the only destination used by 7-Zip.
	cmd := exec.CommandContext(x.ctx, binary,
		"x", "-so", "-spd", "-sccUTF-8", "-p-", "-bsp0", source, entry.name)
	cmd.Stdin = bytes.NewReader(nil)
	stderr := &boundedBuffer{maximum: 8192}
	cmd.Stderr = stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("archive_7zip_extract_failed: %w", err)
	}
	// Always reap the child; if file validation rejects the stream, terminate
	// the decoder so it can't block on an undrained stdout pipe.
	extractedErr := x.writeFile(entry.name, entry.size, pipe)
	if extractedErr == nil {
		var overflow [1]byte
		var n int
		n, err = pipe.Read(overflow[:])
		if n != 0 || !errors.Is(err, io.EOF) {
			extractedErr = fmt.Errorf("archive_7zip_stream_size_mismatch")
		}
	}
	if extractedErr != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	waitErr := cmd.Wait()
	if extractedErr != nil {
		return extractedErr
	}
	if waitErr != nil {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("archive_7zip_extract_failed: %w", waitErr)
	}
	return nil
}

// No NULs or malformed UTF-8 in archive listing metadata.
func utf8Valid(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	return utf8.Valid(b)
}
