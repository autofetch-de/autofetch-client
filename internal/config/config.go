package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/localization"
)

type IRCNickServ struct {
	Enabled  bool   `json:"enabled"`
	Password string `json:"password"`
	Command  string `json:"command"`
}

type IRCSASL struct {
	Enabled  bool   `json:"enabled"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type IRCNetwork struct {
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	TLS      bool     `json:"tls"`
	Channels []string `json:"channels,omitempty"`

	Nick     string `json:"nick"`
	Username string `json:"username"`
	Realname string `json:"realname"`

	NickServ IRCNickServ `json:"nickserv"`
	SASL     IRCSASL     `json:"sasl"`
}

type IRCConfig struct {
	DefaultNick       string       `json:"default_nick"`
	Networks          []IRCNetwork `json:"networks"`
	ReverseDCCEnabled bool         `json:"reverse_dcc_enabled"`
	ReverseDCCPortMin int          `json:"reverse_dcc_port_min,omitempty"`
	ReverseDCCPortMax int          `json:"reverse_dcc_port_max,omitempty"`
	AutoRegister      bool         `json:"-"`
	RegistrationEmail string       `json:"-"`
}

type IRCSecrets struct {
	AutoRegister      bool                         `json:"auto_register"`
	RegistrationEmail string                       `json:"registration_email"`
	IRC               map[string]IRCNetworkSecrets `json:"irc"`
}

type IRCNetworkSecrets struct {
	NickServ IRCNickServSecrets `json:"nickserv"`
	SASL     IRCSASLSecrets     `json:"sasl"`
}

type IRCNickServSecrets struct {
	Password string `json:"password"`
}

type IRCSASLSecrets struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Config struct {
	ConfigPath string

	ServerBaseURL string
	ClientName    string

	ClientID    string
	ClientToken string

	PairedAt  *time.Time
	RevokedAt *time.Time

	DownloadDir string
	IRC         IRCConfig

	HeartbeatInterval      time.Duration
	HeartbeatExtendSeconds int
	DedupeClaimTTLSeconds  int
	MaxConcurrentDownloads int
	LogLevel               string

	RePair          bool
	Headless        bool
	PrintCodeOnly   bool
	EnableWebUI     bool
	OpenBrowser     bool
	NoBrowser       bool
	WebUIListenAddr string
}

func (c IRCConfig) WithDefaults() IRCConfig {
	if c.Networks == nil {
		c.Networks = []IRCNetwork{}
	}
	c.DefaultNick = strings.TrimSpace(c.DefaultNick)
	c.RegistrationEmail = strings.TrimSpace(c.RegistrationEmail)
	if c.ReverseDCCPortMin <= 0 {
		c.ReverseDCCPortMin = 36080
	}
	if c.ReverseDCCPortMax <= 0 {
		c.ReverseDCCPortMax = c.ReverseDCCPortMin + 10
	}
	if c.ReverseDCCPortMax < c.ReverseDCCPortMin {
		c.ReverseDCCPortMin, c.ReverseDCCPortMax = c.ReverseDCCPortMax, c.ReverseDCCPortMin
	}
	for i := range c.Networks {
		n := &c.Networks[i]
		n.Name = strings.TrimSpace(n.Name)
		n.Host = strings.TrimSpace(n.Host)
		if n.Port <= 0 {
			if n.TLS {
				n.Port = 6697
			} else {
				n.Port = 6667
			}
		}
		n.Nick = strings.TrimSpace(n.Nick)
		n.Username = strings.TrimSpace(n.Username)
		n.Realname = strings.TrimSpace(n.Realname)
		n.NickServ.Password = strings.TrimSpace(n.NickServ.Password)
		if n.NickServ.Command == "" {
			n.NickServ.Command = "IDENTIFY"
		}
		n.SASL.Username = strings.TrimSpace(n.SASL.Username)
		n.SASL.Password = strings.TrimSpace(n.SASL.Password)
	}
	return c
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

func Load(l *localization.Localizer) Config {
	if l == nil {
		l = localization.New(localization.English)
	}
	var c Config
	c.ServerBaseURL = "https://autofetch.de"
	c.ClientName = "autofetch"
	c.DownloadDir = "./downloads"
	c.ConfigPath = defaultConfigPath()
	c.IRC = IRCConfig{Networks: []IRCNetwork{}}

	_ = loadFromFile(&c, c.ConfigPath)

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

	c.HeartbeatInterval = time.Duration(atoiEnv("HEARTBEAT_INTERVAL_SECONDS", 20)) * time.Second
	c.HeartbeatExtendSeconds = atoiEnv("HEARTBEAT_EXTEND_SECONDS", 600)
	c.DedupeClaimTTLSeconds = atoiEnv("DEDUPE_CLAIM_TTL_SECONDS", 1800)
	c.MaxConcurrentDownloads = atoiEnv("MAX_CONCURRENT_DOWNLOADS", 1)
	c.LogLevel = getenv("LOG_LEVEL", "info")
	c.EnableWebUI = boolEnv("AUTOFETCH_WEBUI", true)
	c.OpenBrowser = boolEnv("AUTOFETCH_OPEN_BROWSER", false)
	c.NoBrowser = boolEnv("AUTOFETCH_NO_BROWSER", false)
	c.WebUIListenAddr = getenv("AUTOFETCH_WEBUI_ADDR", "127.0.0.1:23324")

	flag.StringVar(&c.ConfigPath, "config", c.ConfigPath, l.T("flag.config"))
	flag.StringVar(&c.ServerBaseURL, "server", c.ServerBaseURL, "SERVER_BASE_URL")
	flag.StringVar(&c.ClientName, "name", c.ClientName, "CLIENT_NAME")
	flag.StringVar(&c.ClientID, "client-id", c.ClientID, "CLIENT_ID (uuid)")
	flag.StringVar(&c.ClientToken, "token", c.ClientToken, "CLIENT_TOKEN (X-Client-Token)")
	flag.StringVar(&c.DownloadDir, "dir", c.DownloadDir, "DOWNLOAD_DIR")
	flag.BoolVar(&c.RePair, "re-pair", false, l.T("flag.repair"))
	flag.BoolVar(&c.PrintCodeOnly, "print-code-only", false, l.T("flag.print_code_only"))
	flag.BoolVar(&c.EnableWebUI, "webui", c.EnableWebUI, l.T("flag.webui"))
	flag.BoolVar(&c.Headless, "headless", false, l.T("flag.headless"))
	flag.BoolVar(&c.OpenBrowser, "open-browser", c.OpenBrowser, l.T("flag.open_browser"))
	flag.BoolVar(&c.NoBrowser, "no-browser", c.NoBrowser, l.T("flag.no_browser"))
	flag.StringVar(&c.WebUIListenAddr, "webui-addr", c.WebUIListenAddr, l.T("flag.webui_addr"))

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

	if flagPassed("config") {
		persisted := Config{ConfigPath: c.ConfigPath}
		_ = loadFromFile(&persisted, c.ConfigPath)
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
		if c.DownloadDir == "" {
			c.DownloadDir = persisted.DownloadDir
		}
		if c.PairedAt == nil {
			c.PairedAt = persisted.PairedAt
		}
		if c.RevokedAt == nil {
			c.RevokedAt = persisted.RevokedAt
		}
		if c.IRC.DefaultNick == "" && len(c.IRC.Networks) == 0 {
			c.IRC = persisted.IRC.WithDefaults()
		}
	}

	c.IRC = c.IRC.WithDefaults()
	return c
}

func Persist(c Config) error {
	if c.ConfigPath == "" {
		return fmt.Errorf("config path missing")
	}
	dir := filepath.Dir(c.ConfigPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	sanitizedIRC, secrets := splitIRCSecrets(c.IRC.WithDefaults())
	if err := persistIRCSecrets(c.ConfigPath, secrets); err != nil {
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
		IRC           IRCConfig  `json:"irc"`
	}{
		ServerBaseURL: c.ServerBaseURL,
		ClientID:      c.ClientID,
		ClientToken:   c.ClientToken,
		PairedAt:      c.PairedAt,
		ClientName:    c.ClientName,
		DownloadDir:   c.DownloadDir,
		RevokedAt:     c.RevokedAt,
		IRC:           sanitizedIRC,
	}
	bs, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	bs = append(bs, '\n')
	tmpPath := c.ConfigPath + ".tmp"
	if err := os.WriteFile(tmpPath, bs, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, c.ConfigPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Chmod(c.ConfigPath, 0o600)
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
		return "./client.json"
	}
	return filepath.Join(base, "autofetch", "client.json")
}

func loadFromFile(c *Config, path string) error {
	bs, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
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
		IRC           IRCConfig  `json:"irc"`
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
	c.IRC = payload.IRC.WithDefaults()

	secrets, err := loadIRCSecrets(path)
	if err != nil {
		return err
	}
	c.IRC = mergeIRCSecrets(c.IRC, secrets).WithDefaults()
	return nil
}

func secretsPath(baseConfigPath string) string {
	if strings.TrimSpace(baseConfigPath) == "" {
		baseConfigPath = defaultConfigPath()
	}
	return filepath.Join(filepath.Dir(baseConfigPath), "irc-secrets.json")
}

func loadIRCSecrets(baseConfigPath string) (IRCSecrets, error) {
	path := secretsPath(baseConfigPath)
	bs, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return IRCSecrets{IRC: map[string]IRCNetworkSecrets{}}, nil
		}
		return IRCSecrets{}, err
	}
	var secrets IRCSecrets
	if err := json.Unmarshal(bs, &secrets); err != nil {
		return IRCSecrets{}, err
	}
	if secrets.IRC == nil {
		secrets.IRC = map[string]IRCNetworkSecrets{}
	}
	return secrets, nil
}

func persistIRCSecrets(baseConfigPath string, secrets IRCSecrets) error {
	path := secretsPath(baseConfigPath)
	if secrets.IRC == nil {
		secrets.IRC = map[string]IRCNetworkSecrets{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	bs, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return err
	}
	bs = append(bs, '\n')
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, bs, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Chmod(path, 0o600)
}

func splitIRCSecrets(ircCfg IRCConfig) (IRCConfig, IRCSecrets) {
	sanitized := ircCfg.WithDefaults()
	if len(sanitized.Networks) > 0 {
		copied := make([]IRCNetwork, len(sanitized.Networks))
		for i, n := range sanitized.Networks {
			copied[i] = n
			if len(n.Channels) > 0 {
				copied[i].Channels = append([]string(nil), n.Channels...)
			}
		}
		sanitized.Networks = copied
	}
	secrets := IRCSecrets{
		AutoRegister:      sanitized.AutoRegister,
		RegistrationEmail: strings.TrimSpace(sanitized.RegistrationEmail),
		IRC:               map[string]IRCNetworkSecrets{},
	}
	for i := range sanitized.Networks {
		n := &sanitized.Networks[i]
		key := networkSecretKey(*n)
		if key == "" {
			continue
		}
		var sec IRCNetworkSecrets
		sec.NickServ.Password = strings.TrimSpace(n.NickServ.Password)
		sec.SASL.Username = strings.TrimSpace(n.SASL.Username)
		sec.SASL.Password = strings.TrimSpace(n.SASL.Password)
		if sec.NickServ.Password != "" || sec.SASL.Username != "" || sec.SASL.Password != "" {
			secrets.IRC[key] = sec
		}
		n.NickServ.Password = ""
		n.SASL.Password = ""
		n.SASL.Username = ""
	}
	sanitized.AutoRegister = false
	sanitized.RegistrationEmail = ""
	return sanitized.WithDefaults(), secrets
}

func mergeIRCSecrets(ircCfg IRCConfig, secrets IRCSecrets) IRCConfig {
	merged := ircCfg.WithDefaults()
	merged.AutoRegister = secrets.AutoRegister
	merged.RegistrationEmail = strings.TrimSpace(secrets.RegistrationEmail)
	for i := range merged.Networks {
		n := &merged.Networks[i]
		key := networkSecretKey(*n)
		if key == "" {
			continue
		}
		sec, ok := secrets.IRC[key]
		if !ok {
			sec, ok = secrets.IRC[strings.TrimSpace(strings.ToLower(n.Host))]
		}
		if !ok {
			continue
		}
		if strings.TrimSpace(sec.NickServ.Password) != "" {
			n.NickServ.Password = strings.TrimSpace(sec.NickServ.Password)
		}
		if strings.TrimSpace(sec.SASL.Username) != "" {
			n.SASL.Username = strings.TrimSpace(sec.SASL.Username)
		}
		if strings.TrimSpace(sec.SASL.Password) != "" {
			n.SASL.Password = strings.TrimSpace(sec.SASL.Password)
		}
	}
	return merged
}

func networkSecretKey(n IRCNetwork) string {
	host := strings.TrimSpace(strings.ToLower(n.Host))
	if host != "" {
		port := n.Port
		if port <= 0 {
			if n.TLS {
				port = 6697
			} else {
				port = 6667
			}
		}
		scheme := "irc"
		if n.TLS {
			scheme = "ircs"
		}
		return fmt.Sprintf("%s://%s:%d", scheme, host, port)
	}
	return strings.TrimSpace(strings.ToLower(n.Name))
}

func flagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// errorsIs keeps compatibility with older config helpers.
func errorsIs(err, target error) bool {
	return err == target
}
