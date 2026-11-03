package worker

import "testing"

func TestTransferSpeedForDisplay(t *testing.T) {
	for _, tc := range []struct {
		name           string
		speed          float64
		current, total int64
		verifying      bool
		want           float64
	}{
		{name: "still transferring", speed: 4_000_000, current: 100, total: 1000, want: 4_000_000},
		{name: "all bytes received", speed: 400_000, current: 1000, total: 1000, want: 0},
		{name: "overshot estimate", speed: 10_000, current: 1001, total: 1000, want: 0},
		{name: "verifying with unknown total", speed: 100_000, current: 1000, total: 0, verifying: true, want: 0},
		{name: "unknown total while downloading", speed: 100_000, current: 1000, total: 0, want: 100_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := transferSpeedForDisplay(tc.speed, tc.current, tc.total, tc.verifying)
			if got != tc.want {
				t.Fatalf("transferSpeedForDisplay() = %v; want %v", got, tc.want)
			}
		})
	}
}
