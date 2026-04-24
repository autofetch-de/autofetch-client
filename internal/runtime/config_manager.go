package runtime

import (
	"context"
	"sync"

	"github.com/autofetch-de/autofetch-client/internal/api"
)

type UpdateFunc func(Config)

type ConfigManager struct {
	api *api.Client

	mu       sync.RWMutex
	cfg      Config
	onUpdate UpdateFunc
}

func NewConfigManager(apiClient *api.Client, fallback Config, onUpdate UpdateFunc) *ConfigManager {
	return &ConfigManager{
		api:      apiClient,
		cfg:      fallback.WithDefaults(),
		onUpdate: onUpdate,
	}
}

func (m *ConfigManager) Current() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func (m *ConfigManager) Refresh(ctx context.Context) error {
	cfg, found, err := m.api.GetRuntimeConfig(ctx)
	if err != nil {
		return err
	}
	if !found || cfg == nil {
		return nil
	}

	next := Config{
		ClientName:              cfg.ClientName,
		PollIntervalSec:         cfg.PollIntervalSec,
		MaxParallelDownloads:    cfg.MaxParallelDownloads,
		BandwidthLimitKiBPerSec: cfg.BandwidthLimitKiBPerSec,
		HeartbeatIntervalSec:    cfg.HeartbeatIntervalSec,
		HeartbeatExtendSec:      cfg.HeartbeatExtendSec,
		DedupeClaimTTLSec:       cfg.DedupeClaimTTLSec,
	}.WithDefaults()

	m.mu.Lock()
	if next.ClientName == "" {
		next.ClientName = m.cfg.ClientName
	}
	m.cfg = next
	m.mu.Unlock()

	if m.onUpdate != nil {
		m.onUpdate(next)
	}
	return nil
}
