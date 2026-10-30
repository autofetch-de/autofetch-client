package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Queue belongs solely to the local client. It never changes a server job
// result and never starts a network transfer. A failed extraction is retried
// only through an explicit, locally authorized request.
type Queue struct {
	Dir         string
	OnlyPlanned bool
	// BeforeRemove persists the download-journal tombstone before removing
	// an archive; without it, automatic deletion fails closed.
	BeforeRemove func(clientID, jobID, source, digest, destination string) error
	mu           sync.Mutex
}

// Policy is captured when extraction begins. Changing client preferences does
// not retrospectively delete archives from earlier completed downloads.
type Policy struct {
	Layout         string // separate (default) or same
	DeleteOriginal bool
}

func (p Policy) validated() (Policy, error) {
	if p.Layout == "" {
		p.Layout = "separate"
	}
	if p.Layout != "separate" && p.Layout != "same" {
		return p, fmt.Errorf("archive_layout_invalid")
	}
	return p, nil
}

type Item struct {
	Version            int           `json:"version"`
	ClientID           string        `json:"client_id"`
	JobID              string        `json:"job_id"`
	Source             string        `json:"source"`
	SHA256             string        `json:"sha256"`
	Status             string        `json:"status"` // queued, extracting, completed, failed
	Destination        string        `json:"destination,omitempty"`
	ErrorCode          string        `json:"error_code,omitempty"`
	UpdatedAt          time.Time     `json:"updated_at"`
	Layout             string        `json:"layout,omitempty"`
	PolicySnapshot     bool          `json:"policy_snapshot,omitempty"`
	NestedDepth        int           `json:"nested_depth,omitempty"`
	DeleteOriginal     bool          `json:"delete_original,omitempty"`
	StagingDir         string        `json:"staging_dir,omitempty"`
	CleanupPending     bool          `json:"cleanup_pending,omitempty"`
	CleanupFiles       []cleanupFile `json:"cleanup_files,omitempty"`
	OriginalDeleted    bool          `json:"original_deleted,omitempty"`
	VolumeSignature    string        `json:"volume_signature,omitempty"`
	Phase              string        `json:"phase,omitempty"`
	ProgressBytes      int64         `json:"progress_bytes,omitempty"`
	ProgressTotalBytes int64         `json:"progress_total_bytes,omitempty"`
}

func (q *Queue) itemPath(clientID, jobID string) (string, error) {
	if q.Dir == "" || clientID == "" || jobID == "" {
		return "", fmt.Errorf("archive_queue_identity_missing")
	}
	hash := sha256.Sum256([]byte(clientID + "\x00" + jobID))
	return filepath.Join(q.Dir, hex.EncodeToString(hash[:])+".json"), nil
}
func (q *Queue) load(path string) (Item, bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 131072 {
		return Item{}, false, fmt.Errorf("archive_queue_invalid_file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Item{}, false, err
	}
	var it Item
	if err = json.Unmarshal(b, &it); err != nil {
		return Item{}, false, fmt.Errorf("archive_queue_invalid_json: %w", err)
	}
	expected, err := q.itemPath(it.ClientID, it.JobID)
	if err != nil || expected != path || it.Version != 1 || !Supported(it.Source) || len(it.SHA256) != 64 {
		return Item{}, false, fmt.Errorf("archive_queue_invalid_record")
	}
	if it.NestedDepth < 0 || it.NestedDepth > 3 {
		return Item{}, false, fmt.Errorf("archive_queue_invalid_depth")
	}
	if it.Layout != "" && it.Layout != "separate" && it.Layout != "same" {
		return Item{}, false, fmt.Errorf("archive_queue_invalid_layout")
	}
	if len(it.CleanupFiles) > maxVolumeFiles {
		return Item{}, false, fmt.Errorf("archive_queue_too_many_volumes")
	}
	switch it.Status {
	case "queued", "extracting", "completed", "failed":
	default:
		return Item{}, false, fmt.Errorf("archive_queue_invalid_status")
	}
	return it, true, nil
}
func (q *Queue) save(path string, it Item) error {
	if err := os.MkdirAll(q.Dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(it)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(q.Dir, ".archive-record-")
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
	return nil
}

// EnqueuePlanned captures the exact server policy before any extraction work.
// A later user setting change cannot alter this job's cleanup decision.
func (q *Queue) EnqueuePlanned(clientID, jobID, source, digest string, policy Policy, nestedDepth int) error {
	p, err := policy.validated()
	if err != nil {
		return err
	}
	if nestedDepth < 0 || nestedDepth > 3 {
		return fmt.Errorf("archive_nested_depth_invalid")
	}
	if !Supported(source) || len(digest) != 64 {
		return fmt.Errorf("archive_queue_invalid_source")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	path, err := q.itemPath(clientID, jobID)
	if err != nil {
		return err
	}
	it, found, err := q.load(path)
	if err != nil {
		return err
	}
	if found {
		if it.Source != source || it.SHA256 != digest {
			return fmt.Errorf("archive_queue_source_changed")
		}
		// Never rewrite the policy of an existing attempt, including retries.
		if !it.PolicySnapshot || it.Layout != p.Layout || it.DeleteOriginal != p.DeleteOriginal || it.NestedDepth != nestedDepth {
			return fmt.Errorf("archive_queue_policy_changed")
		}
		return nil
	}
	return q.save(path, Item{Version: 1, ClientID: clientID, JobID: jobID, Source: source, SHA256: digest,
		Status: "queued", PolicySnapshot: true, Layout: p.Layout, DeleteOriginal: p.DeleteOriginal, NestedDepth: nestedDepth, UpdatedAt: time.Now().UTC()})
}

// Enqueue is idempotent. Previously failed/completed extractions are never
// restarted simply because the server leased the download again.
func (q *Queue) Enqueue(clientID, jobID, source, digest string) error {
	if !Supported(source) {
		return ErrUnsupported
	}
	if len(digest) != 64 {
		return fmt.Errorf("archive_queue_digest_missing")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	path, err := q.itemPath(clientID, jobID)
	if err != nil {
		return err
	}
	it, exists, err := q.load(path)
	if err != nil {
		return err
	}
	if exists {
		if it.Source != source || it.SHA256 != digest {
			return fmt.Errorf("archive_queue_source_changed")
		}
		return nil
	}
	return q.save(path, Item{Version: 1, ClientID: clientID, JobID: jobID, Source: source, SHA256: digest, Status: "queued", UpdatedAt: time.Now().UTC()})
}

func (q *Queue) list(clientID string) ([]string, error) {
	entries, err := os.ReadDir(q.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		paths = append(paths, filepath.Join(q.Dir, e.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

// Retry changes a failed record back to queued without touching the downloaded
// archive. ProcessOne revalidates its journal digest before any extraction.
func (q *Queue) Retry(clientID, jobID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	path, err := q.itemPath(clientID, jobID)
	if err != nil {
		return err
	}
	it, found, err := q.load(path)
	if err != nil {
		return err
	}
	if !found || it.ClientID != clientID {
		return fmt.Errorf("archive_queue_job_not_found")
	}
	if it.Status != "failed" {
		return fmt.Errorf("archive_queue_not_failed")
	}
	it.Status = "queued"
	it.ErrorCode = ""
	it.Destination = ""
	if !it.PolicySnapshot {
		it.Layout = ""
		it.DeleteOriginal = false
	}
	it.StagingDir = ""
	it.CleanupPending = false
	it.CleanupFiles = nil
	it.OriginalDeleted = false
	it.VolumeSignature = ""
	it.Phase = ""
	it.ProgressBytes = 0
	it.ProgressTotalBytes = 0
	it.UpdatedAt = time.Now().UTC()
	return q.save(path, it)
}

// ProcessOne handles at most one queued item. An "extracting" record left by
// an abrupt process exit is eligible for recovery at the next startup.
func (q *Queue) ProcessOne(ctx context.Context, clientID string, limits Limits) (bool, error) {
	return q.ProcessOnePolicy(ctx, clientID, limits, 0, Policy{})
}

// ProcessOneOptions optionally expands nested archives up to three additional
// levels; a positive depth is never enabled implicitly.
func (q *Queue) ProcessOneOptions(ctx context.Context, clientID string, limits Limits, nestedDepth int) (bool, error) {
	return q.ProcessOnePolicy(ctx, clientID, limits, nestedDepth, Policy{})
}

func (q *Queue) ProcessOnePolicy(ctx context.Context, clientID string, limits Limits, nestedDepth int, policy Policy) (bool, error) {
	var err error
	policy, err = policy.validated()
	if err != nil {
		return false, err
	}
	if nestedDepth < 0 || nestedDepth > 3 {
		return false, fmt.Errorf("archive_nested_depth_invalid")
	}
	q.mu.Lock()
	paths, err := q.list(clientID)
	if err != nil {
		q.mu.Unlock()
		return false, err
	}
	var chosen Item
	var filename string
	for _, p := range paths {
		it, found, loadErr := q.load(p)
		if loadErr != nil {
			continue
		} // corrupt records cannot block unrelated jobs
		if found && it.ClientID == clientID && (!q.OnlyPlanned || it.PolicySnapshot) && (it.Status == "queued" || it.Status == "extracting" || (it.Status == "completed" && (it.CleanupPending || it.StagingDir != ""))) {
			chosen, filename = it, p
			break
		}
	}
	if filename == "" {
		q.mu.Unlock()
		return false, nil
	}
	if chosen.Status == "completed" {
		q.mu.Unlock()
		err := q.finishCompleted(ctx, filename, &chosen)
		return true, err
	}
	if chosen.PolicySnapshot {
		// The server-authored plan is frozen in the queue record.
		policy = Policy{Layout: chosen.Layout, DeleteOriginal: chosen.DeleteOriginal}
		nestedDepth = chosen.NestedDepth
	} else if chosen.Status == "queued" {
		// Legacy explicit ProcessOnePolicy API remains for local library tests.
		// The actual worker sets OnlyPlanned and can never run these records.
		chosen.Layout = policy.Layout
		chosen.DeleteOriginal = policy.DeleteOriginal
	}
	chosen.Status = "extracting"
	chosen.Phase = "verifying"
	chosen.ProgressBytes = 0
	chosen.ProgressTotalBytes = 0
	chosen.UpdatedAt = time.Now().UTC()
	err = q.save(filename, chosen)
	q.mu.Unlock()
	if err != nil {
		return false, err
	}
	lastProgressSave := time.Time{}
	lastProgressPhase := "verifying"
	onProgress := func(p Progress) {
		chosen.Phase = p.Phase
		chosen.ProgressBytes = p.Bytes
		chosen.ProgressTotalBytes = p.TotalBytes
		// Persist progress at most once every two seconds while streaming;
		// phase changes are always persisted immediately.
		if p.Phase == lastProgressPhase && time.Since(lastProgressSave) < 2*time.Second {
			return
		}
		lastProgressSave = time.Now()
		lastProgressPhase = p.Phase
		chosen.UpdatedAt = lastProgressSave.UTC()
		q.mu.Lock()
		saveErr := q.save(filename, chosen)
		q.mu.Unlock()
		if saveErr != nil {
			return
		}
		if p.Phase == "extracting" && p.TotalBytes > 0 {
			// Don't log a percentage when nested extraction is still pending.
			log.Printf("archive extraction: %s %d/%d bytes (%d%%)", filepath.Base(chosen.Source), p.Bytes, p.TotalBytes, p.Bytes*100/p.TotalBytes)
		} else if p.Phase == "publishing" && p.TotalBytes > 0 {
			// Fast archives may finish before the next two-second progress log.
			log.Printf("archive extraction: %s %d/%d bytes (%d%%), publishing", filepath.Base(chosen.Source), p.Bytes, p.TotalBytes, p.Bytes*100/p.TotalBytes)
		}
	}
	result, extractionErr := func() (string, error) {
		st, err := os.Lstat(chosen.Source)
		if err != nil {
			return "", err
		}
		if !st.Mode().IsRegular() {
			return "", fmt.Errorf("archive_source_not_regular")
		}
		f, err := os.Open(chosen.Source)
		if err != nil {
			return "", err
		}
		hash := sha256.New()
		buf := make([]byte, 1024*1024)
		for {
			if err := ctx.Err(); err != nil {
				f.Close()
				return "", err
			}
			n, er := f.Read(buf)
			if n > 0 {
				_, _ = hash.Write(buf[:n])
			}
			if er == io.EOF {
				break
			}
			if er != nil {
				f.Close()
				return "", er
			}
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		if hex.EncodeToString(hash.Sum(nil)) != chosen.SHA256 {
			return "", fmt.Errorf("archive_source_changed")
		}
		extracted, err := ExtractVerifiedNestedProgress(ctx, chosen.Source, chosen.SHA256, limits, nestedDepth, onProgress)
		if err != nil {
			return "", err
		}
		onProgress(Progress{Phase: "publishing", Bytes: chosen.ProgressBytes, TotalBytes: chosen.ProgressTotalBytes})
		if chosen.Layout == "same" {
			dest, err := PublishSameDirectory(ctx, chosen.Source, extracted)
			if err != nil {
				return "", err
			}
			chosen.StagingDir = extracted
			return dest, nil
		}
		return extracted, nil
	}()
	if extractionErr == nil && chosen.DeleteOriginal {
		chosen.CleanupFiles, extractionErr = snapshotCleanupFiles(ctx, chosen.Source, chosen.SHA256)
		if extractionErr == nil {
			paths := make([]string, 0, len(chosen.CleanupFiles))
			for _, f := range chosen.CleanupFiles {
				paths = append(paths, f.Path)
			}
			chosen.VolumeSignature, extractionErr = volumeSignature(ctx, paths)
		}
	}
	q.mu.Lock()
	// Enqueue only checks source identity and does not modify an existing record.
	if extractionErr == nil {
		chosen.Status = "completed"
		chosen.Destination = result
		chosen.ErrorCode = ""
		chosen.Phase = "completed"
		chosen.CleanupPending = chosen.DeleteOriginal
	} else if errors.Is(extractionErr, context.Canceled) || errors.Is(extractionErr, context.DeadlineExceeded) {
		chosen.Status = "queued"
		chosen.Phase = ""
	} else {
		chosen.Status = "failed"
		chosen.Phase = "failed"
		chosen.ErrorCode = compactQueueError(extractionErr)
	}
	chosen.UpdatedAt = time.Now().UTC()
	saveErr := q.save(filename, chosen)
	q.mu.Unlock()
	if saveErr != nil {
		return true, saveErr
	}
	// Never remove originals before the completed publication is recorded.
	if extractionErr == nil {
		if err := q.finishCompleted(ctx, filename, &chosen); err != nil {
			return true, err
		}
		log.Printf("archive extraction finished: %s -> %s", filepath.Base(chosen.Source), chosen.Destination)
	}
	return true, extractionErr
}

// finishCompleted runs again after a crash during cleanup. The prior record
// contains immutable source digests and the publication destination.
func (q *Queue) finishCompleted(ctx context.Context, filename string, it *Item) error {
	if it.CleanupPending {
		if it.Layout == "same" {
			if it.StagingDir != "" {
				if err := VerifyPublishedSameDirectory(ctx, it.Source, it.StagingDir); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("archive_cleanup_staging_missing")
			}
		} else if !finishedDirectory(it.Destination, it.SHA256, it.VolumeSignature) {
			// Full multipart signature is verified at extraction; if the
			// result has been removed by someone else, don't delete inputs.
			return fmt.Errorf("archive_cleanup_destination_missing")
		}
		if q.BeforeRemove == nil {
			return fmt.Errorf("archive_cleanup_journal_unavailable")
		}
		// Verify all still-existing files prior to modifying the download receipt.
		for _, f := range it.CleanupFiles {
			actual, err := fileDigest(ctx, f.Path)
			if os.IsNotExist(err) {
				continue
			} // partial cleanup from an earlier run
			if err != nil || actual != f.SHA256 {
				return fmt.Errorf("archive_source_changed")
			}
		}
		if err := q.BeforeRemove(it.ClientID, it.JobID, it.Source, it.SHA256, it.Destination); err != nil {
			return err
		}
		if err := cleanupOriginalFiles(ctx, it.CleanupFiles); err != nil {
			return err
		}
		it.CleanupPending = false
		it.OriginalDeleted = true
		it.UpdatedAt = time.Now().UTC()
		q.mu.Lock()
		err := q.save(filename, *it)
		q.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if it.StagingDir != "" {
		if err := os.RemoveAll(it.StagingDir); err != nil {
			return err
		}
		it.StagingDir = ""
		it.UpdatedAt = time.Now().UTC()
		q.mu.Lock()
		err := q.save(filename, *it)
		q.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func compactQueueError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	// Preserve stable machine-readable leading codes from wrapped decoder
	// errors without leaking arbitrary process output into the local history.
	if head, _, ok := strings.Cut(s, ":"); ok {
		s = head
	}
	if len(s) > 100 {
		s = s[:100]
	}
	for _, c := range s {
		if !(c == '_' || c == '-' || c == '.' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return "archive_extraction_failed"
		}
	}
	return s
}

// History contains no URLs, tokens, or IRC secrets. The web layer must enforce
// localhost access before exposing it.
func (q *Queue) History(clientID string, max int) ([]Item, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	paths, err := q.list(clientID)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, p := range paths {
		it, ok, err := q.load(p)
		if err == nil && ok && it.ClientID == clientID {
			items = append(items, it)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
	if max <= 0 || max > 100 {
		max = 100
	}
	if len(items) > max {
		items = items[:max]
	}
	if items == nil {
		items = []Item{}
	}
	return items, nil
}

// FinishedForJob is true only when extraction AND original cleanup have
// completed durably. Failed or pending processing retains download receipts.
func (q *Queue) FinishedForJob(clientID, jobID string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	path, err := q.itemPath(clientID, jobID)
	if err != nil {
		return false, err
	}
	it, exists, err := q.load(path)
	if err != nil {
		return false, err
	}
	return exists && it.Status == "completed" && !it.CleanupPending && it.StagingDir == "", nil
}
