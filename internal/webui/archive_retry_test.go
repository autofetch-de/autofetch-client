package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/observe"
)

type archiveRetryService struct {
	testService
	last string
}

func (s *archiveRetryService) RetryArchive(ctx context.Context, jobID string) error {
	s.last = jobID
	return nil
}

func TestArchiveRetryLocalOnlyAndExplicit(t *testing.T) {
	svc := &archiveRetryService{}
	server := New("127.0.0.1:23324", observe.NewState("https://autofetch.de", "", "test", 10), svc, localization.New(localization.German))
	cases := []struct {
		name, remote, origin, header, contentType string
		expected                                  int
	}{
		{"valid-local", "127.0.0.1:49252", "", "retry", "application/json", 200},
		{"remote-peer", "203.0.113.8:49252", "", "retry", "application/json", 403},
		{"missing-header", "127.0.0.1:49252", "", "", "application/json", 403},
		{"foreign-origin", "127.0.0.1:49252", "https://evil.example", "retry", "application/json", 403},
		{"same-origin", "127.0.0.1:49252", "http://127.0.0.1:23324", "retry", "application/json", 200},
		{"non-json", "127.0.0.1:49252", "", "retry", "text/plain", 415},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc.last = ""
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:23324/api/archives/retry", strings.NewReader(`{"job_id":"job-123"}`))
			req.RemoteAddr = tc.remote
			req.Header.Set("X-Autofetch-Local-Action", tc.header)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(w, req)
			if w.Code != tc.expected {
				t.Fatalf("status %d, want %d", w.Code, tc.expected)
			}
			if tc.expected == 200 && svc.last != "job-123" {
				t.Fatal("retry not sent")
			}
			if tc.expected != 200 && svc.last != "" {
				t.Fatal("unauthorized retry invoked")
			}
		})
	}
}
