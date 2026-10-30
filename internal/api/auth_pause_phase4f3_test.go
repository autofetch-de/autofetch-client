package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticationDenialIsClassifiedOnConfigAndLease(t *testing.T) {
	for _, route := range []string{"/api/client/v1/config", "/api/client/v1/jobs/lease"} {
		for _, tc := range []struct {
			name    string
			status  int
			body    string
			denied  bool
			revoked bool
		}{
			{"invalid credentials", 403, `{"error":"forbidden","message":"invalid client credentials"}`, true, false},
			{"invalid credentials uppercase", 401, `{"error":"FORBIDDEN","message":"Invalid Client Credentials"}`, true, false},
			{"legacy revoked", 403, `{"error":"CLIENT_REVOKED_OR_MISSING"}`, false, true},
			{"unrelated forbidden", 403, `{"error":"forbidden","message":"source unavailable"}`, false, false},
			{"unrelated policy", 403, `{"error":"network_forbidden"}`, false, false},
			{"temporary", 503, `{"error":"forbidden","message":"invalid client credentials"}`, false, false},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer server.Close()
				cli := New(server.URL, "client-a", "secret")
				var err error
				if route == "/api/client/v1/config" {
					_, _, err = cli.GetRuntimeConfig(context.Background())
				} else {
					_, err = cli.LeaseJob(context.Background())
				}
				if IsAuthDenied(err) != tc.denied || IsRevoked(err) != tc.revoked {
					t.Fatalf("unexpected error classification: err=%v authDenied=%t revoked=%t", err, IsAuthDenied(err), IsRevoked(err))
				}
			})
		}
	}
}
