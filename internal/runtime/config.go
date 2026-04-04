package runtime

import "time"

// Config contains server-driven runtime settings for the client.
// These settings are intentionally not persisted into client.json.
type Config struct {
	ClientName              string `json:"client_name,omitempty"`
	PollIntervalSec         int    `json:"poll_interval_sec"`
	MaxParallelDownloads    int    `json:"max_parallel_downloads"`
	BandwidthLimitKiBPerSec int    `json:"bandwidth_limit_kib_per_sec"`
	HeartbeatIntervalSec    int    `json:"heartbeat_interval_sec"`
	HeartbeatExtendSec      int    `json:"heartbeat_extend_sec"`
	DedupeClaimTTLSec       int    `json:"dedupe_claim_ttl_sec"`
}

func DefaultConfig() Config {
	return Config{
		PollIntervalSec:         60,
		MaxParallelDownloads:    1,
		BandwidthLimitKiBPerSec: 0,
		HeartbeatIntervalSec:    20,
		HeartbeatExtendSec:      600,
		DedupeClaimTTLSec:       1800,
	}
}

func (c Config) WithDefaults() Config {
	d := DefaultConfig()
	if c.ClientName != "" {
		d.ClientName = c.ClientName
	}
	if c.PollIntervalSec > 0 {
		d.PollIntervalSec = c.PollIntervalSec
	}
	if c.MaxParallelDownloads > 0 {
		d.MaxParallelDownloads = c.MaxParallelDownloads
	}
	if c.BandwidthLimitKiBPerSec >= 0 {
		d.BandwidthLimitKiBPerSec = c.BandwidthLimitKiBPerSec
	}
	if c.HeartbeatIntervalSec > 0 {
		d.HeartbeatIntervalSec = c.HeartbeatIntervalSec
	}
	if c.HeartbeatExtendSec > 0 {
		d.HeartbeatExtendSec = c.HeartbeatExtendSec
	}
	if c.DedupeClaimTTLSec > 0 {
		d.DedupeClaimTTLSec = c.DedupeClaimTTLSec
	}
	return d
}

func (c Config) HeartbeatInterval() time.Duration {
	sec := c.HeartbeatIntervalSec
	if sec <= 0 {
		sec = DefaultConfig().HeartbeatIntervalSec
	}
	return time.Duration(sec) * time.Second
}
