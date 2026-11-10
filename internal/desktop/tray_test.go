package desktop

import (
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/desktop/assets"
	"github.com/autofetch-de/autofetch-client/internal/observe"
)

func TestTrayIconForSnapshotAppearanceAndStatus(t *testing.T) {
	cases := []struct {
		name   string
		snap   observe.Snapshot
		status string
	}{
		{"not paired", observe.Snapshot{Running: true, Connected: true}, "pending"},
		{"connected", observe.Snapshot{Paired: true, Running: true, Connected: true}, "okay"},
		{"active download", observe.Snapshot{Paired: true, ActiveDownload: &observe.DownloadSnapshot{}}, "okay"},
		{"stopped", observe.Snapshot{Paired: true, LastError: "connection failed"}, "pending"},
		{"error", observe.Snapshot{Paired: true, Running: true, LastError: "connection failed"}, "fail"},
		{"waiting", observe.Snapshot{Paired: true, Running: true}, "pending"},
	}
	for _, tc := range cases {
		for _, light := range []bool{false, true} {
			t.Run(tc.name+"/light="+map[bool]string{true: "yes", false: "no"}[light], func(t *testing.T) {
				key, resource := trayIconForSnapshot(tc.snap, light)
				want := tc.status
				if light {
					want += "-light"
				}
				if key != want {
					t.Fatalf("icon key = %q, want %q", key, want)
				}
				if resource == nil || len(resource.Content()) == 0 {
					t.Fatalf("missing resource for %q", want)
				}
				if light && !strings.Contains(string(resource.Content()), `fill:#e6e6e6`) {
					t.Fatalf("light tray icon has no contrasting glyph: %s", resource.Name())
				}
			})
		}
	}
}

func TestLightTrayIconPreservesStatusMarkers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contents []byte
		marker   string
	}{
		{"connected", assets.TrayIconOkayLight.Content(), "#2bd000"},
		{"pending", assets.TrayIconPendingLight.Content(), "#ffd42a"},
		{"failure", assets.TrayIconFailLight.Content(), "#ff0000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(string(tc.contents), tc.marker) {
				t.Errorf("status marker %s was not preserved", tc.marker)
			}
		})
	}
}
