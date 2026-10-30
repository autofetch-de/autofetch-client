package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DownloadAttempt captures a *server-leased* execution. Retrying is always
// scheduled by the server, never by replaying this local history.
// ErrorCode deliberately excludes arbitrary error messages, URLs and secrets.
type DownloadAttempt struct {
	AttemptID   string     `json:"attempt_id"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Result      string     `json:"result,omitempty"`
	ErrorCode   string     `json:"error_code,omitempty"`
	ReportState string     `json:"report_state,omitempty"` // pending | acknowledged | lease_lost
}

const maxRecordedAttempts = 32

// The old lease must have reported a failure before switching sources. A
// crash or unacknowledged outcome must never silently rebind an unfinished file.
func lastAttemptFailed(attempts []DownloadAttempt) bool {
	if len(attempts) == 0 {
		return false
	}
	last := attempts[len(attempts)-1]
	return last.FinishedAt != nil && last.ReportState == "acknowledged" &&
		(last.Result == "FAILED" || last.Result == "NOT_FOUND_YET" || last.Result == "RETRYABLE_ERROR")
}

// BeginAttempt is idempotent for a duplicate lease and never clears a
// previously verified transfer receipt. It marks interrupted attempts for
// history; it does NOT send new IRC requests without another server lease.
func (j DownloadJournal) BeginAttempt(clientID, jobID, fingerprint, target, attemptID string, allowFallback ...bool) error {
	journalTransitions.Lock()
	defer journalTransitions.Unlock()
	if attemptID == "" {
		return fmt.Errorf("download_attempt_missing_id")
	}
	rec, found, err := j.Load(clientID, jobID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if !found {
		rec = DownloadReceipt{Version: 1, ClientID: clientID, JobID: jobID, InstructionHash: fingerprint, TargetPath: target, Phase: "downloading"}
	} else if rec.TargetPath != target || (rec.InstructionHash != fingerprint && (rec.Phase != "downloading" || len(allowFallback) == 0 || !allowFallback[0] || !lastAttemptFailed(rec.Attempts))) {
		return fmt.Errorf("download_journal_source_changed")
	} else if rec.InstructionHash != fingerprint {
		// The server deliberately issued the same job with a new source after
		// failure. A completed receipt can NEVER be rebound. Partial bytes
		// remain source-bound and are quarantined by prepareDCCPartial.
		rec.InstructionHash = fingerprint
	}
	for i := range rec.Attempts {
		a := &rec.Attempts[i]
		if a.AttemptID == attemptID {
			return nil
		}
		if a.FinishedAt == nil {
			a.Result = "INTERRUPTED"
			a.ErrorCode = "client_interrupted"
			a.FinishedAt = &now
		}
	}
	rec.Attempts = append(rec.Attempts, DownloadAttempt{AttemptID: attemptID, StartedAt: now})
	if len(rec.Attempts) > maxRecordedAttempts {
		rec.Attempts = rec.Attempts[len(rec.Attempts)-maxRecordedAttempts:]
	}
	rec.UpdatedAt = now
	return j.Write(rec)
}

// MarkTransferred preserves the existing execution history rather than
// replacing the receipt. This checkpoint must precede reporting SUCCESS.
func (j DownloadJournal) MarkTransferred(clientID, jobID, fingerprint, target string, size int64, digest string) error {
	journalTransitions.Lock()
	defer journalTransitions.Unlock()
	rec, found, err := j.Load(clientID, jobID)
	if err != nil {
		return err
	}
	if !found || rec.InstructionHash != fingerprint || rec.TargetPath != target {
		return fmt.Errorf("download_journal_missing_or_changed")
	}
	if size <= 0 || len(digest) != 64 {
		return fmt.Errorf("download_journal_invalid_completion")
	}
	rec.Phase = "transferred"
	rec.Bytes = size
	rec.SHA256 = digest
	rec.UpdatedAt = time.Now().UTC()
	return j.Write(rec)
}

// compactErrorCode intentionally stores only a conservative leading error code.
// Arbitrary network/server error text (including URL query strings or secrets)
// never enters the persistent history.
func compactErrorCode(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if n := strings.IndexAny(raw, ": \t\r\n"); n >= 0 {
		raw = raw[:n]
	}
	if len(raw) > 80 {
		raw = raw[:80]
	}
	for _, r := range raw {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return "error"
		}
	}
	return raw
}

// SetReportState records both the intended server result and whether the server
// accepted it. A failed HTTP request remains 'pending'; a 409 is 'lease_lost',
// never 'acknowledged'. A future lease may retry reporting a verified transfer.
func (j DownloadJournal) SetReportState(clientID, jobID, attemptID, result, errorText, state string) error {
	journalTransitions.Lock()
	defer journalTransitions.Unlock()
	if state != "pending" && state != "acknowledged" && state != "lease_lost" {
		return fmt.Errorf("invalid_report_state")
	}
	rec, found, err := j.Load(clientID, jobID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	} // invalid payloads may never have a receipt
	for i := len(rec.Attempts) - 1; i >= 0; i-- {
		a := &rec.Attempts[i]
		if a.AttemptID != attemptID {
			continue
		}
		now := time.Now().UTC()
		a.Result = result
		a.ErrorCode = compactErrorCode(errorText)
		a.ReportState = state
		a.FinishedAt = &now
		rec.UpdatedAt = now
		return j.Write(rec)
	}
	return nil // a completion for an invalid/unknown attempt is never invented
}

type DownloadHistoryItem struct {
	JobID         string    `json:"job_id"`
	Filename      string    `json:"filename"`
	Phase         string    `json:"phase"`
	Bytes         int64     `json:"bytes,omitempty"`
	LastResult    string    `json:"last_result,omitempty"`
	LastErrorCode string    `json:"last_error_code,omitempty"`
	ReportState   string    `json:"report_state,omitempty"`
	AttemptCount  int       `json:"attempt_count"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// History lists local receipt summaries for the current paired client only.
// Journal files from previous pairings remain on disk but aren't shown here.
// It deliberately exposes no URLs, arbitrary server messages or credentials.
func (j DownloadJournal) History(ctx context.Context, clientID string, limit int) ([]DownloadHistoryItem, error) {
	if clientID == "" {
		return nil, fmt.Errorf("client_id missing")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	visibility, err := j.readVisibility(clientID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(j.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return []DownloadHistoryItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]DownloadHistoryItem, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		// Read only files written by this journal. Reject links/special files.
		path := filepath.Join(j.Dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			continue
		}
		rec, err := readHistoryReceipt(path)
		if err != nil {
			continue
		} // a corrupt record can't make the entire history unavailable
		if rec.ClientID != clientID || !visibility.shows(rec) {
			continue
		}
		expected, err := j.filename(rec.ClientID, rec.JobID)
		if err != nil || expected != path {
			continue
		}
		item := DownloadHistoryItem{JobID: rec.JobID, Filename: filepath.Base(rec.TargetPath), Phase: rec.Phase, Bytes: rec.Bytes, AttemptCount: len(rec.Attempts), UpdatedAt: rec.UpdatedAt}
		if n := len(rec.Attempts); n > 0 {
			a := rec.Attempts[n-1]
			item.LastResult = a.Result
			item.LastErrorCode = a.ErrorCode
			item.ReportState = a.ReportState
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, k int) bool {
		if items[i].UpdatedAt.Equal(items[k].UpdatedAt) {
			return items[i].JobID < items[k].JobID
		}
		return items[i].UpdatedAt.After(items[k].UpdatedAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
