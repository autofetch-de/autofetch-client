package buildinfo

import (
	"strings"
	"testing"
)

func TestVersionText(t *testing.T) {
	i := Info{Version: "1.0.3", BuildCommit: "abc1234", BuildDate: "2026-07-14T10:00:00Z", Platform: "linux", Arch: "arm64", Variant: "headless", Language: "en"}
	got := i.VersionText()
	for _, want := range []string{"autofetch 1.0.3", "commit abc1234", "built 2026-07-14T10:00:00Z", "linux/arm64", "headless", "language en"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
