package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/autofetch-de/autofetch-client/internal/localization"
)

// writePairingBanner prints a clearly separated terminal block on pairing
// creation. The code is intentionally written to the console rather than
// through log.Printf, so it does not become an ordinary timestamped log entry.
// This is plain text (no ANSI escapes), so it also works in systemd journals.
func writePairingBanner(out io.Writer, l *localization.Localizer, code, pairingURL string) {
	code = strings.TrimSpace(code)
	if code == "" {
		return
	}
	if l == nil {
		l = localization.New(localization.English)
	}
	rule := strings.Repeat("=", 70)
	title := strings.ToUpper(l.T("pairing.card_title"))
	instructions := l.T("pairing.enter_code")
	link := l.T("cli.open", map[string]any{"URL": pairingURL})

	// Compose the whole banner before writing to reduce interleaving with logs.
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s\n  AUTOFETCH - %s\n%s\n\n", rule, title, rule)
	fmt.Fprintf(&b, "  %s\n\n", instructions)
	fmt.Fprintf(&b, "          >>>  %s  <<<\n\n", code)
	fmt.Fprintf(&b, "  %s\n\n%s\n\n", link, rule)
	_, _ = io.WriteString(out, b.String())
}
