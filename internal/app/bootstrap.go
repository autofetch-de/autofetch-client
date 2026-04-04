package app

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/xtorian/autofetch-client/internal/api"
	"github.com/xtorian/autofetch-client/internal/config"
	"github.com/xtorian/autofetch-client/internal/observe"
	clientruntime "github.com/xtorian/autofetch-client/internal/runtime"
	"github.com/xtorian/autofetch-client/internal/worker"
)

func Bootstrap(cfg *config.Config, version string) (*Service, *observe.State, error) {
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
	apiClient.HTTP.Timeout = 60 * time.Second
	dlHTTP := &http.Client{Timeout: 0}
	clientsCfg := config.LoadClientsConfig()

	state := observe.NewState(cfg.ServerBaseURL, cfg.ClientID, cfg.ClientName, 200)
	log.SetOutput(io.MultiWriter(os.Stderr, &observe.LogWriter{State: state}))

	runtimeFallback := clientruntime.Config{
		PollIntervalSec:         60,
		MaxParallelDownloads:    cfg.MaxConcurrentDownloads,
		BandwidthLimitKiBPerSec: 0,
		HeartbeatIntervalSec:    int(cfg.HeartbeatInterval.Seconds()),
		HeartbeatExtendSec:      cfg.HeartbeatExtendSeconds,
		DedupeClaimTTLSec:       cfg.DedupeClaimTTLSeconds,
	}.WithDefaults()
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
			API:    apiClient,
			DLHTTP: dlHTTP,

			ClientID: cfg.ClientID,

			DownloadDir: cfg.DownloadDir,
			IRCNick:     clientsCfg.IRCNick,

			HeartbeatInterval: cfg.HeartbeatInterval,
			HeartbeatExtend:   cfg.HeartbeatExtendSeconds,
			DedupeTTLSeconds:  cfg.DedupeClaimTTLSeconds,

			RuntimeConfig: cfgMgr,
			Observer:      obs,
		}
	}

	return NewService(cfg, version, apiClient, state, buildRunner), state, nil
}
