package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	gort "runtime"
	"strings"
	"sync"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
	"github.com/autofetch-de/autofetch-client/internal/archive"
	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
	internalirc "github.com/autofetch-de/autofetch-client/internal/irc"
	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/observe"
	"github.com/autofetch-de/autofetch-client/internal/update"
	"github.com/autofetch-de/autofetch-client/internal/worker"
)

type RunnerFactory func(obs observe.Observer) *worker.Runner

type stopMode string

const (
	stopModePause    stopMode = "pause"
	stopModeShutdown stopMode = "shutdown"
)

const stopModeGetterContextKey = "autofetch.stop_mode_getter"

type stopSignal struct {
	mu   sync.RWMutex
	mode stopMode
}

func (s *stopSignal) Set(mode stopMode) {
	if s != nil {
		s.mu.Lock()
		s.mode = mode
		s.mu.Unlock()
	}
}
func (s *stopSignal) Mode() stopMode {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

type Service struct {
	mu         sync.Mutex
	cfg        *config.Config
	buildInfo  buildinfo.Info
	api        *api.Client
	state      *observe.State
	localizer  *localization.Localizer
	factory    RunnerFactory
	runCtx     context.Context
	cancelRun  context.CancelFunc
	stopSignal *stopSignal
	// Incremented whenever a queued settings-triggered restart must be superseded.
	settingsRestartGeneration uint64
	runnerDone                chan struct{}
	running                   bool
	updateStatus              update.Status
	pairing                   bool
}

func NewService(cfg *config.Config, info buildinfo.Info, apiClient *api.Client, state *observe.State, factory RunnerFactory) *Service {
	return &Service{cfg: cfg, buildInfo: info, api: apiClient, state: state, localizer: localization.New(info.Language), factory: factory}
}

func (s *Service) BuildInfo() buildinfo.Info { return s.buildInfo }

func (s *Service) UpdateStatus() update.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateStatus
}

// RunUpdateChecker is independent of the download worker. A failed check never
// interrupts leasing, pairing or transfers. It reports only published releases.
func (s *Service) RunUpdateChecker(ctx context.Context) {
	if s == nil || s.cfg == nil {
		return
	}
	check := func() {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		status, err := update.Check(checkCtx, s.cfg.ServerBaseURL, s.buildInfo, nil)
		if err != nil {
			return
		} // Offline/missing release API: keep running normally.
		s.mu.Lock()
		wasAvailable := s.updateStatus.Available
		previousLatest := s.updateStatus.Latest
		s.updateStatus = status
		s.mu.Unlock()
		if status.Available && (!wasAvailable || previousLatest != status.Latest) {
			log.Printf("client update available: installed=%s published=%s download=%s", status.Installed, status.Latest, status.DownloadURL)
		}
	}
	check()
	ticker := time.NewTicker(update.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func (s *Service) Localizer() *localization.Localizer {
	if s == nil || s.localizer == nil {
		return localization.New(localization.English)
	}
	return s.localizer
}

func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked()
}

// startLocked is also used after a previous worker has completely exited.
// Callers must hold s.mu.
func (s *Service) startLocked() error {
	if s.running || s.pairing {
		return nil
	}
	if s.cfg.AuthPausedAt != nil {
		return fmt.Errorf("authentication paused: invalid client credentials; re-pair required")
	}
	// Do not overlap a new runner with a previous one still flushing its
	// final report and closing the DCC file/socket.
	if s.runnerDone != nil {
		select {
		case <-s.runnerDone:
		default:
			return fmt.Errorf("client still stopping; retry start shortly")
		}
	}
	if s.cfg != nil {
		s.cfg.IRC = s.cfg.IRC.WithDefaults()
		if internalirc.EnsureDefaultNick(s.cfg) {
			if err := config.Persist(*s.cfg); err != nil {
				return err
			}
		}
	}
	sig := &stopSignal{}
	baseCtx := context.WithValue(context.Background(), stopModeGetterContextKey, func() string { return string(sig.Mode()) })
	ctx, cancel := context.WithCancel(baseCtx)
	s.runCtx = ctx
	s.cancelRun = cancel
	s.stopSignal = sig
	done := make(chan struct{})
	s.runnerDone = done
	if strings.TrimSpace(s.cfg.ClientID) == "" || strings.TrimSpace(s.cfg.ClientToken) == "" {
		s.pairing = true
		go func() { defer close(done); s.pairAndMaybeRun(ctx) }()
		return nil
	}
	s.running = true
	s.state.SetPaired(true)
	s.state.SetRunning(true)
	r := s.factory(s.state)
	go func() { defer close(done); s.run(r, ctx) }()
	return nil
}

// WaitStopped keeps the process alive briefly after SIGINT/SIGTERM so the
// worker can close its partial file and acknowledge the released lease.
func (s *Service) WaitStopped(ctx context.Context) error {
	s.mu.Lock()
	done := s.runnerDone
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) pairAndMaybeRun(ctx context.Context) {
	defer func() { s.mu.Lock(); s.pairing = false; s.mu.Unlock() }()
	apiClient := api.New(s.cfg.ServerBaseURL, "", "")
	apiClient.HTTP.Timeout = 60 * time.Second
	platform := s.buildInfo.Platform
	if strings.TrimSpace(platform) == "" {
		platform = gort.GOOS
	}
	arch := s.buildInfo.Arch
	if strings.TrimSpace(arch) == "" {
		arch = normalizeArch(gort.GOARCH)
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		start, err := apiClient.RegisterStart(ctx, api.RegisterStartRequest{ClientName: s.cfg.ClientName, Platform: platform, Arch: arch, Version: s.buildInfo.Version, Variant: s.buildInfo.Variant, BuildCommit: s.buildInfo.BuildCommit, BuildDate: s.buildInfo.BuildDate, ClientProtocolInfo: api.CurrentProtocolInfo(s.cfg.InstanceID)})
		if err != nil {
			s.state.PairingFailed(err)
			s.state.Error(err)
			return
		}
		pairingURL := strings.TrimRight(s.cfg.ServerBaseURL, "/") + "/clients/new"
		s.state.StartPairing(start.PairingCode, pairingURL, start.ExpiresAt)
		// A service-backed pairing flow (GUI or local Web UI) does not run the
		// CLI pairing path. Print the same prominent code in the terminal.
		writePairingBanner(os.Stdout, s.localizer, start.PairingCode, pairingURL)
		pollEvery := time.Duration(start.PollAfterSeconds) * time.Second
		if pollEvery <= 0 {
			pollEvery = 3 * time.Second
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollEvery):
			}
			res, err := apiClient.RegisterPoll(ctx, api.RegisterPollRequest{PairingID: start.PairingID})
			if err != nil {
				s.state.PairingFailed(err)
				s.state.Error(err)
				return
			}
			switch res.Status {
			case "PENDING":
				s.state.PairingPending(observe.StatusPairingWaiting)
				if res.PollAfterSeconds > 0 {
					pollEvery = time.Duration(res.PollAfterSeconds) * time.Second
				}
				continue
			case "APPROVED":
				if strings.TrimSpace(res.ClientID) == "" || strings.TrimSpace(res.ClientToken) == "" {
					err := fmt.Errorf("pairing approved but credentials missing")
					s.state.PairingFailed(err)
					return
				}
				now := time.Now().UTC()
				s.mu.Lock()
				s.cfg.ClientID = res.ClientID
				s.cfg.ClientToken = res.ClientToken
				s.cfg.PairedAt = &now
				s.cfg.RevokedAt = nil
				s.cfg.AuthPausedAt = nil
				s.api.ClientID = res.ClientID
				s.api.ClientToken = res.ClientToken
				s.running = true
				s.mu.Unlock()
				if err := config.Persist(*s.cfg); err != nil {
					s.state.PairingFailed(err)
					return
				}
				s.state.PairingApproved(res.ClientID)
				s.state.SetIdentity(s.cfg.ServerBaseURL, res.ClientID)
				s.state.SetRunning(true)
				r := s.factory(s.state)
				s.run(r, ctx)
				return
			case "EXPIRED":
				s.state.PairingPending(observe.StatusPairingExpiredNewCode)
				goto NEWPAIR
			case "REJECTED":
				s.state.PairingPending(observe.StatusPairingRejectedNewCode)
				goto NEWPAIR
			default:
				err := fmt.Errorf("unknown pairing status: %s", res.Status)
				s.state.PairingFailed(err)
				return
			}
		}
	NEWPAIR:
		continue
	}
}

func (s *Service) run(r *worker.Runner, ctx context.Context) {
	err := r.Run(ctx)
	if errors.Is(err, worker.ErrClientAuthSuspended) || errors.Is(err, worker.ErrClientRevoked) {
		s.mu.Lock()
		now := time.Now().UTC()
		s.cfg.AuthPausedAt = &now
		if errors.Is(err, worker.ErrClientRevoked) {
			config.MarkRevoked(s.cfg, now)
		}
		if persistErr := config.Persist(*s.cfg); persistErr != nil {
			log.Printf("authentication pause persistence failed: %v", persistErr)
		}
		s.mu.Unlock()
		log.Printf("client automatically paused: server repeatedly rejected credentials; re-pair required")
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		s.state.Error(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	s.cancelRun = nil
	s.runCtx = nil
	s.stopSignal = nil
	s.state.SetRunning(false)
	s.state.SetConnected(false)
}

func (s *Service) Stop() error { return s.Pause() }
func (s *Service) Pause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settingsRestartGeneration++
	if s.stopSignal != nil {
		s.stopSignal.Set(stopModePause)
	}
	if s.cancelRun != nil {
		s.cancelRun()
	}
	s.running = false
	s.pairing = false
	s.cancelRun = nil
	s.runCtx = nil
	s.stopSignal = nil
	s.state.SetRunning(false)
	s.state.SetConnected(false)
	return nil
}
func (s *Service) Shutdown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settingsRestartGeneration++
	if s.stopSignal != nil {
		s.stopSignal.Set(stopModeShutdown)
	}
	if s.cancelRun != nil {
		s.cancelRun()
	}
	s.running = false
	s.pairing = false
	s.cancelRun = nil
	s.runCtx = nil
	s.stopSignal = nil
	s.state.SetRunning(false)
	s.state.SetConnected(false)
	return nil
}

func (s *Service) StartRePair() error {
	_ = s.Shutdown()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := config.ClearCredentials(s.cfg); err != nil {
		return err
	}
	s.api.ClientID = ""
	s.api.ClientToken = ""
	s.state.SetPaired(false)
	s.state.SetConnected(false)
	go func() {
		waitCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = s.WaitStopped(waitCtx)
		_ = s.Start()
	}()
	return nil
}
func (s *Service) Snapshot() observe.Snapshot {
	if s == nil || s.state == nil {
		return observe.Snapshot{}
	}
	return s.state.Snapshot()
}

// DownloadHistory returns the local, per-pairing record summaries. Download
// retries remain exclusively server-leased; history is read-only.
func (s *Service) DownloadHistory(ctx context.Context, limit int) ([]worker.DownloadHistoryItem, error) {
	if s == nil || s.cfg == nil {
		return nil, fmt.Errorf("service not initialized")
	}
	s.mu.Lock()
	clientID := s.cfg.ClientID
	configPath := s.cfg.ConfigPath
	s.mu.Unlock()
	return (worker.DownloadJournal{Dir: filepath.Join(filepath.Dir(configPath), "download-journal")}).History(ctx, clientID, limit)
}

// HideDownloadHistory hides one/all entries in the local presentation without
// deleting durable download receipts or modifying any server-side job.
func (s *Service) HideDownloadHistory(ctx context.Context, jobID string, all bool) error {
	if s == nil || s.cfg == nil {
		return fmt.Errorf("service not initialized")
	}
	s.mu.Lock()
	clientID, configPath := s.cfg.ClientID, s.cfg.ConfigPath
	s.mu.Unlock()
	return (worker.DownloadJournal{Dir: filepath.Join(filepath.Dir(configPath), "download-journal")}).HideHistory(ctx, clientID, jobID, all)
}

// ArchiveHistory exposes extraction results separately from server downloads.
func (s *Service) ArchiveHistory(ctx context.Context, limit int) ([]archive.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.cfg == nil {
		return nil, fmt.Errorf("service not initialized")
	}
	s.mu.Lock()
	clientID, configPath := s.cfg.ClientID, s.cfg.ConfigPath
	s.mu.Unlock()
	return (&archive.Queue{Dir: filepath.Join(filepath.Dir(configPath), "archive-queue")}).History(clientID, limit)
}

// RetryArchive requeues a local failed extraction; it never re-leases a server job.
func (s *Service) RetryArchive(ctx context.Context, jobID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.cfg == nil {
		return fmt.Errorf("service not initialized")
	}
	s.mu.Lock()
	clientID, configPath := s.cfg.ClientID, s.cfg.ConfigPath
	s.mu.Unlock()
	return (&archive.Queue{Dir: filepath.Join(filepath.Dir(configPath), "archive-queue")}).Retry(clientID, jobID)
}

func (s *Service) ConfigPath() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	return s.cfg.ConfigPath
}

// SetWebUIPassword performs the one-time password bootstrap. Credentials are
// written to a new, private file; only its path is persisted in client.json.
// Existing password files are never overwritten by the unauthenticated setup.
func (s *Service) SetWebUIPassword(password string) error {
	if err := config.ValidateWebUIPassword(password); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg == nil || s.cfg.ConfigPath == "" {
		return fmt.Errorf("client configuration path unavailable")
	}
	if s.cfg.WebUIPasswordFile != "" {
		return fmt.Errorf("WebUI password already configured")
	}
	path := filepath.Join(filepath.Dir(s.cfg.ConfigPath), "webui.password")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create WebUI password directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create WebUI password file: %w", err)
	}
	if _, err = file.WriteString(password); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write WebUI password file: %w", err)
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close WebUI password file: %w", err)
	}
	oldNoPassword := s.cfg.WebUILANNoPassword
	s.cfg.WebUIPasswordFile = path
	s.cfg.WebUILANNoPassword = false
	if err = config.Persist(*s.cfg); err != nil {
		s.cfg.WebUIPasswordFile = ""
		s.cfg.WebUILANNoPassword = oldNoPassword
		_ = os.Remove(path)
		return fmt.Errorf("save WebUI password configuration: %w", err)
	}
	return nil
}

// ChangeWebUIPassword rotates an already configured WebUI password. The new
// credential is atomically published in the same directory and is not stored
// in client.json. The caller must have authenticated with the current one.
func (s *Service) ChangeWebUIPassword(password string) error {
	if err := config.ValidateWebUIPassword(password); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg == nil || s.cfg.WebUIPasswordFile == "" {
		return fmt.Errorf("WebUI password is not configured")
	}
	path := s.cfg.WebUIPasswordFile
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect WebUI password file: %w", err)
	}
	if !info.Mode().IsRegular() || (gort.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return fmt.Errorf("WebUI password file must be regular and private")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".webui-password-*")
	if err != nil {
		return fmt.Errorf("create new WebUI password file: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString(password); err != nil {
		_ = file.Close()
		return fmt.Errorf("write new WebUI password file: %w", err)
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync WebUI password file: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("close WebUI password file: %w", err)
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("replace WebUI password file: %w", err)
	}
	return nil
}

func (s *Service) DownloadDir() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.DownloadDir
}

// Legacy local archive controls are intentionally no longer authoritative.
func (s *Service) UpdateArchiveOptions(_ config.ArchiveOptions) error {
	return fmt.Errorf("archive_settings_managed_by_server")
}

func (s *Service) IRCConfig() config.IRCConfig {
	if s == nil || s.cfg == nil {
		return config.IRCConfig{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Return independent slices. GUI editors and normalization must not mutate
	// the active network configuration before a successful persisted save.
	copied := s.cfg.IRC
	copied.Networks = append([]config.IRCNetwork(nil), copied.Networks...)
	for i := range copied.Networks {
		copied.Networks[i].Channels = append([]string(nil), copied.Networks[i].Channels...)
	}
	return copied
}
func (s *Service) EnsureIRCDefaults() error {
	s.mu.Lock()
	if s.cfg == nil {
		s.mu.Unlock()
		return fmt.Errorf("config missing")
	}
	changed := internalirc.EnsureDefaultNick(s.cfg)
	s.cfg.IRC = s.cfg.IRC.WithDefaults()
	if !changed {
		s.mu.Unlock()
		return nil
	}
	err := config.Persist(*s.cfg)
	s.mu.Unlock()
	return err
}
func (s *Service) UpdateDownloadDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("download path missing")
	}
	dir = filepath.Clean(dir)
	s.mu.Lock()
	if s.cfg == nil {
		s.mu.Unlock()
		return fmt.Errorf("config missing")
	}
	wasActive := s.running || s.pairing
	s.cfg.DownloadDir = dir
	err := config.Persist(*s.cfg)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if !wasActive {
		return nil
	}
	if err := s.Shutdown(); err != nil {
		return err
	}
	return s.Start()
}

// UpdateLocalSettings persists the configuration before restarting a running
// worker. The previous worker may still own a DCC socket or journal receipt
// after cancellation, so start the replacement only after runnerDone closes.
func (s *Service) UpdateLocalSettings(dir string, ircCfg config.IRCConfig, autoRegister bool, registrationEmail string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("download path missing")
	}
	dir = filepath.Clean(dir)
	ircCfg = ircCfg.WithDefaults()
	ircCfg.AutoRegister = autoRegister
	ircCfg.RegistrationEmail = strings.TrimSpace(registrationEmail)
	s.mu.Lock()
	if s.cfg == nil {
		s.mu.Unlock()
		return fmt.Errorf("config missing")
	}
	// Persist a copy first: a failed disk write must not silently change the
	// running identity or make the WebUI report a reset that wasn't saved.
	next := *s.cfg
	next.DownloadDir = dir
	next.IRC = ircCfg
	next.IRC.Networks = append([]config.IRCNetwork(nil), ircCfg.Networks...)
	for i := range next.IRC.Networks {
		next.IRC.Networks[i].Channels = append([]string(nil), next.IRC.Networks[i].Channels...)
	}
	if internalirc.EnsureDefaultNick(&next) {
		next.IRC = next.IRC.WithDefaults()
	}
	if err := config.Persist(next); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("persist local settings: %w", err)
	}
	wasActive := s.running || s.pairing
	*s.cfg = next
	if !wasActive {
		s.mu.Unlock()
		return nil
	}
	if s.stopSignal != nil {
		s.stopSignal.Set(stopModeShutdown)
	}
	if s.cancelRun != nil {
		s.cancelRun()
	}
	s.running = false
	s.pairing = false
	s.cancelRun = nil
	s.runCtx = nil
	s.stopSignal = nil
	s.state.SetRunning(false)
	s.state.SetConnected(false)
	s.settingsRestartGeneration++
	generation := s.settingsRestartGeneration
	previousDone := s.runnerDone
	s.mu.Unlock()

	go func() {
		if previousDone != nil {
			<-previousDone
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		// An explicit Stop/Pause/Shutdown supersedes this automatic restart.
		if s.settingsRestartGeneration != generation {
			return
		}
		if err := s.startLocked(); err != nil {
			log.Printf("restart after IRC settings change failed: %v", err)
		}
	}()
	return nil
}
func (s *Service) TestConnection(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if s.api == nil {
		err := fmt.Errorf("api client missing")
		s.state.Error(err)
		s.state.SetConnected(false)
		return err
	}
	if strings.TrimSpace(s.api.ClientID) == "" || strings.TrimSpace(s.api.ClientToken) == "" {
		err := fmt.Errorf("client not paired yet")
		s.state.Error(err)
		s.state.SetConnected(false)
		return err
	}
	_, _, err := s.api.GetRuntimeConfig(ctx)
	if err != nil {
		s.state.Poll(false, time.Now(), err)
		s.state.Error(err)
		return err
	}
	s.state.Poll(true, time.Now(), nil)
	return nil
}
