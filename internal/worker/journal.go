package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DownloadReceipt is local evidence of a completed transfer, not an
// authentication token and not proof of the authenticity of remote content.
// Keep it independent of the server's attempt ID: a job may be re-leased.
type DownloadReceipt struct {
	Version                int               `json:"version"`
	ClientID               string            `json:"client_id"`
	JobID                  string            `json:"job_id"`
	InstructionHash        string            `json:"instruction_sha256"`
	TargetPath             string            `json:"target_path"`
	Phase                  string            `json:"phase"`
	Bytes                  int64             `json:"bytes,omitempty"`
	SHA256                 string            `json:"sha256,omitempty"`
	ArchiveDestination     string            `json:"archive_destination,omitempty"`
	ArchiveDestinationKind string            `json:"archive_destination_kind,omitempty"`
	UpdatedAt              time.Time         `json:"updated_at"`
	Attempts               []DownloadAttempt `json:"attempts,omitempty"`
}

type DownloadJournal struct{ Dir string }

// Serialize local read-modify-write transitions between the download worker
// and the independent archive cleanup worker.
var journalTransitions sync.Mutex

func (j DownloadJournal) filename(clientID, jobID string) (string, error) {
	if j.Dir == "" || clientID == "" || jobID == "" {
		return "", fmt.Errorf("download_journal_missing_identity")
	}
	sum := sha256.Sum256([]byte(clientID + "\x00" + jobID))
	return filepath.Join(j.Dir, hex.EncodeToString(sum[:])+".json"), nil
}

func (j DownloadJournal) Load(clientID, jobID string) (DownloadReceipt, bool, error) {
	path, err := j.filename(clientID, jobID)
	if err != nil {
		return DownloadReceipt{}, false, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DownloadReceipt{}, false, nil
	}
	if err != nil {
		return DownloadReceipt{}, false, err
	}
	var rec DownloadReceipt
	if err := json.Unmarshal(b, &rec); err != nil {
		return DownloadReceipt{}, false, fmt.Errorf("download_journal_invalid: %w", err)
	}
	if rec.Version != 1 || rec.ClientID != clientID || rec.JobID != jobID || (rec.Phase != "downloading" && rec.Phase != "transferred" && rec.Phase != "postprocessed") || rec.TargetPath == "" || rec.InstructionHash == "" {
		return DownloadReceipt{}, false, fmt.Errorf("download_journal_invalid_record")
	}
	return rec, true, nil
}

// Write uses a same-directory rename, with fsync before rename. Never put
// credentials, raw URLs or IRC passwords in this journal.
func (j DownloadJournal) Write(rec DownloadReceipt) error {
	path, err := j.filename(rec.ClientID, rec.JobID)
	if err != nil {
		return err
	}
	if rec.Version != 1 || rec.InstructionHash == "" || rec.TargetPath == "" {
		return fmt.Errorf("download_journal_invalid_record")
	}
	if err := os.MkdirAll(j.Dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(j.Dir, ".receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Directory sync is best-effort on platforms where directory handles
	// cannot be synced; the completed file is never deleted in either case.
	if dir, err := os.Open(j.Dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func instructionFingerprint(inst any) (string, error) {
	b, err := json.Marshal(inst)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func hashLocalFile(ctx context.Context, path string) (int64, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("download_target_not_regular_file")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1024*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			total += int64(n)
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, "", err
		}
	}
	if total != info.Size() {
		return 0, "", fmt.Errorf("download_file_modified_during_verification")
	}
	return total, hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyCompleted checks job identity, the exact local target, byte count
// and all bytes. Any mismatch is an error, never a reason to overwrite.
func (j DownloadJournal) VerifyCompleted(ctx context.Context, rec DownloadReceipt, fingerprint, target string) (bool, error) {
	if rec.InstructionHash != fingerprint || rec.TargetPath != target {
		return false, fmt.Errorf("download_journal_source_changed")
	}
	if rec.Phase != "transferred" && rec.Phase != "postprocessed" {
		return false, nil
	}
	if rec.Phase == "postprocessed" {
		// This receipt records the successful extraction and intentional
		// removal of the original. Never silently redownload on a re-lease.
		// We require the persisted destination to still exist.
		st, err := os.Lstat(rec.ArchiveDestination)
		if err != nil || st.Mode()&os.ModeSymlink != 0 || (rec.ArchiveDestinationKind == "file" && !st.Mode().IsRegular()) || (rec.ArchiveDestinationKind != "file" && !st.IsDir()) {
			return false, fmt.Errorf("download_postprocessed_destination_missing")
		}
		if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
	}
	if rec.Bytes <= 0 || len(rec.SHA256) != 64 {
		return false, fmt.Errorf("download_journal_invalid_completion")
	}
	n, sum, err := hashLocalFile(ctx, target)
	if err != nil {
		return false, fmt.Errorf("download_journal_file_invalid: %w", err)
	}
	if n != rec.Bytes || sum != rec.SHA256 {
		return false, fmt.Errorf("download_journal_file_changed")
	}
	return true, nil
}

// readHistoryReceipt is restricted to local journal files (not remote metadata).
func readHistoryReceipt(path string) (DownloadReceipt, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return DownloadReceipt{}, err
	}
	var rec DownloadReceipt
	if err := json.Unmarshal(b, &rec); err != nil {
		return DownloadReceipt{}, err
	}
	if rec.Version != 1 || rec.ClientID == "" || rec.JobID == "" || rec.InstructionHash == "" || rec.TargetPath == "" {
		return DownloadReceipt{}, fmt.Errorf("invalid_history_receipt")
	}
	return rec, nil
}

// MarkPostProcessed is called only AFTER the local archive queue has durably
// confirmed successful publication, but BEFORE it deletes any original files.
// The retained JSON receipt is deliberate: removing it would cause re-leased
// server jobs to download the intentionally removed archive again.
func (j DownloadJournal) MarkPostProcessed(clientID, jobID, source, digest, destination string) error {
	journalTransitions.Lock()
	defer journalTransitions.Unlock()
	rec, ok, err := j.Load(clientID, jobID)
	if err != nil {
		return err
	}
	if !ok || rec.TargetPath != source || rec.SHA256 != digest || rec.Phase != "transferred" && rec.Phase != "postprocessed" {
		return fmt.Errorf("download_journal_archive_receipt_mismatch")
	}
	st, err := os.Lstat(destination)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || (!st.Mode().IsRegular() && !st.IsDir()) {
		return fmt.Errorf("download_journal_archive_destination_missing")
	}
	if st.Mode().IsRegular() {
		rec.ArchiveDestinationKind = "file"
	} else {
		rec.ArchiveDestinationKind = "directory"
	}
	rec.Phase = "postprocessed"
	rec.ArchiveDestination = destination
	rec.UpdatedAt = time.Now().UTC()
	return j.Write(rec)
}

// ForgetAcknowledgedSuccess prunes a local receipt only after the server
// acknowledged SUCCESS and any queued postprocessing has reached terminal
// completion. It does not delete downloads, partial files, or archive state.
func (j DownloadJournal) ForgetAcknowledgedSuccess(clientID, jobID string, archiveReady bool) (bool, error) {
	journalTransitions.Lock()
	defer journalTransitions.Unlock()
	rec, exists, err := j.Load(clientID, jobID)
	if err != nil || !exists {
		return false, err
	}
	if !archiveReady || (rec.Phase != "transferred" && rec.Phase != "postprocessed") {
		return false, nil
	}
	acknowledged := false
	for _, a := range rec.Attempts {
		if a.Result == "SUCCESS" && a.ReportState == "acknowledged" {
			acknowledged = true
			break
		}
	}
	if !acknowledged {
		return false, nil
	}
	path, err := j.filename(clientID, jobID)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// PruneOldUnfinished removes stale metadata that cannot resume because neither
// partial nor its metadata remain. Pending server reports are never discarded.
func (j DownloadJournal) PruneOldUnfinished(ctx context.Context, clientID string, olderThan time.Time) (int, error) {
	journalTransitions.Lock()
	defer journalTransitions.Unlock()
	entries, err := os.ReadDir(j.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(j.Dir, entry.Name())
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			continue
		}
		rec, err := readHistoryReceipt(path)
		if err != nil || rec.ClientID != clientID || !rec.UpdatedAt.Before(olderThan) {
			continue
		}
		hasPending := false
		for _, a := range rec.Attempts {
			if a.ReportState == "pending" || a.FinishedAt == nil {
				hasPending = true
				break
			}
		}
		if hasPending || rec.Phase != "downloading" {
			continue
		}
		_, partialErr := os.Lstat(rec.TargetPath + ".part")
		_, metaErr := os.Lstat(rec.TargetPath + ".part.meta.json")
		if !errors.Is(partialErr, os.ErrNotExist) || !errors.Is(metaErr, os.ErrNotExist) {
			continue
		}
		expected, err := j.filename(clientID, rec.JobID)
		if err != nil || expected != path {
			continue
		}
		if err := os.Remove(path); err == nil {
			count++
		}
	}
	return count, nil
}
