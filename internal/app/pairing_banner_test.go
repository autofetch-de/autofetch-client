package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/localization"
)

func TestWritePairingBannerVisibleAndLocalized(t *testing.T) {
	tests := []struct {
		language     string
		title        string
		instructions string
	}{
		{localization.German, "AUTOFETCH - CLIENT KOPPELN", "Diesen Code auf der Pairing-Seite eingeben:"},
		{localization.English, "AUTOFETCH - PAIR CLIENT", "Enter this code on the pairing page:"},
	}
	for _, tc := range tests {
		t.Run(tc.language, func(t *testing.T) {
			var out bytes.Buffer
			writePairingBanner(&out, localization.New(tc.language), "ABCD-1234", "https://autofetch.de/clients/new")
			got := out.String()
			if !strings.Contains(got, tc.title) || !strings.Contains(got, tc.instructions) {
				t.Fatalf("banner is not localized: %q", got)
			}
			if !strings.Contains(got, ">>>  ABCD-1234  <<<") {
				t.Fatalf("pairing code not highlighted: %q", got)
			}
			if !strings.Contains(got, "https://autofetch.de/clients/new") {
				t.Fatalf("pairing URL missing: %q", got)
			}
			if strings.Count(got, strings.Repeat("=", 70)) != 3 {
				t.Fatalf("missing banner separators: %q", got)
			}
			if strings.Contains(got, "\x1b") {
				t.Fatalf("banner must be readable without terminal ANSI support: %q", got)
			}
		})
	}
}

func TestWritePairingBannerEmptyCodeIsNotPrinted(t *testing.T) {
	var out bytes.Buffer
	writePairingBanner(&out, nil, "  ", "https://autofetch.de/clients/new")
	if out.Len() != 0 {
		t.Fatalf("unexpected banner without pairing code: %q", out.String())
	}
}
