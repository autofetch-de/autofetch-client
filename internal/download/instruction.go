package download

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

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
	Host     string
	Port     int
	TLS      bool
	Channel  string
	Bot      string
	Package  int
	Filename string

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
	filename := strings.TrimSpace(ircInfo.Filename)
	if filename == "" {
		filename = out.FilenameVideo
	}

	if channel == "" {
		return nil, fmt.Errorf("missing irc.channel")
	}
	if bot == "" {
		return nil, fmt.Errorf("missing irc.bot")
	}

	pkg, err := parseRequiredPackage(ircInfo.Package)
	if err != nil {
		return nil, err
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
			Channel:              channel,
			Bot:                  bot,
			Package:              pkg,
			Filename:             filename,
			Network:              network,
			PrerequisiteChannels: joinChannels,
		},
		Output: out,
	}, nil
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
