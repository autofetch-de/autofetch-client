package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	gort "runtime"
	"strings"
	"sync"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/observe"
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
	if s == nil {
		return
	}
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
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
	mu sync.Mutex

	cfg     *config.Config
	version string

	api     *api.Client
	state   *observe.State
	factory RunnerFactory

	runCtx     context.Context
	cancelRun  context.CancelFunc
	stopSignal *stopSignal
	running    bool
	pairing    bool
}

func NewService(cfg *config.Config, version string, apiClient *api.Client, state *observe.State, factory RunnerFactory) *Service {
	return &Service{cfg: cfg, version: version, api: apiClient, state: state, factory: factory}
}

func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running || s.pairing {
		return nil
	}
	sig := &stopSignal{}
	baseCtx := context.WithValue(context.Background(), stopModeGetterContextKey, func() string { return string(sig.Mode()) })
	ctx, cancel := context.WithCancel(baseCtx)
	s.runCtx = ctx
	s.cancelRun = cancel
	s.stopSignal = sig
	if strings.TrimSpace(s.cfg.ClientID) == "" || strings.TrimSpace(s.cfg.ClientToken) == "" {
		s.pairing = true
		go s.pairAndMaybeRun(ctx)
		return nil
	}
	s.running = true
	s.state.SetPaired(true)
	s.state.SetRunning(true)
	r := s.factory(s.state)
	go s.run(r, ctx)
	return nil
}

func (s *Service) pairAndMaybeRun(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.pairing = false
		s.mu.Unlock()
	}()

	apiClient := api.New(s.cfg.ServerBaseURL, "", "")
	apiClient.HTTP.Timeout = 60 * time.Second
	platform := gort.GOOS
	arch := normalizeArch(gort.GOARCH)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start, err := apiClient.RegisterStart(ctx, api.RegisterStartRequest{
			ClientName: s.cfg.ClientName,
			Platform:   platform,
			Arch:       arch,
			Version:    s.version,
		})
		if err != nil {
			s.state.PairingFailed(err)
			s.state.Error(err)
			return
		}

		s.state.StartPairing(start.PairingCode, strings.TrimRight(s.cfg.ServerBaseURL, "/")+"/clients/new", start.ExpiresAt)
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
				s.state.PairingPending("Warte auf Bestätigung")
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
				s.state.PairingPending("Code abgelaufen, neuer Code wird erstellt")
				goto NEWPAIR
			case "REJECTED":
				s.state.PairingPending("Pairing abgelehnt, neuer Code wird erstellt")
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
}

func (s *Service) Stop() error {
	return s.Pause()
}

func (s *Service) Pause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	go func() { _ = s.Start() }()
	return nil
}

func (s *Service) Snapshot() observe.Snapshot {
	if s == nil || s.state == nil {
		return observe.Snapshot{}
	}
	return s.state.Snapshot()
}

func (s *Service) ConfigPath() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	return s.cfg.ConfigPath
}

func (s *Service) DownloadDir() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.DownloadDir
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
