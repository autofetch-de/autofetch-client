package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
	"github.com/autofetch-de/autofetch-client/internal/archive"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/download"
	internalirc "github.com/autofetch-de/autofetch-client/internal/irc"
	"github.com/autofetch-de/autofetch-client/internal/observe"
	clientruntime "github.com/autofetch-de/autofetch-client/internal/runtime"
)

// ErrClientRevoked is returned when the server responds with
// HTTP 403 {"error":"CLIENT_REVOKED_OR_MISSING"}.
var ErrClientRevoked = errors.New("client revoked or deleted by server")

// ErrClientAuthSuspended is deliberately distinct from a confirmed revocation.
// The application pauses until the user explicitly pairs again.
var ErrClientAuthSuspended = errors.New("authentication suspended after repeated invalid credentials; re-pair required")
var errLeaseLost = errors.New("completion_lease_lost")

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

	DownloadDir           string
	JournalDir            string
	ArchiveQueueDir       string
	ExtractArchives       bool
	ExtractNestedDepth    int
	ExtractLayout         string
	ExtractDeleteOriginal bool
	archiveQueue          *archive.Queue

	// Optional IRC identity (used for XDCC)
	IRCNick       string
	IRCConfig     *config.Config
	PersistConfig func() error

	// AuthRetryDelay overrides the default 30s delay for deterministic tests.
	AuthRetryDelay    time.Duration
	HeartbeatInterval time.Duration
	HeartbeatExtend   int
	DedupeTTLSeconds  int

	RuntimeConfig *clientruntime.ConfigManager
	Observer      observe.Observer

	ircMu sync.Mutex
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

// completeWithRetry retries only result reporting, never IRC/HTTP transfers.
// A 409 means the lease was lost, NOT an acknowledged completion.
func (r *Runner) completeWithRetry(ctx context.Context, job *api.LeasedJob, status, errStr string, data map[string]any) error {
	delays := []time.Duration{0, 2 * time.Second, 5 * time.Second}
	var lastErr error
	journal := DownloadJournal{Dir: r.JournalDir}
	if err := journal.SetReportState(r.ClientID, job.JobID, job.Lease.AttemptID, status, errStr, "pending"); err != nil {
		log.Printf("journal report pending: %v", err)
	}

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
			if jerr := journal.SetReportState(r.ClientID, job.JobID, job.Lease.AttemptID, status, errStr, "acknowledged"); jerr != nil {
				log.Printf("journal report ack: %v", jerr)
			} else if status == "SUCCESS" {
				if err := r.pruneReceiptIfSettled(job.JobID, job.Payload.ArchiveProcessing); err != nil {
					log.Printf("journal prune deferred: %v", err)
				}
			}
			return nil
		}
		if errors.Is(err, errLeaseLost) {
			if jerr := journal.SetReportState(r.ClientID, job.JobID, job.Lease.AttemptID, status, errStr, "lease_lost"); jerr != nil {
				log.Printf("journal lease lost: %v", jerr)
			}
			return err
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
	if r.ArchiveQueueDir != "" {
		archiveCtx, stopArchive := context.WithCancel(ctx)
		archiveDone := make(chan struct{})
		defer func() {
			stopArchive()
			// Do not start another runner while the previous extraction still
			// owns files. Reads/writes check cancellation regularly.
			select {
			case <-archiveDone:
			case <-time.After(5 * time.Second):
				log.Printf("archive worker shutdown timeout")
			}
		}()
		r.archiveQueue = &archive.Queue{Dir: r.ArchiveQueueDir, OnlyPlanned: true, BeforeRemove: func(clientID, jobID, source, digest, destination string) error {
			return (DownloadJournal{Dir: r.JournalDir}).MarkPostProcessed(clientID, jobID, source, digest, destination)
		}}
		go func() { defer close(archiveDone); r.runArchiveQueue(archiveCtx) }()
	}
	bo := &Backoff{Min: 500 * time.Millisecond, Max: 30 * time.Second}
	lastConfigRefresh := time.Time{}
	consecutiveAuthDenials := 0
	authRetryDelay := r.AuthRetryDelay
	if authRetryDelay <= 0 {
		authRetryDelay = 30 * time.Second
	}
	// Each denial is checked in isolation; do not perform a second server
	// request (config + lease) during the same failed authentication cycle.
	authDenied := func(err error) (bool, bool) {
		if !api.IsAuthDenied(err) {
			consecutiveAuthDenials = 0
			return false, false
		}
		consecutiveAuthDenials++
		return true, consecutiveAuthDenials >= 3
	}
	waitAfterAuthDenial := func() error {
		select {
		case <-time.After(authRetryDelay):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	obs := r.Observer
	if obs == nil {
		obs = observe.NopObserver{}
	}

	if err := CleanupPartialsTTL(r.DownloadDir, DefaultPartialTTL); err != nil {
		log.Printf("cleanup error: %v", err)
	}
	if n, err := (DownloadJournal{Dir: r.JournalDir}).PruneOldUnfinished(ctx, r.ClientID, time.Now().Add(-7*24*time.Hour)); err != nil {
		log.Printf("download journal cleanup deferred: %v", err)
	} else if n > 0 {
		log.Printf("download journal cleanup: %d stale entries", n)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if r.RuntimeConfig != nil && (lastConfigRefresh.IsZero() || time.Since(lastConfigRefresh) >= 30*time.Second) {
			if err := r.RuntimeConfig.Refresh(ctx); err != nil {
				if api.IsRevoked(err) {
					return ErrClientRevoked
				}
				if denied, suspend := authDenied(err); denied {
					obs.Poll(false, time.Now(), err)
					if suspend {
						return ErrClientAuthSuspended
					}
					log.Printf("server rejected client authentication (%d/3); retry in %s", consecutiveAuthDenials, authRetryDelay)
					if err := waitAfterAuthDenial(); err != nil {
						return err
					}
					continue
				}
				log.Printf("runtime config refresh failed: %v", err)
			} else {
				// A successful config read is not proof that a denied job
				// lease has recovered. Only successful leasing resets it.
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
			if denied, suspend := authDenied(err); denied {
				if suspend {
					return ErrClientAuthSuspended
				}
				log.Printf("server rejected client authentication (%d/3); retry in %s", consecutiveAuthDenials, authRetryDelay)
				if err := waitAfterAuthDenial(); err != nil {
					return err
				}
				continue
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
		consecutiveAuthDenials = 0
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

		// IRC command jobs require v2; explicit TLS policies additionally require v3.
		method := job.Payload.Download.Instruction.DownloadMethod
		if method == "irc_command" && job.Payload.SchemaVersion != 2 && job.Payload.SchemaVersion != 3 {
			if err := r.completeWithRetry(ctx, job, "FAILED", "irc_command_requires_schema_v2_or_v3", nil); err == ErrClientRevoked {
				return ErrClientRevoked
			}
			continue
		}
		policy := job.Payload.Download.Instruction.IRC
		if policy != nil && (policy.TLSVerification == "pinned" || policy.TLSVerification == "unverified") && job.Payload.SchemaVersion != 3 {
			if err := r.completeWithRetry(ctx, job, "FAILED", "irc_tls_policy_requires_schema_v3", nil); err == ErrClientRevoked {
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

		// The server owns output naming, but paths must not escape the user's
		// chosen download folder, including through symlinks.
		finalPath, pathErr := safeDownloadPath(r.DownloadDir, parsed.Output.Dir, parsed.Output.FilenameVideo)
		if pathErr != nil {
			if err := r.completeWithRetry(ctx, job, "FAILED", pathErr.Error(), nil); err == ErrClientRevoked {
				return ErrClientRevoked
			}
			continue
		}
		displayPath := download.RelativeOutputPath(parsed.Output)
		fingerprint, err := instructionFingerprint(job.Payload.Download.Instruction)
		if err != nil {
			if err := r.completeWithRetry(ctx, job, "FAILED", "invalid_instruction_fingerprint", nil); err == ErrClientRevoked {
				return ErrClientRevoked
			}
			continue
		}
		journal := DownloadJournal{Dir: r.JournalDir}
		// Reuse the exact path of an interrupted attempt. A NEW job must be
		// free to download again even if the old file still exists.
		if prior, found, err := journal.Load(r.ClientID, job.JobID); err != nil {
			log.Printf("journal load failed: %v", err)
			if e := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", "download_journal_read_failed", nil); e == ErrClientRevoked {
				return e
			}
			continue
		} else if found {
			finalPath = prior.TargetPath
		} else {
			path, err := chooseNewJobOutput(finalPath, job.JobID)
			if err != nil {
				if e := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", "download_output_selection_failed", nil); e == ErrClientRevoked {
					return e
				}
				continue
			}
			finalPath = path
		}
		// Record the server-issued attempt before any transfer/dedupe work.
		// Never retry an IRC transfer without a new lease from the server.
		// Only an explicit server-issued automatic fallback may rebind an
		// unfinished download receipt; completed receipts remain immutable.
		sourceSelection := job.Payload.IRCSourceSelection
		allowSourceFallback := sourceSelection != nil && sourceSelection.Mode == "auto" &&
			len(sourceSelection.TriedSourceIDs) >= 2 &&
			sourceSelection.TriedSourceIDs[len(sourceSelection.TriedSourceIDs)-1] == job.Payload.Download.Instruction.CandidateID
		if err := journal.BeginAttempt(r.ClientID, job.JobID, fingerprint, finalPath, job.Lease.AttemptID, allowSourceFallback); err != nil {
			log.Printf("download history start failed job=%s: %v", job.JobID, err)
			if errors.Is(err, os.ErrPermission) {
				obs.Error(err)
			}
			if e := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", "download_journal_start_failed", nil); e == ErrClientRevoked {
				return ErrClientRevoked
			}
			continue
		}

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

		// Never infer success merely from the filename. A matching durable
		// receipt proves this specific job completed locally on a prior attempt.
		receipt, seen, err := journal.Load(r.ClientID, job.JobID)
		if err != nil {
			if err := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", "download_journal_read_failed", nil); err == ErrClientRevoked {
				return ErrClientRevoked
			}
			log.Printf("download journal read failed job=%s: %v", job.JobID, err)
			continue
		}
		if seen {
			verified, verifyErr := journal.VerifyCompleted(ctx, receipt, fingerprint, finalPath)
			if verifyErr != nil {
				log.Printf("download journal mismatch job=%s: %v", job.JobID, verifyErr)
				if err := r.completeWithRetry(ctx, job, "FAILED", verifyErr.Error(), nil); err == ErrClientRevoked {
					return ErrClientRevoked
				}
				continue
			}
			if verified {
				r.enqueueArchive(job, finalPath, receipt.SHA256)
				log.Printf("download recovered from journal: %s", displayPath)
				if err := r.completeWithRetry(ctx, job, "SUCCESS", "", map[string]any{
					"reason": "LOCAL_RECEIPT", "file_path": finalPath, "bytes": receipt.Bytes, "dedupe_key": dedupeKey,
				}); err == ErrClientRevoked {
					return ErrClientRevoked
				}
				continue
			}
		}
		if _, err := os.Lstat(finalPath); err == nil {
			// A file with no durable completion receipt may be unrelated or
			// left over from a crash. Preserve it and let the user decide.
			log.Printf("download target already exists without verified receipt: %s", displayPath)
			if err := r.completeWithRetry(ctx, job, "FAILED", "existing_file_unverified", nil); err == ErrClientRevoked {
				return ErrClientRevoked
			}
			continue
		} else if !os.IsNotExist(err) {
			if err := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", "download_target_stat_failed", nil); err == ErrClientRevoked {
				return ErrClientRevoked
			}
			continue
		}
		// A completed transfer is checkpointed before the server is told SUCCESS.
		err = r.downloadWithHeartbeat(ctx, job, parsed, finalPath, displayPath, dedupeKey, journal, fingerprint)
		if err == ErrClientRevoked {
			return ErrClientRevoked
		}

	}
}

func (r *Runner) downloadWithHeartbeat(ctx context.Context, job *api.LeasedJob, inst download.ParsedInstruction, finalPath, displayPath, dedupeKey string, journal DownloadJournal, fingerprint string) error {
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

	var verifying atomic.Bool
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
			var (
				ircNick           = r.IRCNick
				ircUser           = ""
				ircReal           = ""
				nsEnabled         bool
				nsPass            string
				nsCommand         string
				saslEnabled       bool
				saslUser          string
				saslPass          string
				autoRegister      bool
				registrationEmail string
				reverseDCCEnabled bool
				dccPortMin        int
				dccPortMax        int
				networkIdx        = -1
				networkNick       = ""
			)
			if r.IRCConfig != nil {
				internalirc.EnsureDefaultNick(r.IRCConfig)
				r.ircMu.Lock()
				_, netCfg, changed := internalirc.EnsureNetwork(r.IRCConfig, inst.IRC.Host, inst.IRC.Port, inst.IRC.TLS, inst.IRC.Channel)
				if netCfg != nil {
					// Record server-supplied prerequisite channels for informational
					// display. Downloads still join only the channels in their job.
					if internalirc.RememberJobChannels(netCfg, inst.IRC.PrerequisiteChannels) {
						changed = true
					}
					autoRegister = false // Only explicit WebUI actions may register accounts.
					registrationEmail = r.IRCConfig.IRC.RegistrationEmail
					reverseDCCEnabled = r.IRCConfig.IRC.ReverseDCCEnabled
					dccPortMin = r.IRCConfig.IRC.ReverseDCCPortMin
					dccPortMax = r.IRCConfig.IRC.ReverseDCCPortMax
					networkIdx = 0
					for i := range r.IRCConfig.IRC.Networks {
						if &r.IRCConfig.IRC.Networks[i] == netCfg {
							networkIdx = i
							break
						}
					}
					networkNick = strings.TrimSpace(netCfg.Nick)
					ircNick = internalirc.EffectiveNick(r.IRCConfig, netCfg)
					ircUser = netCfg.Username
					ircReal = netCfg.Realname
					if netCfg.NickServ.RegistrationPending || netCfg.NickServ.RequiresRegistration {
						dlErr = errors.New("irc_registration_pending: complete NickServ registration in local WebUI")
						r.ircMu.Unlock()
						return
					}
					nsEnabled = netCfg.NickServ.Enabled
					nsPass = netCfg.NickServ.Password
					nsCommand = netCfg.NickServ.Command
					saslEnabled = netCfg.SASL.Enabled
					saslUser = netCfg.SASL.Username
					saslPass = netCfg.SASL.Password
				} else if strings.TrimSpace(ircNick) == "" {
					ircNick = internalirc.EffectiveNick(r.IRCConfig, nil)
				}
				if changed && r.PersistConfig != nil {
					if err := r.PersistConfig(); err != nil {
						log.Printf("persist irc network config failed: %v", err)
					}
				}
				r.ircMu.Unlock()
			}

			// An unauthenticated TLS transport must never carry IRC passwords.
			if inst.IRC.TLSVerification == "unverified" {
				if nsEnabled || saslEnabled || autoRegister || nsPass != "" || saslPass != "" {
					dlErr = errors.New("irc_tls_unverified_credentials_not_allowed")
					return
				}
			}
			prog.Expected = 0
			res, prog, dlErr = download.DownloadXDCCToFile(jobCtx, download.XDCCOptions{
				Nick:              ircNick,
				Host:              inst.IRC.Host,
				Port:              inst.IRC.Port,
				TLS:               inst.IRC.TLS,
				TLSVerification:   inst.IRC.TLSVerification,
				TLSSPKIPinSHA256:  inst.IRC.TLSSPKIPinSHA256,
				Network:           inst.IRC.Network,
				JoinChannels:      inst.IRC.PrerequisiteChannels,
				Channel:           inst.IRC.Channel,
				Bot:               inst.IRC.Bot,
				Package:           inst.IRC.Package,
				DownloadMethod:    inst.IRC.DownloadMethod,
				SourceID:          inst.IRC.SourceID,
				CommandTargetKind: inst.IRC.CommandTargetKind,
				CommandTarget:     inst.IRC.CommandTarget,
				DownloadCommand:   inst.IRC.DownloadCommand,
				ExpectedFilename:  inst.IRC.Filename,
				Username:          ircUser,
				Realname:          ircReal,
				NickServEnabled:   nsEnabled,
				NickServPassword:  nsPass,
				NickServCommand:   nsCommand,
				SASLEnabled:       saslEnabled,
				SASLUsername:      saslUser,
				SASLPassword:      saslPass,
				AutoRegister:      autoRegister,
				RegistrationEmail: registrationEmail,
				ReverseDCCEnabled: reverseDCCEnabled,
				DCCPortMin:        dccPortMin,
				DCCPortMax:        dccPortMax,
				OnNickSelected: func(selected string) {
					if r.IRCConfig == nil || networkIdx < 0 {
						return
					}
					selected = strings.TrimSpace(selected)
					if selected == "" {
						return
					}
					r.ircMu.Lock()
					defer r.ircMu.Unlock()
					if networkIdx >= len(r.IRCConfig.IRC.Networks) {
						return
					}
					if strings.EqualFold(networkNick, selected) || strings.EqualFold(r.IRCConfig.IRC.Networks[networkIdx].Nick, selected) {
						return
					}
					r.IRCConfig.IRC.Networks[networkIdx].Nick = selected
					networkNick = selected
					if r.PersistConfig != nil {
						if err := r.PersistConfig(); err != nil {
							log.Printf("persist selected irc nick failed: %v", err)
						}
					}
				},
				OnRegistered: func(selectedNick, password string) {
					if r.IRCConfig == nil || networkIdx < 0 {
						return
					}
					selectedNick = strings.TrimSpace(selectedNick)
					password = strings.TrimSpace(password)
					if password == "" {
						return
					}
					r.ircMu.Lock()
					defer r.ircMu.Unlock()
					if networkIdx >= len(r.IRCConfig.IRC.Networks) {
						return
					}
					n := &r.IRCConfig.IRC.Networks[networkIdx]
					if selectedNick != "" {
						n.Nick = selectedNick
					}
					n.NickServ.Enabled = true
					if strings.TrimSpace(n.NickServ.Command) == "" {
						n.NickServ.Command = "IDENTIFY"
					}
					n.NickServ.Password = password
					r.IRCConfig.IRC.AutoRegister = autoRegister
					r.IRCConfig.IRC.RegistrationEmail = registrationEmail
					if r.PersistConfig != nil {
						if err := r.PersistConfig(); err != nil {
							log.Printf("persist irc registration credentials failed: %v", err)
						}
					}
				},
			}, finalPath, r.bandwidthLimitBytesPerSec(), prog)
			// Keep the actual rejected prerequisite channel from the IRC error,
			// and add the network for the local WebUI configuration link.
			if dlErr != nil {
				lower := strings.ToLower(dlErr.Error())
				if errors.Is(dlErr, download.ErrIRCRegisteredNickRequired) || strings.Contains(lower, "nickserv identify failed") || strings.Contains(lower, "nickserv register failed") || strings.Contains(lower, "sasl_auth_failed") {
					dlErr = fmt.Errorf("%w; network=%s", dlErr, inst.IRC.Host)
				}
			}
		default:
			dlErr = errors.New("unsupported_mode: " + inst.Mode)
		}
		if dlErr == nil && res != nil {
			// File bytes have landed; keep heartbeat active while verifying
			// and writing the durable completion receipt.
			verifying.Store(true)
			size, digest, err := hashLocalFile(jobCtx, finalPath)
			if err == nil && (inst.Mode == "single" || inst.Mode == "") && inst.ExpectedSize > 0 && size != inst.ExpectedSize {
				err = fmt.Errorf("download_size_mismatch: got=%d expected=%d", size, inst.ExpectedSize)
			}
			if err == nil && size != res.Bytes {
				err = fmt.Errorf("download_size_mismatch: received=%d actual=%d", res.Bytes, size)
			}
			if err == nil {
				err = journal.MarkTransferred(r.ClientID, job.JobID, fingerprint, finalPath, size, digest)
				if err != nil {
					err = fmt.Errorf("download_journal_write_failed: %w", err)
				} else {
					r.enqueueArchive(job, finalPath, digest)
				}
			}
			if err != nil {
				dlErr = err
			}
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
					statusText := observe.StatusDownloadCanceled
					errorText := "client_shutdown"
					if mode == "pause" {
						statusText = observe.StatusDownloadPaused
						errorText = "client_paused"
					}
					obs.DownloadFinished(displayPath, statusText)
					obs.JobCleared()
					// The transfer has stopped and its .part file is closed. Report
					// with an independent, bounded context: ctx was already
					// canceled by Ctrl+C / Pause and cannot carry HTTP requests.
					reportCtx, stopReport := context.WithTimeout(context.Background(), 6*time.Second)
					reportErr := r.completeWithRetry(reportCtx, job, "RETRYABLE_ERROR", errorText, map[string]any{
						"dedupe_key": dedupeKey,
						"reason":     strings.ToUpper(errorText),
					})
					stopReport()
					if reportErr != nil {
						log.Printf("graceful job release unconfirmed job=%s: %v", job.JobID, reportErr)
						if reportErr == ErrClientRevoked {
							return ErrClientRevoked
						}
					} else {
						log.Printf("graceful job release acknowledged job=%s reason=%s", job.JobID, errorText)
					}
					return dlErr
				}

				obs.Error(dlErr)

				if errors.Is(dlErr, download.ErrDCCOfferTimeout) {
					if err := r.completeWithRetry(ctx, job, "FAILED", "irc_dcc_offer_timeout", map[string]any{
						"reason": "IRC_DCC_OFFER_TIMEOUT", "network": inst.IRC.Host,
						"channel": inst.IRC.Channel, "bot": inst.IRC.Bot,
						"dedupe_key": dedupeKey,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

				if errors.Is(dlErr, download.ErrDCCPrivateAddress) || errors.Is(dlErr, download.ErrDCCConnectFailed) {
					reason := "DCC_CONNECT_FAILED"
					if errors.Is(dlErr, download.ErrDCCPrivateAddress) {
						reason = "DCC_PRIVATE_ADDRESS_REJECTED"
					}
					if err := r.completeWithRetry(ctx, job, "FAILED", strings.ToLower(reason), map[string]any{
						"reason": reason, "network": inst.IRC.Host, "channel": inst.IRC.Channel,
						"bot": inst.IRC.Bot, "dedupe_key": dedupeKey,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

				var gline *download.IRCGLineError
				if errors.As(dlErr, &gline) {
					data := map[string]any{
						"reason":     "IRC_GLINED",
						"message":    gline.Message,
						"network":    inst.IRC.Host,
						"channel":    inst.IRC.Channel,
						"bot":        inst.IRC.Bot,
						"retryable":  false,
						"dedupe_key": dedupeKey,
					}
					if gline.RetryAfter > 0 {
						data["retry_after_seconds"] = int64(gline.RetryAfter / time.Second)
						data["retry_after_hint"] = gline.RetryAfter.String()
					}
					if err := r.completeWithRetry(ctx, job, "FAILED", "irc_glined", data); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

				if errors.Is(dlErr, download.ErrReverseDCCTimeout) {
					message := dlErr.Error()
					if idx := strings.Index(message, ": "); idx >= 0 && idx+2 < len(message) {
						message = message[idx+2:]
					}
					if err := r.completeWithRetry(ctx, job, "FAILED", "reverse_dcc_port_forward_required", map[string]any{
						"reason":     "REVERSE_DCC_PORT_FORWARD_REQUIRED",
						"message":    message,
						"network":    inst.IRC.Host,
						"channel":    inst.IRC.Channel,
						"bot":        inst.IRC.Bot,
						"retryable":  false,
						"dedupe_key": dedupeKey,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

				if errors.Is(dlErr, download.ErrReverseDCCDisabled) {
					message := dlErr.Error()
					if idx := strings.Index(message, ": "); idx >= 0 && idx+2 < len(message) {
						message = message[idx+2:]
					}
					if err := r.completeWithRetry(ctx, job, "FAILED", "reverse_dcc_disabled", map[string]any{
						"reason":     "REVERSE_DCC_DISABLED",
						"message":    message,
						"network":    inst.IRC.Host,
						"channel":    inst.IRC.Channel,
						"bot":        inst.IRC.Bot,
						"retryable":  false,
						"dedupe_key": dedupeKey,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

				if errors.Is(dlErr, download.ErrIRCRegisteredNickRequired) {
					if err := r.completeWithRetry(ctx, job, "FAILED", "irc_registered_nick_required", map[string]any{
						"reason":     "IRC_AUTH_REQUIRED",
						"message":    dlErr.Error(),
						"network":    inst.IRC.Host,
						"channel":    inst.IRC.Channel,
						"dedupe_key": dedupeKey,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

				var incomplete *download.XDCCTransferIncompleteError
				if errors.As(dlErr, &incomplete) {
					if err := r.completeWithRetry(ctx, job, "RETRYABLE_ERROR", "xdcc_transfer_incomplete", map[string]any{
						"reason":         "XDCC_TRANSFER_INCOMPLETE",
						"message":        dlErr.Error(),
						"received_bytes": incomplete.Received,
						"expected_bytes": incomplete.Expected,
						"network":        inst.IRC.Host,
						"channel":        inst.IRC.Channel,
						"bot":            inst.IRC.Bot,
						"dedupe_key":     dedupeKey,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

				if strings.Contains(dlErr.Error(), "xdcc_offer_timeout") {
					if err := r.completeWithRetry(ctx, job, "NOT_FOUND_YET", "xdcc_offer_timeout", map[string]any{
						"reason":     "XDCC_OFFER_TIMEOUT",
						"message":    dlErr.Error(),
						"network":    inst.IRC.Host,
						"channel":    inst.IRC.Channel,
						"bot":        inst.IRC.Bot,
						"dedupe_key": dedupeKey,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

				if strings.Contains(dlErr.Error(), "sasl_auth_failed") || strings.Contains(strings.ToLower(dlErr.Error()), "nickserv identify failed") || strings.Contains(strings.ToLower(dlErr.Error()), "nickserv register failed") {
					reason := "IRC_AUTH_FAILED"
					if strings.Contains(dlErr.Error(), "sasl_auth_failed") {
						reason = "SASL_AUTH_FAILED"
					}
					if err := r.completeWithRetry(ctx, job, "FAILED", strings.ToLower(reason), map[string]any{
						"reason":     reason,
						"message":    dlErr.Error(),
						"network":    inst.IRC.Host,
						"channel":    inst.IRC.Channel,
						"dedupe_key": dedupeKey,
					}); err == ErrClientRevoked {
						return ErrClientRevoked
					}
					return dlErr
				}

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
			obs.DownloadFinished(displayPath, observe.StatusDownloadCompleted)
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

				phase := "downloading"
				if verifying.Load() {
					phase = "verifying"
				}
				p = &api.ProgressInfo{Phase: phase, Pct: pct}
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
				serverStopStatus = observe.StatusDownloadCanceled
				serverStopErr = context.Canceled
				cancel()

				// Wait until the downloader has actually released its file/socket handles.
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					log.Printf("download cancel timeout: %s", displayPath)
				}

				// Explicit user cancellation discards resumable partial data.
				_ = os.Remove(finalPath + ".part")
				_ = os.Remove(finalPath + ".part.meta.json")

				// Finalize cancel after local shutdown/cleanup.
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

			// A user pause is not a cancellation. Keep .part and resume
			// metadata untouched and let the server hold the paused job.
			if code == 409 && apiErr != nil && apiErr.Error == "job_paused" {
				serverStopStatus = observe.StatusDownloadPaused
				serverStopErr = errors.New("job_paused")
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					log.Printf("download pause timeout: %s", displayPath)
					// Never report a completed pause while the file handle is open.
					return serverStopErr
				}
				log.Printf("download paused: %s", displayPath)
				obs.DownloadFinished(displayPath, observe.StatusDownloadPaused)
				obs.JobCleared()
				// Context is still alive; only the download's jobCtx was canceled.
				if err := r.completeWithRetry(ctx, job, "PAUSED", "job_paused", map[string]any{
					"dedupe_key": dedupeKey, "reason": "PAUSED_BY_USER",
				}); err != nil {
					log.Printf("pause acknowledgement unconfirmed job=%s: %v", job.JobID, err)
				}
				return serverStopErr
			}

			// Other server-side "stop working" signals
			if code == 404 && apiErr != nil && apiErr.Error == "attempt_not_found" {
				serverStopStatus = observe.StatusDownloadCanceled
				serverStopErr = errors.New("attempt_not_found")
				cancel()
				log.Printf("heartbeat 404 attempt_not_found, giving up job=%s attempt=%s", job.JobID, job.Lease.AttemptID)
				obs.DownloadFinished(displayPath, serverStopStatus)
				obs.JobCleared()
				return serverStopErr
			}
			if code == 409 && apiErr != nil && apiErr.Error == "attempt_not_open" {
				serverStopStatus = observe.StatusDownloadCanceled
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
		log.Printf("complete 409 (late complete), lease lost: %s", job.JobID)
		return errLeaseLost
	}
	return err
}

// enqueueArchive runs independently of download completion: a failed local
// extraction is NEVER reported as a failed server download.
func (r *Runner) enqueueArchive(job *api.LeasedJob, source, digest string) {
	plan := job.Payload.ArchiveProcessing
	if plan == nil || !plan.Enabled || !plan.Validate() || !archive.Supported(source) {
		return
	}
	q := r.archiveQueue
	if q == nil && r.ArchiveQueueDir != "" {
		q = &archive.Queue{Dir: r.ArchiveQueueDir, OnlyPlanned: true}
	}
	if q == nil {
		log.Printf("archive extraction not queued: archive_queue_dir_missing")
		return
	}
	if err := q.EnqueuePlanned(r.ClientID, job.JobID, source, digest, archive.Policy{Layout: plan.Layout, DeleteOriginal: plan.DeleteOriginal}, plan.NestedDepth); err != nil {
		log.Printf("archive enqueue failed job=%s: %v (download remains completed)", job.JobID, err)
	} else {
		log.Printf("archive extraction queued: %s", source)
	}
}

func (r *Runner) runArchiveQueue(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		processed, err := r.archiveQueue.ProcessOnePolicy(ctx, r.ClientID, archive.Limits{}, 0, archive.Policy{Layout: "separate"})
		if err != nil && ctx.Err() == nil {
			log.Printf("archive post-processing failed: %v (see local archive history)", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
		if processed {
			if items, err := r.archiveQueue.History(r.ClientID, 100); err == nil {
				journal := DownloadJournal{Dir: r.JournalDir}
				for _, it := range items {
					if it.Status == "completed" && !it.CleanupPending && it.StagingDir == "" {
						_, _ = journal.ForgetAcknowledgedSuccess(r.ClientID, it.JobID, true)
					}
				}
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) pruneReceiptIfSettled(jobID string, plan *api.ArchiveProcessing) error {
	ready := plan == nil || !plan.Enabled
	if plan != nil && plan.Enabled {
		if receipt, exists, err := (DownloadJournal{Dir: r.JournalDir}).Load(r.ClientID, jobID); err == nil && exists && !archive.Supported(receipt.TargetPath) {
			ready = true
		}
	}
	if plan != nil && plan.Enabled && !ready {
		if r.archiveQueue != nil {
			readyFlag, err := r.archiveQueue.FinishedForJob(r.ClientID, jobID)
			if err != nil {
				return err
			}
			ready = readyFlag
		}
	}
	_, err := (DownloadJournal{Dir: r.JournalDir}).ForgetAcknowledgedSuccess(r.ClientID, jobID, ready)
	return err
}
