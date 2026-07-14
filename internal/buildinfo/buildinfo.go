package buildinfo

import (
	"fmt"
	"runtime"
	"strings"
)

var (
	Version     = "dev"
	BuildCommit = "unknown"
	BuildDate   = "unknown"
	Variant     = "unknown"
	Platform    = ""
	Arch        = ""
)

type Info struct {
	Version     string `json:"version,omitempty"`
	Platform    string `json:"platform,omitempty"`
	Arch        string `json:"arch,omitempty"`
	Variant     string `json:"variant,omitempty"`
	BuildCommit string `json:"build_commit,omitempty"`
	BuildDate   string `json:"build_date,omitempty"`
}

func Current() Info {
	platform := strings.TrimSpace(Platform)
	if platform == "" {
		platform = runtime.GOOS
	}
	arch := strings.TrimSpace(Arch)
	if arch == "" {
		arch = normalizeArch(runtime.GOARCH)
	}
	return Info{
		Version: clean(Version, "dev"), Platform: platform, Arch: arch,
		Variant: clean(Variant, "unknown"), BuildCommit: clean(BuildCommit, "unknown"),
		BuildDate: clean(BuildDate, "unknown"),
	}
}

func normalizeArch(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "arm") {
		return "arm"
	}
	return strings.TrimSpace(v)
}
func clean(v, fallback string) string {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if v == "" {
		return fallback
	}
	return v
}

func (i Info) VersionText() string {
	return fmt.Sprintf("autofetch %s\ncommit %s\nbuilt %s\n%s/%s\n%s", i.Version, i.BuildCommit, i.BuildDate, i.Platform, i.Arch, i.Variant)
}
func (i Info) StartLogLines() []string {
	return []string{"autofetch client starting", "version=" + i.Version, "commit=" + i.BuildCommit, "build_date=" + i.BuildDate, "platform=" + i.Platform, "arch=" + i.Arch, "variant=" + i.Variant}
}
