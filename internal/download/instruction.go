package download

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/autofetch-de/autofetch-client/internal/api"
)

type ParsedInstruction struct {
	Mode         string
	VideoURL     string
	ExpectedSize int64
	Output       InstructionOutput
	IRC          *IRCInstruction
	DedupeKey    string
	Provider     string
	CandidateID  string
	WebsiteURL   string
	Quality      api.QualityInfo
}

type InstructionOutput struct {
	Dir              string
	FilenameVideo    string
	FilenameSubtitle string
}

type IRCInstruction struct {
	Host              string
	Port              int
	TLS               bool
	TLSVerification   string
	TLSSPKIPinSHA256  string
	Channel           string
	Bot               string
	Package           int
	Filename          string
	DownloadMethod    string
	SourceID          string
	CommandTargetKind string
	CommandTarget     string
	DownloadCommand   string

	// Legacy fallback
	Network string

	// Future-proofing
	PrerequisiteChannels []string
}

type IRCXDCCPlan struct {
	IRC    IRCInstruction
	Output InstructionOutput
}

func ParseDownloadInstruction(inst api.DownloadInstruction, legacyDownloadPath *string) (ParsedInstruction, error) {
	mode := strings.TrimSpace(inst.Mode)
	if mode == "" {
		mode = "single"
	}

	out := InstructionOutput{}
	if inst.Output != nil {
		out.Dir = strings.TrimSpace(inst.Output.Dir)
		out.FilenameVideo = strings.TrimSpace(inst.Output.FilenameVideo)
		out.FilenameSubtitle = strings.TrimSpace(inst.Output.FilenameSubtitle)
	}
	if out.Dir == "" && legacyDownloadPath != nil {
		out.Dir = strings.TrimSpace(*legacyDownloadPath)
	}
	if out.FilenameVideo == "" {
		return ParsedInstruction{}, fmt.Errorf("missing output.filename_video")
	}

	parsed := ParsedInstruction{
		Mode:         mode,
		VideoURL:     strings.TrimSpace(inst.VideoURL),
		ExpectedSize: inst.ExpectedSize,
		Output:       out,
		DedupeKey:    strings.TrimSpace(inst.DedupeKey),
		Provider:     strings.TrimSpace(inst.Provider),
		CandidateID:  strings.TrimSpace(inst.CandidateID),
		WebsiteURL:   strings.TrimSpace(inst.WebsiteURL),
		Quality:      inst.Quality,
	}

	switch mode {
	case "single":
		if inst.DownloadMethod != "" {
			return ParsedInstruction{}, fmt.Errorf("invalid http download_method")
		}
		if parsed.VideoURL == "" {
			return ParsedInstruction{}, fmt.Errorf("missing instruction.video_url")
		}
		return parsed, nil
	case "xdcc":
		plan, err := BuildIRCXDCCPlan(inst, legacyDownloadPath)
		if err != nil {
			return ParsedInstruction{}, err
		}
		parsed.IRC = &plan.IRC
		parsed.Output = plan.Output
		return parsed, nil
	default:
		return ParsedInstruction{}, fmt.Errorf("unsupported instruction.mode: %s", mode)
	}
}

func BuildIRCXDCCPlan(inst api.DownloadInstruction, legacyDownloadPath *string) (*IRCXDCCPlan, error) {
	if strings.TrimSpace(inst.Mode) != "xdcc" {
		return nil, fmt.Errorf("instruction.mode is not xdcc")
	}
	if inst.IRC == nil {
		return nil, fmt.Errorf("missing instruction.irc")
	}

	out := InstructionOutput{}
	if inst.Output != nil {
		out.Dir = strings.TrimSpace(inst.Output.Dir)
		out.FilenameVideo = strings.TrimSpace(inst.Output.FilenameVideo)
		out.FilenameSubtitle = strings.TrimSpace(inst.Output.FilenameSubtitle)
	}
	if out.Dir == "" && legacyDownloadPath != nil {
		out.Dir = strings.TrimSpace(*legacyDownloadPath)
	}
	if out.FilenameVideo == "" {
		return nil, fmt.Errorf("missing output.filename_video")
	}

	ircInfo := inst.IRC
	host := strings.TrimSpace(ircInfo.Host)
	port := ircInfo.Port
	network := strings.TrimSpace(ircInfo.Network)
	channel := strings.TrimSpace(ircInfo.Channel)
	bot := strings.TrimSpace(ircInfo.Bot)
	filename := ircInfo.Filename
	if strings.TrimSpace(filename) == "" {
		filename = out.FilenameVideo
	}

	if channel == "" {
		return nil, fmt.Errorf("missing irc.channel")
	}
	if bot == "" {
		return nil, fmt.Errorf("missing irc.bot")
	}

	if err := validateIRCTLSPolicy(ircInfo.TLS, ircInfo.TLSVerification, ircInfo.TLSSPKIPinSHA256); err != nil {
		return nil, err
	}
	method := strings.TrimSpace(inst.DownloadMethod)
	if method == "" {
		method = "xdcc_numeric"
	}
	var pkg int
	if method == "xdcc_numeric" {
		var err error
		pkg, err = parseRequiredPackage(ircInfo.Package)
		if err != nil {
			return nil, err
		}
	} else if method == "irc_command" {
		if ircInfo.Package != nil {
			return nil, fmt.Errorf("irc_command cannot carry pack number")
		}
		if err := validateIRCCommandSource(inst.SourceID, host, port, channel, bot,
			filename, ircInfo.CommandTargetKind, ircInfo.CommandTarget, ircInfo.DownloadCommand); err != nil {
			return nil, err
		}
		if err := validateCommandOutput(out); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("unsupported irc download_method: %s", method)
	}

	// Prerequisite channel names are server-supplied for both legacy numeric
	// XDCC and text-command downloads. Never permit IRC command injection.
	if len(ircInfo.PrerequisiteChannels) > 12 {
		return nil, fmt.Errorf("too many prerequisite channels")
	}
	for _, ch := range ircInfo.PrerequisiteChannels {
		if !validIRCChannel(ch) {
			return nil, fmt.Errorf("invalid prerequisite channel")
		}
	}

	if host != "" {
		if port <= 0 {
			return nil, fmt.Errorf("missing irc.port")
		}
	} else if network == "" {
		return nil, fmt.Errorf("missing irc.host and no legacy network fallback")
	}

	joinChannels := make([]string, 0, len(ircInfo.PrerequisiteChannels)+1)
	seen := map[string]struct{}{}
	for _, ch := range ircInfo.PrerequisiteChannels {
		ch = strings.TrimSpace(ch)
		if ch == "" {
			continue
		}
		key := strings.ToLower(ch)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		joinChannels = append(joinChannels, ch)
	}
	key := strings.ToLower(channel)
	if _, ok := seen[key]; !ok {
		joinChannels = append(joinChannels, channel)
	}

	return &IRCXDCCPlan{
		IRC: IRCInstruction{
			Host:                 host,
			Port:                 port,
			TLS:                  ircInfo.TLS,
			TLSVerification:      ircInfo.TLSVerification,
			TLSSPKIPinSHA256:     ircInfo.TLSSPKIPinSHA256,
			Channel:              channel,
			Bot:                  bot,
			Package:              pkg,
			Filename:             filename,
			DownloadMethod:       method,
			SourceID:             strings.TrimSpace(inst.SourceID),
			CommandTargetKind:    ircInfo.CommandTargetKind,
			CommandTarget:        ircInfo.CommandTarget,
			DownloadCommand:      ircInfo.DownloadCommand,
			Network:              network,
			PrerequisiteChannels: joinChannels,
		},
		Output: out,
	}, nil
}

var sourceUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$`)
var ircBotNick = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_\[\]{}|` + "`" + `^\-]{0,29}$`)

func hasIRCControls(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}
func validIRCChannel(ch string) bool {
	return strings.HasPrefix(ch, "#") && len(ch) <= 81 && !hasIRCControls(ch) && !strings.ContainsAny(ch, " ,")
}

// The server owns the filename, but it cannot write outside the user's chosen
// download directory, including when a Windows-shaped path is sent to a Unix client.
func validateCommandOutput(out InstructionOutput) error {
	name := out.FilenameVideo
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || hasIRCControls(name) ||
		strings.Contains(name, ":") {
		return fmt.Errorf("unsafe irc_command output filename")
	}
	path := strings.ReplaceAll(out.Dir, "\\", "/")
	if strings.HasPrefix(path, "/") || strings.Contains(path, ":") || hasIRCControls(path) {
		return fmt.Errorf("unsafe irc_command output directory")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return fmt.Errorf("unsafe irc_command output directory")
		}
	}
	return nil
}

// validateIRCCommandSource admits only the exact !Bot <listed filename>
// grammar validated by the server packlist importer. No general IRC scripting.
func validateIRCCommandSource(source, host string, port int, channel, bot, filename, kind, target, command string) error {
	if !sourceUUID.MatchString(source) || host == "" || port <= 0 || port > 65535 ||
		strings.ContainsAny(host, "\r\n \t") || !ircBotNick.MatchString(bot) ||
		!validIRCChannel(channel) {
		return fmt.Errorf("invalid irc_command source")
	}
	if filename == "" || len(filename) > 255 || !utf8.ValidString(filename) ||
		hasIRCControls(filename) || strings.ContainsAny(filename, "/\\") || filename == "." || filename == ".." ||
		strings.TrimSpace(filename) != filename && (!strings.HasPrefix(filename, " ") || strings.TrimSpace(filename[1:]) != filename[1:]) {
		return fmt.Errorf("invalid irc_command filename")
	}
	if len(command) > 512 || !utf8.ValidString(command) || hasIRCControls(command) {
		return fmt.Errorf("invalid irc_command command")
	}
	space := strings.IndexByte(command, ' ')
	if space < 2 || !strings.HasPrefix(command, "!") || !strings.EqualFold(command[1:space], bot) ||
		command[space+1:] != filename {
		return fmt.Errorf("irc_command must match selected bot and filename")
	}
	if kind != "channel" || !strings.EqualFold(target, channel) {
		// Current OmenServe/FW imports only support channel-targeted commands.
		return fmt.Errorf("unsupported irc_command target")
	}
	return nil
}

func parseRequiredPackage(v any) (int, error) {
	switch x := v.(type) {
	case nil:
		return 0, fmt.Errorf("missing irc.package")
	case int:
		if x <= 0 {
			return 0, fmt.Errorf("missing irc.package")
		}
		return x, nil
	case int32:
		if x <= 0 {
			return 0, fmt.Errorf("missing irc.package")
		}
		return int(x), nil
	case int64:
		if x <= 0 {
			return 0, fmt.Errorf("missing irc.package")
		}
		return int(x), nil
	case float64:
		if x <= 0 || x != float64(int(x)) {
			return 0, fmt.Errorf("invalid irc.package")
		}
		return int(x), nil
	case json.Number:
		i, err := x.Int64()
		if err != nil || i <= 0 {
			return 0, fmt.Errorf("invalid irc.package")
		}
		return int(i), nil
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return 0, fmt.Errorf("missing irc.package")
		}
		var n json.Number = json.Number(x)
		i, err := n.Int64()
		if err != nil || i <= 0 {
			return 0, fmt.Errorf("invalid irc.package")
		}
		return int(i), nil
	default:
		return 0, fmt.Errorf("invalid irc.package")
	}
}

func RelativeOutputPath(out InstructionOutput) string {
	if strings.TrimSpace(out.Dir) == "" {
		return filepath.ToSlash(strings.TrimSpace(out.FilenameVideo))
	}
	return filepath.ToSlash(filepath.Join(strings.TrimSpace(out.Dir), strings.TrimSpace(out.FilenameVideo)))
}
