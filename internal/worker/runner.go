package worker

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
	"github.com/autofetch-de/autofetch-client/internal/download"
	"github.com/autofetch-de/autofetch-client/internal/observe"
	clientruntime "github.com/autofetch-de/autofetch-client/internal/runtime"
)

// ErrClientRevoked is returned when the server responds with
// HTTP 403 {"error":"CLIENT_REVOKED_OR_MISSING"}.
var ErrClientRevoked = errors.New("client revoked or deleted by server")

const stopModeGetterContextKey = "autofetch.stop_mode_getter"

func localStopMode(ctx context.Context) string {
	getter, _ := ctx.Value(stopModeGetterContextKey).(func() string)
	if getter == nil {
		return ""
	}
	return strings.TrimSpace(getter())
}

type Runner struct {
	API    *api.Client
	DLHTTP *http.Client

	ClientID string

	DownloadDir string

	// Optional IRC identity (used for XDCC)
	IRCNick string

	HeartbeatInterval time.Duration
	HeartbeatExtend   int
	DedupeTTLSeconds  int

	RuntimeConfig *clientruntime.ConfigManager
	Observer      observe.Observer
}

func (r *Runner) currentRuntimeConfig() clientruntime.Config {
	if r.RuntimeConfig != nil {
		return r.RuntimeConfig.Current()
	}
	return clientruntime.Config{
		PollIntervalSec:      2,
		HeartbeatIntervalSec: int(r.HeartbeatInterval.Seconds()),
		HeartbeatExtendSec:   r.HeartbeatExtend,
		DedupeClaimTTLSec:    r.DedupeTTLSeconds,
	}.WithDefaults()
}

func (r *Runner) bandwidthLimitBytesPerSec() int64 {
	cfg := r.currentRuntimeConfig()
	if cfg.BandwidthLimitKiBPerSec <= 0 {
		return 0
	}
	return int64(cfg.BandwidthLimitKiBPerSec) * 1024
}

// completeWithRetry retries complete() a few times to survive transient transport errors.
// It does NOT retry ErrClientRevoked, and it treats 409 (late complete) as success (complete() returns nil).
func (r *Runner) completeWithRetry(ctx context.Context, job *api.LeasedJob, status, errStr string, data map[string]any) error {
	delays := []time.Duration{0, 2 * time.Second, 5 * time.Second}
	var lastErr error

	for i, d := range delays {
		if d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		err := r.complete(ctx, job, status, errStr, data)
		if err == nil {
			return nil
		}
		if err == ErrClientRevoked {
			return ErrClientRevoked
		}

		lastErr = err
		log.Printf("complete retry %d/%d failed job=%s attempt=%s status=%s err=%v",
			i+1, len(delays), job.JobID, job.Lease.AttemptID, status, err)
	}

	return lastErr
}

func (r *Runner) Run(ctx context.Context) error {
	bo := &Backoff{Min: 500 * time.Millisecond, Max: 30 * time.Second}
	lastConfigRefresh := time.Time{}
	obs := r.Observer
	if obs == nil {
		obs = observe.NopObserver{}
	}

	if err := CleanupPartialsTTL(r.DownloadDir, DefaultPartialTTL); err != nil {
		log.Printf("cleanup error: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if r.RuntimeConfig != nil && (lastConfigRefresh.IsZero() || time.Since(lastConfigRefresh) >= 30*time.Second) {
			if err := r.RuntimeConfig.Refresh(ctx); err != nil {
				log.Printf("runtime config refresh failed: %v", err)
			} else {
				lastConfigRefresh = time.Now()
			}
		}

		resp, err := r.API.LeaseJob(ctx)
		if err != nil {
			obs.Poll(false, time.Now(), err)
			obs.Error(err)
			if api.IsRevoked(err) {
				return ErrClientRevoked
			}
			d := bo.Next()
			log.Printf("lease error: %v (sleep %s)", err, d)
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		bo.Reset()
		obs.Poll(true, time.Now(), nil)

		if resp.Job == nil {
			obs.JobCleared()
			sleep := time.Duration(resp.RetryAfterSeconds) * time.Second
			if sleep <= 0 {
				cfg := r.currentRuntimeConfig()
				sleep = time.Duration(cfg.PollIntervalSec) * time.Second
				if sleep <= 0 {
					sleep = 2 * time.Second
				}
			}
			select {
			case <-time.After(sleep):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		job := resp.Job
		obs.JobLeased(job.JobID, job.Type)
		log.Printf("leased job=%s type=%s attempt=%s", job.JobID, job.Type, job.Lease.AttemptID)

		if job.Payload.Download == nil {
			if err := r.completeWithRetry(ctx, job, "FAILED", "missing payload.download", nil); err == ErrClientRevoked {
				return ErrClientRevoked
			}
			continue
		}

		parsed, err := download.ParseDownloadInstruction(job.Payload.Download.Instruction, job.Payload.Download.DownloadPath)
		if err != nil {
			log.Printf("invalid download instruction for job=%s: %v", job.JobID, err)
			if err := r.completeWithRetry(ctx, job, "FAILED", err.Error(), nil); err == ErrClientRevoked {
				return ErrClientRevoked
			}
			continue
		}

		// Dedupe key: prefer instruction, fallback payload.dedupe
		dedupeKey := strings.TrimSpace(parsed.DedupeKey)
		if dedupeKey == "" && job.Payload.Dedupe != nil {
			dedupeKey = strings.TrimSpace(job.Payload.Dedupe.DedupeKey)
		}

		// IMPORTANT: Do NOT auto-create subfolders from title/series.
		// If output.dir is empty, write directly into download_base_path.
		targetDir := r.DownloadDir
		if strings.TrimSpace(parsed.Output.Dir) != "" {
			targetDir = filepath.Join(r.DownloadDir, strings.TrimSpace(parsed.Output.Dir))
		}

		filename := strings.TrimSpace(parsed.Output.FilenameVideo)
		finalPath := filepath.Join(targetDir, filename)
		displayPath := download.RelativeOutputPath(parsed.Output)

		// 1) Dedupe claim (optional) — MUST happen before local exists-check
		if dedupeKey != "" {
			claimResp, err := r.API.DedupeClaim(ctx, api.DedupeClaimRequest{
				ClientID:   r.ClientID,
				JobID:      job.JobID,
				AttemptID:  job.Lease.AttemptID,
				DedupeKey:  dedupeKey,
				TTLSeconds: r.currentRuntimeConfig().DedupeClaimTTLSec,
				Quality:    parsed.Quality,
				Meta: map[string]any{
					"provider":     parsed.Provider,
					"candidate_id": parsed.CandidateID,
					"website_url":  parsed.WebsiteURL,
				},
			})
			if err != nil {
				if api.IsRevoked(err) {
					return ErrClientRevoked
				}
				if err2 := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", "dedupe_claim_failed: "+err.Error(), nil); err2 == ErrClientRevoked {
					return ErrClientRevoked
				}
				continue
			}

			switch claimResp.Status {
			case "ALREADY_SUCCEEDED":
				data := map[string]any{
					"reason":     "DEDUPED",
					"dedupe_key": dedupeKey,
				}
				if claimResp.Result != nil && claimResp.Result.Data != nil {
					for k, v := range claimResp.Result.Data {
						data[k] = v
					}
				}
				log.Printf("download deduped: %s", displayPath)
				if err := r.completeWithRetry(ctx, job, "SUCCESS", "", data); err == ErrClientRevoked {
					return ErrClientRevoked
				}
				continue

			case "IN_PROGRESS":
				log.Printf("download in progress elsewhere: %s", displayPath)
				if err := r.completeWithRetry(ctx, job, "NOT_FOUND_YET", "dedupe_in_progress", map[string]any{
					"dedupe_key": dedupeKey,
				}); err == ErrClientRevoked {
					return ErrClientRevoked
				}
				continue

			case "CLAIMED":
				// proceed
			default:
				if err := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", "unknown_dedupe_status: "+claimResp.Status, nil); err == ErrClientRevoked {
					return ErrClientRevoked
				}
				continue
			}
		}

		// 2) Local idempotency: if file already exists => SUCCESS (EXISTS)
		// Now the server has a claim and can mark it succeeded on complete.
		if _, err := os.Stat(finalPath); err == nil {
			log.Printf("download exists: %s", displayPath)
			if err := r.completeWithRetry(ctx, job, "SUCCESS", "", map[string]any{
				"reason":     "EXISTS",
				"file_path":  finalPath,
				"dedupe_key": dedupeKey,
			}); err == ErrClientRevoked {
				return ErrClientRevoked
			}
			continue
		}

		// 3) Download (with heartbeat)
		err = r.downloadWithHeartbeat(ctx, job, parsed, finalPath, displayPath, dedupeKey)
		if err != nil {
			if err == ErrClientRevoked {
				return ErrClientRevoked
			}
			continue
		}
	}
}

func (r *Runner) downloadWithHeartbeat(ctx context.Context, job *api.LeasedJob, inst download.ParsedInstruction, finalPath, displayPath, dedupeKey string) error {
	obs := r.Observer
	if obs == nil {
		obs = observe.NopObserver{}
	}
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	log.Printf("download start: %s", displayPath)
	obs.DownloadStarted(displayPath)

	cfg := r.currentRuntimeConfig()
	ticker := time.NewTicker(cfg.HeartbeatInterval())
	defer ticker.Stop()

	var (
		res   *download.Result
		prog  = download.NewProgress(inst.ExpectedSize)
		dlErr error
		done  = make(chan struct{})
	)

	go func() {
		defer close(done)
		switch strings.TrimSpace(inst.Mode) {
		case "", "single":
			res, prog, dlErr = download.DownloadToFile(jobCtx, r.DLHTTP, inst.VideoURL, finalPath, inst.ExpectedSize, r.bandwidthLimitBytesPerSec(), prog)
		case "xdcc":
			prog.Expected = 0
			res, prog, dlErr = download.DownloadXDCCToFile(jobCtx, download.XDCCOptions{
				Nick:             r.IRCNick,
				Host:             inst.IRC.Host,
				Port:             inst.IRC.Port,
				TLS:              inst.IRC.TLS,
				Network:          inst.IRC.Network,
				JoinChannels:     inst.IRC.PrerequisiteChannels,
				Channel:          inst.IRC.Channel,
				Bot:              inst.IRC.Bot,
				Package:          inst.IRC.Package,
				ExpectedFilename: inst.IRC.Filename,
			}, finalPath, r.bandwidthLimitBytesPerSec(), prog)
		default:
			dlErr = errors.New("unsupported_mode: " + inst.Mode)
		}
	}()

	// Heartbeat progress state.
	var (
		lastBytes        int64
		lastAt           = time.Now()
		lastPct          int
		emaSpeed         float64
		serverStopStatus string
		serverStopErr    error
	)
	const emaWindowSeconds = 10.0

	progressTicker := time.NewTicker(250 * time.Millisecond)
	defer progressTicker.Stop()

	reportProgress := func() {
		if prog == nil {
			obs.DownloadProgress(displayPath, 0, 0, 0, 0)
			return
		}
		cur := prog.Downloaded.Load()
		exp := prog.Expected
		now := time.Now()
		dBytes := cur - lastBytes
		dt := now.Sub(lastAt).Seconds()
		instSpeed := 0.0
		if dt > 0 {
			instSpeed = float64(dBytes) / dt
		}
		alpha := 1.0
		if dt > 0 {
			alpha = 1.0 - (1.0 / (1.0 + (dt / emaWindowSeconds)))
		}
		if emaSpeed <= 0 {
			emaSpeed = instSpeed
		} else {
			emaSpeed = emaSpeed + alpha*(instSpeed-emaSpeed)
		}
		lastBytes, lastAt = cur, now
		eta := time.Duration(0)
		if exp > 0 && emaSpeed > 0 && cur < exp {
			eta = time.Duration(float64(exp-cur)/emaSpeed) * time.Second
		}
		obs.DownloadProgress(displayPath, cur, exp, emaSpeed, eta)
	}

	// Best-effort heartbeat retry delays for transient network errors.
	retryDelays := []time.Duration{0, 2 * time.Second, 5 * time.Second, 10 * time.Second}

	for {
		select {
		case <-done:
			if dlErr != nil {
				if serverStopErr != nil && errors.Is(dlErr, context.Canceled) {
					return serverStopErr
				}

				log.Printf("download failed: %s err=%v", displayPath, dlErr)

				// If we canceled due to server-side loss/cancel, completion/state cleanup is already handled in the heartbeat branch.
				if errors.Is(dlErr, context.Canceled) {
					mode := localStopMode(ctx)
					statusText := "abgebrochen"
					errorText := "client_shutdown"
					if mode == "pause" {
						statusText = "pausiert"
						errorText = "client_paused"
					}
					obs.DownloadFinished(displayPath, statusText)
					obs.JobCleared()
					if err := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", errorText, map[string]any{
						"dedupe_key": dedupeKey,
						"reason":     strings.ToUpper(errorText),
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

				obs.Error(dlErr)

				if errors.Is(dlErr, download.ErrFilenameMismatch) {
					if err := r.completeWithRetry(ctx, job, "NOT_FOUND_YET", "filename_mismatch", map[string]any{
						"expected":   inst.IRC.Filename,
						"offered":    download.OfferedFilename(dlErr),
						"dedupe_key": dedupeKey,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}
				if errors.Is(dlErr, download.ErrXDCCFilenameMismatch) {
					// Server asked for a specific filename, but the bot offered a different one.
					// Treat as NOT_FOUND_YET so a JSON change (or counter advance) can retry later.
					if err := r.completeWithRetry(ctx, job, "NOT_FOUND_YET", "xdcc_filename_mismatch", map[string]any{
						"expected_filename": inst.IRC.Filename,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

				if dlErr.Error() == "http_404" {
					if err := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", "http_404", map[string]any{
						"url":        inst.VideoURL,
						"dedupe_key": dedupeKey,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}
				if err := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", dlErr.Error(), map[string]any{
					"dedupe_key": dedupeKey,
				}); err == ErrClientRevoked {
					return ErrClientRevoked
				}
				return dlErr
			}

			log.Printf("download finished: %s", displayPath)
			obs.DownloadFinished(displayPath, "fertig")
			obs.JobCleared()

			data := map[string]any{
				"file_path":        res.FilePath,
				"bytes":            res.Bytes,
				"dedupe_key":       dedupeKey,
				"quality_selected": inst.Quality.Selected,
				"quality": map[string]any{
					"requested": inst.Quality.Requested,
					"selected":  inst.Quality.Selected,
				},
				"website_url":  inst.WebsiteURL,
				"candidate_id": inst.CandidateID,
				"provider":     inst.Provider,
			}

			if err := r.completeWithRetry(ctx, job, "SUCCESS", "", data); err != nil {
				if err == ErrClientRevoked {
					return ErrClientRevoked
				}
				log.Printf("complete error after success job=%s attempt=%s: %v", job.JobID, job.Lease.AttemptID, err)
				return err
			}
			return nil

		case <-progressTicker.C:
			reportProgress()

		case <-ticker.C:
			// Build progress snapshot (optional).
			reportProgress()
			var p *api.ProgressInfo
			if prog != nil {
				cur := prog.Downloaded.Load()
				exp := prog.Expected

				pct := lastPct
				if exp > 0 {
					raw := int((float64(cur) / float64(exp)) * 100.0)
					if raw < 0 {
						raw = 0
					}
					if raw > 100 {
						raw = 100
					}
					if raw > pct {
						pct = raw
					}
				} else if cur > 0 && pct < 95 {
					pct++
				}
				lastPct = pct

				p = &api.ProgressInfo{Phase: "downloading", Pct: pct}
				bytesDone := cur
				p.BytesDone = &bytesDone
				if exp > 0 {
					bytesTotal := exp
					p.BytesTotal = &bytesTotal
				}
				if emaSpeed > 0 {
					sb := int64(emaSpeed)
					if sb > 0 {
						p.SpeedBps = &sb
					}
				}
			} else {
				// Download not yet initialized (or no progress available): still extend the lease.
				p = &api.ProgressInfo{Phase: "starting", Pct: 0}
			}

			// Best-effort heartbeat with retry/backoff for transient transport errors.
			var (
				code   int
				apiErr *api.APIErrorResponse
				hbErr  error
			)
			for i, d := range retryDelays {
				if i > 0 {
					select {
					case <-time.After(d):
					case <-ctx.Done():
						cancel()
						return ctx.Err()
					}
				}
				code, _, apiErr, hbErr = r.API.Heartbeat(ctx, job.JobID, api.HeartbeatRequest{
					ClientID:      r.ClientID,
					AttemptID:     job.Lease.AttemptID,
					ExtendSeconds: cfg.HeartbeatExtendSec,
					Progress:      p,
					DedupeKey:     dedupeKey,
				})
				if hbErr == nil {
					break
				}
				if api.IsRevoked(hbErr) {
					cancel()
					return ErrClientRevoked
				}
				log.Printf("heartbeat transport error job=%s (try %d/%d): %v", job.JobID, i+1, len(retryDelays), hbErr)
			}
			if hbErr != nil {
				// Give up until next ticker tick.
				continue
			}

			// Cancel case (new): 409 { error:"job_canceled", canceled_at: ... }
			if code == 403 && apiErr != nil && apiErr.Error == "CLIENT_REVOKED_OR_MISSING" {
				cancel()
				return ErrClientRevoked
			}

			if code == 409 && apiErr != nil && (apiErr.Error == "job_canceled" || apiErr.CanceledAt != nil) {
				// Stop download immediately
				serverStopStatus = "abgebrochen"
				serverStopErr = context.Canceled
				cancel()

				// MVP recommendation: delete .part / meta (avoid accumulating trash for canceled jobs)
				_ = os.Remove(finalPath + ".part")
				_ = os.Remove(finalPath + ".part.meta.json")

				// Finalize cancel (server expects this)
				log.Printf("download canceled: %s", displayPath)
				obs.DownloadFinished(displayPath, serverStopStatus)
				obs.JobCleared()
				if err := r.completeWithRetry(ctx, job, "CANCELED", "job_canceled", map[string]any{
					"dedupe_key": dedupeKey,
					"reason":     "CANCELED_BY_SERVER",
				}); err == ErrClientRevoked {
					return ErrClientRevoked
				}

				return serverStopErr
			}

			// Other server-side "stop working" signals
			if code == 404 && apiErr != nil && apiErr.Error == "attempt_not_found" {
				serverStopStatus = "abgebrochen"
				serverStopErr = errors.New("attempt_not_found")
				cancel()
				log.Printf("heartbeat 404 attempt_not_found, giving up job=%s attempt=%s", job.JobID, job.Lease.AttemptID)
				obs.DownloadFinished(displayPath, serverStopStatus)
				obs.JobCleared()
				return serverStopErr
			}
			if code == 409 && apiErr != nil && apiErr.Error == "attempt_not_open" {
				serverStopStatus = "abgebrochen"
				serverStopErr = errors.New("attempt_not_open")
				cancel()
				log.Printf("heartbeat 409 attempt_not_open, giving up job=%s attempt=%s", job.JobID, job.Lease.AttemptID)
				obs.DownloadFinished(displayPath, serverStopStatus)
				obs.JobCleared()
				return serverStopErr
			}

			// Any other unexpected non-200
			if code != 200 {
				log.Printf("heartbeat non-200 job=%s: code=%d api_error=%q canceled_at=%v", job.JobID, code, apiErr.Error, apiErr.CanceledAt)
			}
		}
	}
}

func (r *Runner) complete(ctx context.Context, job *api.LeasedJob, status, errStr string, data map[string]any) error {
	code, err := r.API.Complete(ctx, job.JobID, api.CompleteRequest{
		ClientID:  r.ClientID,
		AttemptID: job.Lease.AttemptID,
		Result: api.CompleteResult{
			Status: status,
			Error:  errStr,
			Data:   data,
		},
	})
	if api.IsRevoked(err) {
		return ErrClientRevoked
	}
	if code == 409 {
		log.Printf("complete 409 (late complete), job lost: %s", job.JobID)
		return nil
	}
	return err
}
