package app

import (
	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"testing"
)

func TestLocalArchiveSettingsCannotOverrideServer(t *testing.T) {
	s := NewService(&config.Config{ExtractLayout: "separate"}, buildinfo.Info{}, nil, nil, nil)
	if err := s.UpdateArchiveOptions(config.ArchiveOptions{Enabled: true, Layout: "same", DeleteOriginal: true}); err == nil {
		t.Fatal("local mutation must be rejected")
	}
}
