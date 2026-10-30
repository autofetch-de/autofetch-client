package webui

import (
	"context"
	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/observe"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type historyHiderTestService struct {
	testService
	jobID string
	all   bool
	calls int
}

func (s *historyHiderTestService) HideDownloadHistory(_ context.Context, jobID string, all bool) error {
	s.jobID = jobID
	s.all = all
	s.calls++
	return nil
}

func TestHistoryHideAuthorizedLocalActionOnly(t *testing.T) {
	svc := &historyHiderTestService{}
	srv := New("127.0.0.1:23324", observe.NewState("https://autofetch.de", "client", "test", 20), svc, localization.New(localization.English))
	for _, tc := range []struct {
		name, remote, host, origin, action, body string
		want                                     int
	}{
		{"single", "127.0.0.1:1234", "127.0.0.1:23324", "http://127.0.0.1:23324", "history-hide", `{"job_id":"test-id"}`, http.StatusOK},
		{"all", "127.0.0.1:1234", "127.0.0.1:23324", "http://127.0.0.1:23324", "history-hide", `{"all":true}`, http.StatusOK},
		{"remote", "192.0.2.1:1234", "127.0.0.1:23324", "", "history-hide", `{"all":true}`, http.StatusForbidden},
		{"missing header", "127.0.0.1:1234", "127.0.0.1:23324", "", "", `{"all":true}`, http.StatusForbidden},
		{"rebind", "127.0.0.1:1234", "bad.example:23324", "", "history-hide", `{"all":true}`, http.StatusForbidden},
		{"cross origin", "127.0.0.1:1234", "127.0.0.1:23324", "http://bad.example", "history-hide", `{"all":true}`, http.StatusForbidden},
		{"ambiguous", "127.0.0.1:1234", "127.0.0.1:23324", "", "history-hide", `{"all":true,"job_id":"some"}`, http.StatusBadRequest},
		{"empty", "127.0.0.1:1234", "127.0.0.1:23324", "", "history-hide", `{}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/history/hide", strings.NewReader(tc.body))
			req.RemoteAddr, req.Host = tc.remote, tc.host
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Autofetch-Local-Action", tc.action)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			srv.http.Handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
	if svc.calls != 2 || svc.jobID != "" || !svc.all {
		t.Fatalf("unexpected modifications: %+v", svc)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/history/hide", nil)
	req.RemoteAddr = "127.0.0.1:1000"
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET accepted: %d", rec.Code)
	}
}

func TestHistoryAndLogsAreCollapsedAndOfferDeleteActions(t *testing.T) {
	for _, language := range []string{localization.German, localization.English} {
		srv := New("127.0.0.1:23324", observe.NewState("https://autofetch.de", "client", "test", 20), &historyHiderTestService{}, localization.New(language))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(rec, req)
		body := rec.Body.String()
		for _, want := range []string{`<details class="card collapsible" id="history-panel">`, `<details class="card collapsible" id="archive-panel">`, `<details class="card collapsible" id="log-panel">`, `fetch('/api/history/hide'`, `id="history-clear"`, `textContent=text.historyRemove`, `textContent=title`} {
			if !strings.Contains(body, want) {
				t.Errorf("language=%s missing %q", language, want)
			}
		}
		if strings.Contains(body, "innerHTML=") {
			t.Fatal("untrusted filename interpolated as html")
		}
	}
}
