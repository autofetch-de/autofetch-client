package irc

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/config"
)

var adjectives = []string{
	"silent", "blue", "lunar", "pixel", "crystal", "rapid", "mellow", "solar",
	"misty", "brisk", "amber", "cobalt", "frozen", "gentle", "shadow", "bright",
}

var nouns = []string{
	"Falcon", "Tiger", "Panda", "Nova", "River", "Wolf", "Leaf", "Stone",
	"Otter", "Comet", "Sparrow", "Harbor", "Forest", "Echo", "Raven", "Meadow",
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func NormalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimPrefix(host, "irc://")
	host = strings.TrimPrefix(host, "ircs://")
	host = strings.TrimSuffix(host, "/")
	return host
}

func NormalizeChannel(ch string) string {
	ch = strings.TrimSpace(ch)
	if ch == "" {
		return ""
	}
	if !strings.HasPrefix(ch, "#") && !strings.HasPrefix(ch, "&") {
		ch = "#" + ch
	}
	return ch
}

func SanitizeNick(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}

	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '|', r == '[', r == ']', r == '\\', r == '{', r == '}':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "u" + out
	}
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

func GenerateDefaultNick() string {
	a := adjectives[rand.Intn(len(adjectives))]
	n := nouns[rand.Intn(len(nouns))]
	suffix := rand.Intn(100)
	return SanitizeNick(fmt.Sprintf("%s%s%d", a, n, suffix))
}

func EnsureDefaultNick(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	nick := SanitizeNick(cfg.IRC.DefaultNick)
	if nick != "" {
		if nick != cfg.IRC.DefaultNick {
			cfg.IRC.DefaultNick = nick
			return true
		}
		return false
	}

	legacy := SanitizeNick(strings.TrimSpace(cfg.ClientName))
	if legacy != "" && !strings.EqualFold(legacy, "autofetch") {
		cfg.IRC.DefaultNick = legacy
		return true
	}

	cfg.IRC.DefaultNick = GenerateDefaultNick()
	return true
}

func WithNetworkDefaults(n config.IRCNetwork, defaultNick string) config.IRCNetwork {
	n.Host = NormalizeHost(n.Host)
	if n.Port <= 0 {
		if n.TLS {
			n.Port = 6697
		} else {
			n.Port = 6667
		}
	}
	n.Nick = SanitizeNick(n.Nick)
	n.Username = strings.TrimSpace(n.Username)
	n.Realname = strings.TrimSpace(n.Realname)

	if n.Username == "" {
		if n.Nick != "" {
			n.Username = n.Nick
		} else {
			n.Username = defaultNick
		}
	}
	if n.Realname == "" {
		n.Realname = "autofetch client"
	}

	for i := range n.Channels {
		n.Channels[i] = NormalizeChannel(n.Channels[i])
	}

	n.NickServ.Command = strings.TrimSpace(n.NickServ.Command)
	if n.NickServ.Command == "" {
		n.NickServ.Command = "IDENTIFY"
	}

	n.SASL.Username = strings.TrimSpace(n.SASL.Username)
	return n
}

func FindNetwork(cfg *config.Config, host string, port int) (int, *config.IRCNetwork) {
	if cfg == nil {
		return -1, nil
	}
	host = NormalizeHost(host)
	for i := range cfg.IRC.Networks {
		n := &cfg.IRC.Networks[i]
		if NormalizeHost(n.Host) == host {
			if port <= 0 || n.Port == port {
				return i, n
			}
		}
	}
	return -1, nil
}

func EnsureNetwork(cfg *config.Config, host string, port int, tls bool, channel string) (int, *config.IRCNetwork, bool) {
	if cfg == nil {
		return -1, nil, false
	}
	EnsureDefaultNick(cfg)

	channel = NormalizeChannel(channel)
	if idx, n := FindNetwork(cfg, host, port); n != nil {
		before := *n
		changed := false
		*n = WithNetworkDefaults(*n, cfg.IRC.DefaultNick)
		if before.Name != n.Name || before.Host != n.Host || before.Port != n.Port || before.TLS != n.TLS ||
			before.Nick != n.Nick || before.Username != n.Username || before.Realname != n.Realname ||
			before.NickServ.Enabled != n.NickServ.Enabled || before.NickServ.Command != n.NickServ.Command ||
			before.NickServ.Password != n.NickServ.Password || before.SASL.Enabled != n.SASL.Enabled ||
			before.SASL.Username != n.SASL.Username || before.SASL.Password != n.SASL.Password {
			changed = true
		}
		if channel != "" && !containsString(n.Channels, channel) {
			n.Channels = append(n.Channels, channel)
			changed = true
		}
		return idx, n, changed
	}

	n := config.IRCNetwork{
		Name:     strings.TrimSpace(host),
		Host:     NormalizeHost(host),
		Port:     port,
		TLS:      tls,
		NickServ: config.IRCNickServ{Command: "IDENTIFY"},
	}
	if channel != "" {
		n.Channels = []string{channel}
	}
	n = WithNetworkDefaults(n, cfg.IRC.DefaultNick)
	cfg.IRC.Networks = append(cfg.IRC.Networks, n)
	return len(cfg.IRC.Networks) - 1, &cfg.IRC.Networks[len(cfg.IRC.Networks)-1], true
}

func EffectiveNick(cfg *config.Config, n *config.IRCNetwork) string {
	if cfg != nil {
		EnsureDefaultNick(cfg)
	}
	if n != nil {
		if nick := SanitizeNick(n.Nick); nick != "" {
			return nick
		}
	}
	if cfg != nil {
		return SanitizeNick(cfg.IRC.DefaultNick)
	}
	return ""
}

func NickCandidates(base string) []string {
	base = SanitizeNick(base)
	if base == "" {
		base = "guest"
	}
	candidates := []string{
		base,
		base + "_",
		base + "_" + strconv.Itoa(rand.Intn(90)+10),
		base + "|2",
		base + "|" + strconv.Itoa(rand.Intn(9)+3),
	}
	out := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, c := range candidates {
		c = SanitizeNick(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}
