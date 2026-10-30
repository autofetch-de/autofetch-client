package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPhase1CapabilitiesIncludeIRCCommand(t *testing.T) {
	got := CurrentProtocolInfo("2fe7f5e0-f2a3-4bda-893a-65b04c367201")
	if !reflect.DeepEqual(got.ProtocolVersions, []int{1, 2, 3}) {
		t.Fatalf("versions: %v", got.ProtocolVersions)
	}
	if !reflect.DeepEqual(got.Capabilities, []string{"http_download", "xdcc_pack", "irc_command", "irc_tls_policy", "job_pause", "archive_extract"}) {
		t.Fatalf("capabilities: %v", got.Capabilities)
	}
}

func TestPairingAndLeaseSendSameProtocolWithoutChangingLegacyFields(t *testing.T) {
	const instanceID = "2fe7f5e0-f2a3-4bda-893a-65b04c367201"
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		seen = append(seen, body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/client/v1/register/start":
			if r.Header.Get("Authorization") != "" {
				t.Errorf("pairing should be anonymous")
			}
			_, _ = w.Write([]byte(`{"pairing_id":"pair-1","pairing_code":"123456","poll_after_seconds":3}`))
		case "/api/client/v1/jobs/lease":
			if r.Header.Get("Authorization") != "Client client-1:secret" {
				t.Errorf("lease authorization changed")
			}
			_, _ = w.Write([]byte(`{"job":null,"retry_after_seconds":60}`))
		default:
			t.Errorf("unexpected URL: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	client := New(srv.URL, "client-1", "secret")
	client.Metadata = ClientMetadata{Version: "1.1.0", Platform: "linux", Arch: "amd64"}
	client.Protocol = CurrentProtocolInfo(instanceID)
	pairClient := New(srv.URL, "", "")
	_, err := pairClient.RegisterStart(context.Background(), RegisterStartRequest{ClientName: "test", Platform: "linux", ClientProtocolInfo: client.Protocol})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.LeaseJob(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("request count: %d", len(seen))
	}
	for _, body := range seen {
		if body["instance_id"] != instanceID {
			t.Fatalf("missing identity: %v", body)
		}
		if !reflect.DeepEqual(body["protocol_versions"], []any{float64(1), float64(2), float64(3)}) {
			t.Fatalf("versions: %v", body)
		}
		if !reflect.DeepEqual(body["capabilities"], []any{"http_download", "xdcc_pack", "irc_command", "irc_tls_policy", "job_pause", "archive_extract"}) {
			t.Fatalf("caps: %v", body)
		}
		if _, ok := body["client_protocol_info"]; ok {
			t.Fatal("protocol was incorrectly nested")
		}
	}
	if seen[1]["client_id"] != "client-1" || seen[1]["platform"] != "linux" {
		t.Fatalf("legacy fields changed: %v", seen[1])
	}
}
