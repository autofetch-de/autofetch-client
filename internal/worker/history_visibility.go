package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The local history UI is disposable. Download receipts are not: the worker
// needs them to verify completed downloads, resume safely and avoid duplicate
// transfers. Keep presentation preferences in a separate sidecar instead.
type historyVisibility struct {
	Version       int                  `json:"version"`
	ClearedBefore time.Time            `json:"cleared_before,omitempty"`
	Hidden        map[string]time.Time `json:"hidden,omitempty"`
}

func (j DownloadJournal) visibilityPath(clientID string) (string, error) {
	if j.Dir == "" || clientID == "" {
		return "", fmt.Errorf("history_visibility_missing_identity")
	}
	sum := sha256.Sum256([]byte(clientID))
	return filepath.Join(j.Dir, ".history-view-"+hex.EncodeToString(sum[:])+".json"), nil
}

func (j DownloadJournal) readVisibility(clientID string) (historyVisibility, error) {
	path, err := j.visibilityPath(clientID)
	if err != nil {
		return historyVisibility{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return historyVisibility{Version: 1}, nil
	}
	if err != nil {
		return historyVisibility{}, err
	}
	if len(data) > 2*1024*1024 {
		return historyVisibility{}, fmt.Errorf("history_visibility_invalid")
	}
	var v historyVisibility
	if err := json.Unmarshal(data, &v); err != nil || v.Version != 1 {
		return historyVisibility{}, fmt.Errorf("history_visibility_invalid")
	}
	return v, nil
}

func (j DownloadJournal) saveVisibility(clientID string, v historyVisibility) error {
	path, err := j.visibilityPath(clientID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(j.Dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(j.Dir, ".history-view-*")
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
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	if dir, err := os.Open(j.Dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// HideHistory changes only the locally displayed history, never a download
// receipt, partial file, archive state or server job. An updated/new attempt is
// allowed to reappear in the view.
func (j DownloadJournal) HideHistory(ctx context.Context, clientID, jobID string, all bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	journalTransitions.Lock()
	defer journalTransitions.Unlock()
	v, err := j.readVisibility(clientID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if all {
		v.ClearedBefore = now
		v.Hidden = nil
	} else {
		if jobID == "" {
			return fmt.Errorf("history_job_id_required")
		}
		if _, found, err := j.Load(clientID, jobID); err != nil {
			return err
		} else if !found {
			return fmt.Errorf("history_job_not_found")
		}
		if v.Hidden == nil {
			v.Hidden = make(map[string]time.Time)
		}
		v.Hidden[jobID] = now
	}
	return j.saveVisibility(clientID, v)
}

func (v historyVisibility) shows(rec DownloadReceipt) bool {
	if !v.ClearedBefore.IsZero() && !rec.UpdatedAt.After(v.ClearedBefore) {
		return false
	}
	if hiddenAt, ok := v.Hidden[rec.JobID]; ok && !rec.UpdatedAt.After(hiddenAt) {
		return false
	}
	return true
}
