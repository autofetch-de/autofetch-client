package app

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
	internalirc "github.com/autofetch-de/autofetch-client/internal/irc"
	"github.com/autofetch-de/autofetch-client/internal/observe"
	clientruntime "github.com/autofetch-de/autofetch-client/internal/runtime"
	"github.com/autofetch-de/autofetch-client/internal/worker"
)

func Bootstrap(cfg *config.Config, info buildinfo.Info) (*Service, *observe.State, error) {
	if cfg.ServerBaseURL == "" {
		return nil, nil, fmt.Errorf("SERVER_BASE_URL missing")
	}
	if cfg.DownloadDir == "" {
		return nil, nil, fmt.Errorf("DOWNLOAD_DIR missing")
	}
	// Older builds persisted revocation and then exited. Preserve the
	// prohibition on reconnecting, but keep the UI/headless process alive in
	// a non-polling paused state even under systemd Restart=always.
	if cfg.RevokedAt != nil && !cfg.RePair && cfg.AuthPausedAt == nil {
		cfg.AuthPausedAt = cfg.RevokedAt
		if err := config.Persist(*cfg); err != nil {
			return nil, nil, fmt.Errorf("persist authentication pause: %w", err)
		}
	}
	if err := config.EnsureInstanceID(cfg); err != nil {
		return nil, nil, err
	}
	if cfg.RePair {
		if err := config.ClearCredentials(cfg); err != nil {
			return nil, nil, err
		}
	}

	apiClient := api.New(cfg.ServerBaseURL, cfg.ClientID, cfg.ClientToken)
	apiClient.Metadata = api.ClientMetadata{Version: info.Version, Platform: info.Platform, Arch: info.Arch, Variant: info.Variant, BuildCommit: info.BuildCommit, BuildDate: info.BuildDate}
	apiClient.Protocol = api.CurrentProtocolInfo(cfg.InstanceID)
	apiClient.HTTP.Timeout = 60 * time.Second
	dlHTTP := &http.Client{Timeout: 0}
	clientsCfg := config.LoadClientsConfig()
	cfg.IRC = cfg.IRC.WithDefaults()
	if strings.TrimSpace(cfg.IRC.DefaultNick) == "" {
		legacyNick := strings.TrimSpace(clientsCfg.IRCNick)
		if legacyNick != "" {
			cfg.IRC.DefaultNick = legacyNick
		}
	}
	if internalirc.EnsureDefaultNick(cfg) {
		if err := config.Persist(*cfg); err != nil {
			return nil, nil, err
		}
	}

	state := observe.NewState(cfg.ServerBaseURL, cfg.ClientID, cfg.ClientName, 200)
	if cfg.AuthPausedAt != nil {
		state.Error(fmt.Errorf("authentication paused: invalid client credentials; re-pair required"))
	}
	log.SetOutput(io.MultiWriter(os.Stderr, &observe.LogWriter{State: state, Debug: strings.EqualFold(strings.TrimSpace(cfg.LogLevel), "debug") || strings.EqualFold(strings.TrimSpace(os.Getenv("AUTOFETCH_DEBUG")), "1") || strings.EqualFold(strings.TrimSpace(os.Getenv("AUTOFETCH_DEBUG")), "true")}))
	// Log effective policy, not just CLI flags: client.json and environment
	// variables can enable archive extraction and deletion without flags.
	log.Printf("archive processing: server-managed per job; legacy local archive flags are ignored")

	runtimeFallback := clientruntime.Config{PollIntervalSec: 60, MaxParallelDownloads: cfg.MaxConcurrentDownloads, BandwidthLimitKiBPerSec: 0, HeartbeatIntervalSec: int(cfg.HeartbeatInterval.Seconds()), HeartbeatExtendSec: cfg.HeartbeatExtendSeconds, DedupeClaimTTLSec: cfg.DedupeClaimTTLSeconds}.WithDefaults()
	cfgMgr := clientruntime.NewConfigManager(apiClient, runtimeFallback, func(next clientruntime.Config) {
		if next.ClientName == "" {
			return
		}
		state.SetClientName(next.ClientName)
		if cfg.ClientName == next.ClientName {
			return
		}
		cfg.ClientName = next.ClientName
		if err := config.Persist(*cfg); err != nil {
			log.Printf("persist client name failed: %v", err)
		}
	})
	if cfg.AuthPausedAt == nil && cfg.ClientID != "" && cfg.ClientToken != "" {
		if err := cfgMgr.Refresh(context.Background()); err != nil {
			if api.IsRevoked(err) {
				now := time.Now().UTC()
				config.MarkRevoked(cfg, now)
				cfg.AuthPausedAt = &now
				if persistErr := config.Persist(*cfg); persistErr != nil {
					return nil, nil, fmt.Errorf("persist revoked-client pause: %w", persistErr)
				}
				state.Error(fmt.Errorf("authentication paused: client revoked; re-pair required"))
			}
			log.Printf("runtime config refresh failed, using local defaults: %v", err)
		}
	}

	buildRunner := func(obs observe.Observer) *worker.Runner {
		return &worker.Runner{
			API:                   apiClient,
			DLHTTP:                dlHTTP,
			ClientID:              cfg.ClientID,
			DownloadDir:           cfg.DownloadDir,
			JournalDir:            filepath.Join(filepath.Dir(cfg.ConfigPath), "download-journal"),
			ArchiveQueueDir:       filepath.Join(filepath.Dir(cfg.ConfigPath), "archive-queue"),
			ExtractArchives:       false, // server-owned per-job policy only
			ExtractNestedDepth:    0,
			ExtractLayout:         "separate",
			ExtractDeleteOriginal: false,
			IRCNick:               cfg.IRC.DefaultNick,
			IRCConfig:             cfg,
			PersistConfig:         func() error { return config.Persist(*cfg) },
			HeartbeatInterval:     cfg.HeartbeatInterval,
			HeartbeatExtend:       cfg.HeartbeatExtendSeconds,
			DedupeTTLSeconds:      cfg.DedupeClaimTTLSeconds,
			RuntimeConfig:         cfgMgr,
			Observer:              obs,
		}
	}
	return NewService(cfg, info, apiClient, state, buildRunner), state, nil
}
