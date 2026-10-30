package download

import (
	"errors"
	"net"
	"testing"
)

func TestDCCDestinationRejectsNonPublicAddresses(t *testing.T) {
	tests := []struct {
		ip     string
		reject bool
	}{
		{"192.168.2.254", true}, {"127.0.0.1", true}, {"10.0.1.2", true},
		{"172.20.10.1", true}, {"100.64.5.1", true}, {"169.254.1.2", true},
		{"198.51.100.1", true}, {"198.19.0.1", true}, {"0.0.0.0", true},
		{"::1", true}, {"fc00::123", true}, {"fe80::1", true}, {"2001:db8::1", true},
		{"8.8.8.8", false}, {"2606:4700:4700::1111", false},
	}
	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			err := validateDCCDestination(&dccOffer{IP: net.ParseIP(tc.ip), Port: 58109})
			if got := errors.Is(err, ErrDCCPrivateAddress); got != tc.reject {
				t.Fatalf("address=%s error=%v reject=%v", tc.ip, err, tc.reject)
			}
		})
	}
}
func TestReverseDCCNotTreatedAsPublicDial(t *testing.T) {
	if err := validateDCCDestination(&dccOffer{IP: net.ParseIP("127.0.0.1"), Port: 0, Token: "123"}); err != nil {
		t.Fatal(err)
	}
}
