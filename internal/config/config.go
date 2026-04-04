package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Path to the persisted client.json.
	ConfigPath string

	ServerBaseURL string
	ClientName    string

	ClientID    string
	ClientToken string

	PairedAt  *time.Time
	RevokedAt *time.Time

	DownloadDir string

	HeartbeatInterval      time.Duration
	HeartbeatExtendSeconds int
	DedupeClaimTTLSeconds  int
	MaxConcurrentDownloads int
	LogLevel               string

	// Pairing / lifecycle flags
	RePair          bool
	Headless        bool
	PrintCodeOnly   bool
	EnableWebUI     bool
	OpenBrowser     bool
	NoBrowser       bool
	WebUIListenAddr string
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func atoiEnv(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func boolEnv(k string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(k)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func Load() Config {
	var c Config

	// Defaults
	c.ServerBaseURL = "https://autofetch.de"
	c.ClientName = "autofetch"
	c.DownloadDir = "./downloads"

	// Default config path
	c.ConfigPath = defaultConfigPath()

	// Load persisted config first (if present)
	_ = loadFromFile(&c, c.ConfigPath)

	// ENV overrides (only if set)
	if v := os.Getenv("SERVER_BASE_URL"); v != "" {
		c.ServerBaseURL = v
	}
	if v := os.Getenv("CLIENT_NAME"); v != "" {
		c.ClientName = v
	}
	if v := os.Getenv("CLIENT_ID"); v != "" {
		c.ClientID = v
	}
	if v := os.Getenv("CLIENT_TOKEN"); v != "" {
		c.ClientToken = v
	}
	if v := os.Getenv("DOWNLOAD_DIR"); v != "" {
		c.DownloadDir = v
	}

	// Recommended: interval 15–30s, extend 600s. Default to 20s for safety.
	c.HeartbeatInterval = time.Duration(atoiEnv("HEARTBEAT_INTERVAL_SECONDS", 20)) * time.Second
	c.HeartbeatExtendSeconds = atoiEnv("HEARTBEAT_EXTEND_SECONDS", 600)
	c.DedupeClaimTTLSeconds = atoiEnv("DEDUPE_CLAIM_TTL_SECONDS", 1800)

	c.MaxConcurrentDownloads = atoiEnv("MAX_CONCURRENT_DOWNLOADS", 1)
	c.LogLevel = getenv("LOG_LEVEL", "info")
	c.EnableWebUI = boolEnv("AUTOFETCH_WEBUI", true)
	c.OpenBrowser = boolEnv("AUTOFETCH_OPEN_BROWSER", false)
	c.NoBrowser = boolEnv("AUTOFETCH_NO_BROWSER", false)
	c.WebUIListenAddr = getenv("AUTOFETCH_WEBUI_ADDR", "127.0.0.1:23324")

	// Flags override everything
	flag.StringVar(&c.ConfigPath, "config", c.ConfigPath, "Path to client.json")
	flag.StringVar(&c.ServerBaseURL, "server", c.ServerBaseURL, "SERVER_BASE_URL")
	flag.StringVar(&c.ClientName, "name", c.ClientName, "CLIENT_NAME")
	flag.StringVar(&c.ClientID, "client-id", c.ClientID, "CLIENT_ID (uuid)")
	flag.StringVar(&c.ClientToken, "token", c.ClientToken, "CLIENT_TOKEN (X-Client-Token)")
	flag.StringVar(&c.DownloadDir, "dir", c.DownloadDir, "DOWNLOAD_DIR")
	flag.BoolVar(&c.RePair, "re-pair", false, "Start pairing flow (discard stored client_id/token and register this device again)")
	flag.BoolVar(&c.PrintCodeOnly, "print-code-only", false, "Print pairing code only (headless pairing UX)")
	flag.BoolVar(&c.EnableWebUI, "webui", c.EnableWebUI, "Enable local status Web UI")
	flag.BoolVar(&c.Headless, "headless", false, "Run without UI/tray (headless mode)")
	flag.BoolVar(&c.OpenBrowser, "open-browser", c.OpenBrowser, "Open the local status Web UI in a browser")
	flag.BoolVar(&c.NoBrowser, "no-browser", c.NoBrowser, "Do not open the local status Web UI in a browser")
	flag.StringVar(&c.WebUIListenAddr, "webui-addr", c.WebUIListenAddr, "Listen address for local status Web UI")

	hbi := flag.Int("heartbeat-interval", int(c.HeartbeatInterval.Seconds()), "HEARTBEAT_INTERVAL_SECONDS")
	flag.IntVar(&c.HeartbeatExtendSeconds, "heartbeat-extend", c.HeartbeatExtendSeconds, "HEARTBEAT_EXTEND_SECONDS")
	flag.IntVar(&c.DedupeClaimTTLSeconds, "dedupe-ttl", c.DedupeClaimTTLSeconds, "DEDUPE_CLAIM_TTL_SECONDS")
	flag.IntVar(&c.MaxConcurrentDownloads, "max-concurrent", c.MaxConcurrentDownloads, "MAX_CONCURRENT_DOWNLOADS")
	flag.StringVar(&c.LogLevel, "log-level", c.LogLevel, "LOG_LEVEL (info|debug)")

	flag.Parse()
	c.HeartbeatInterval = time.Duration(*hbi) * time.Second

	if c.Headless {
		c.EnableWebUI = false
		c.OpenBrowser = false
	}
	if c.NoBrowser {
		c.OpenBrowser = false
	}

	// If --config points somewhere else than the default, reload persisted config from there,
	// but do NOT clobber already provided flags.
	// We only do this when the user explicitly passed --config.
	if flagPassed("config") {
		persisted := Config{ConfigPath: c.ConfigPath}
		_ = loadFromFile(&persisted, c.ConfigPath)
		// Only fill missing creds from file if flags/env did not provide them.
		if c.ClientID == "" {
			c.ClientID = persisted.ClientID
		}
		if c.ClientToken == "" {
			c.ClientToken = persisted.ClientToken
		}
		if c.ServerBaseURL == "" {
			c.ServerBaseURL = persisted.ServerBaseURL
		}
		if c.ClientName == "" {
			c.ClientName = persisted.ClientName
		}
		if c.PairedAt == nil {
			c.PairedAt = persisted.PairedAt
		}
		if c.RevokedAt == nil {
			c.RevokedAt = persisted.RevokedAt
		}
	}

	return c
}

// Persist writes the current config to client.json (credentials + lifecycle markers).
// It intentionally does not store operational tuning fields (heartbeat intervals, etc.).
func Persist(c Config) error {
	if c.ConfigPath == "" {
		return fmt.Errorf("config path missing")
	}
	// Ensure directory exists
	dir := filepath.Dir(c.ConfigPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	payload := struct {
		ServerBaseURL string     `json:"server_base_url"`
		ClientID      string     `json:"client_id"`
		ClientToken   string     `json:"client_token"`
		PairedAt      *time.Time `json:"paired_at,omitempty"`
		ClientName    string     `json:"client_name"`
		DownloadDir   string     `json:"download_dir"`
		RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	}{
		ServerBaseURL: c.ServerBaseURL,
		ClientID:      c.ClientID,
		ClientToken:   c.ClientToken,
		PairedAt:      c.PairedAt,
		ClientName:    c.ClientName,
		DownloadDir:   c.DownloadDir,
		RevokedAt:     c.RevokedAt,
	}

	bs, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	bs = append(bs, '\n')
	return os.WriteFile(c.ConfigPath, bs, 0o600)
}

func ClearCredentials(c *Config) error {
	c.ClientID = ""
	c.ClientToken = ""
	c.PairedAt = nil
	c.RevokedAt = nil
	if c.ConfigPath == "" {
		return nil
	}
	return Persist(*c)
}

func MarkRevoked(c *Config, at time.Time) {
	c.RevokedAt = &at
}

func defaultConfigPath() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		// Fallback to current dir
		return "./client.json"
	}
	return filepath.Join(base, "autofetch", "client.json")
}

func loadFromFile(c *Config, path string) error {
	bs, err := os.ReadFile(path)
	if err != nil {
		if errorsIs(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}

	var payload struct {
		ServerBaseURL string     `json:"server_base_url"`
		ClientID      string     `json:"client_id"`
		ClientToken   string     `json:"client_token"`
		PairedAt      *time.Time `json:"paired_at,omitempty"`
		ClientName    string     `json:"client_name"`
		DownloadDir   string     `json:"download_dir"`
		RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	}
	if err := json.Unmarshal(bs, &payload); err != nil {
		return err
	}
	if payload.ServerBaseURL != "" {
		c.ServerBaseURL = payload.ServerBaseURL
	}
	if payload.ClientName != "" {
		c.ClientName = payload.ClientName
	}
	if payload.ClientID != "" {
		c.ClientID = payload.ClientID
	}
	if payload.DownloadDir != "" {
		c.DownloadDir = payload.DownloadDir
	}
	if payload.ClientToken != "" {
		c.ClientToken = payload.ClientToken
	}
	c.PairedAt = payload.PairedAt
	c.RevokedAt = payload.RevokedAt
	return nil
}

// flagPassed reports whether a CLI flag was explicitly provided.
func flagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// errorsIs is a tiny helper to avoid importing errors just for Is.
func errorsIs(err, target error) bool {
	// Go 1.20+ supports errors.Is; keep it local for minimal imports.
	type iser interface{ Is(error) bool }
	if err == target {
		return true
	}
	if e, ok := err.(iser); ok {
		return e.Is(target)
	}
	return false
}
