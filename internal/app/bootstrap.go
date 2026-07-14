package app

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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
	if cfg.RevokedAt != nil && !cfg.RePair {
		return nil, nil, fmt.Errorf("client is marked revoked since %s; please re-pair manually (--re-pair)", cfg.RevokedAt.Format(time.RFC3339))
	}
	if cfg.RePair {
		_ = config.ClearCredentials(cfg)
	}

	apiClient := api.New(cfg.ServerBaseURL, cfg.ClientID, cfg.ClientToken)
	apiClient.Metadata = api.ClientMetadata{Version: info.Version, Platform: info.Platform, Arch: info.Arch, Variant: info.Variant, BuildCommit: info.BuildCommit, BuildDate: info.BuildDate}
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
	log.SetOutput(io.MultiWriter(os.Stderr, &observe.LogWriter{State: state, Debug: strings.EqualFold(strings.TrimSpace(cfg.LogLevel), "debug") || strings.EqualFold(strings.TrimSpace(os.Getenv("AUTOFETCH_DEBUG")), "1") || strings.EqualFold(strings.TrimSpace(os.Getenv("AUTOFETCH_DEBUG")), "true")}))

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
	if cfg.ClientID != "" && cfg.ClientToken != "" {
		if err := cfgMgr.Refresh(context.Background()); err != nil {
			if api.IsRevoked(err) {
				now := time.Now().UTC()
				config.MarkRevoked(cfg, now)
				_ = config.Persist(*cfg)
				return nil, nil, fmt.Errorf("client revoked or deleted by server")
			}
			log.Printf("runtime config refresh failed, using local defaults: %v", err)
		}
	}

	buildRunner := func(obs observe.Observer) *worker.Runner {
		return &worker.Runner{
			API:               apiClient,
			DLHTTP:            dlHTTP,
			ClientID:          cfg.ClientID,
			DownloadDir:       cfg.DownloadDir,
			IRCNick:           cfg.IRC.DefaultNick,
			IRCConfig:         cfg,
			PersistConfig:     func() error { return config.Persist(*cfg) },
			HeartbeatInterval: cfg.HeartbeatInterval,
			HeartbeatExtend:   cfg.HeartbeatExtendSeconds,
			DedupeTTLSeconds:  cfg.DedupeClaimTTLSeconds,
			RuntimeConfig:     cfgMgr,
			Observer:          obs,
		}
	}
	return NewService(cfg, info, apiClient, state, buildRunner), state, nil
}
