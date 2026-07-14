package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLeaseRequestSerializesMetadata(t *testing.T) {
	b, err := json.Marshal(LeaseRequest{ClientID: "id", ClientMetadata: ClientMetadata{Version: "1.0.3", Platform: "linux", Arch: "arm64", Variant: "headless", BuildCommit: "abc", BuildDate: "2026-07-14T10:00:00Z"}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"client_id":"id"`, `"version":"1.0.3"`, `"platform":"linux"`, `"arch":"arm64"`, `"variant":"headless"`, `"build_commit":"abc"`, `"build_date":"2026-07-14T10:00:00Z"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
}
func TestLeaseRequestOmitsEmptyMetadata(t *testing.T) {
	b, _ := json.Marshal(LeaseRequest{ClientID: "id"})
	if strings.Contains(string(b), "version") {
		t.Fatalf("unexpected metadata: %s", b)
	}
}
