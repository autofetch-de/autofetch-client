package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/archive"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/observe"
	"github.com/autofetch-de/autofetch-client/internal/worker"
)

type testService struct {
	downloadDir string
	irc         config.IRCConfig
	testErr     error
}

func (s *testService) Start() error                                                     { return nil }
func (s *testService) Stop() error                                                      { return nil }
func (s *testService) StartRePair() error                                               { return nil }
func (s *testService) TestConnection(context.Context) error                             { return s.testErr }
func (s *testService) DownloadDir() string                                              { return s.downloadDir }
func (s *testService) IRCConfig() config.IRCConfig                                      { return s.irc }
func (s *testService) UpdateLocalSettings(string, config.IRCConfig, bool, string) error { return nil }

func TestIndexUsesSelectedLanguage(t *testing.T) {
	tests := []struct {
		name       string
		language   string
		wantLang   string
		wantText   string
		rejectText string
	}{
		{name: "German", language: localization.German, wantLang: `lang="de"`, wantText: "Einstellungen", rejectText: "Settings"},
		{name: "English", language: localization.English, wantLang: `lang="en"`, wantText: "Settings", rejectText: "Einstellungen"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := observe.NewState("https://autofetch.de", "", "test", 10)
			state.StartPairing("ABCD-EFGH", "https://autofetch.de/clients/new", time.Now().Add(10*time.Minute))
			server := New("127.0.0.1:0", state, &testService{}, localization.New(tt.language))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected status: %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tt.wantLang) {
				t.Fatalf("missing HTML language %q", tt.wantLang)
			}
			if !strings.Contains(body, tt.wantText) {
				t.Fatalf("missing localized text %q", tt.wantText)
			}
			if strings.Contains(body, tt.rejectText) {
				t.Fatalf("unexpected text from other language %q", tt.rejectText)
			}
			if !strings.Contains(body, `id="archive-progress"`) || !strings.Contains(body, `fetch('/api/archives'`) || !strings.Contains(body, "120000") || !strings.Contains(body, "archive-completed") {
				t.Fatalf("archive progress is missing from the local UI")
			}
		})
	}
}

func TestStatusLocalizesStateButKeepsLogsTechnical(t *testing.T) {
	state := observe.NewState("https://autofetch.de", "", "test", 10)
	state.StartPairing("ABCD-EFGH", "https://autofetch.de/clients/new", time.Now().Add(10*time.Minute))
	state.DownloadFinished("episode.mp4", observe.StatusDownloadCompleted)
	state.Error(errors.New("reverse_dcc_port_forward_required: port=36080 advertised_ip=203.0.113.10"))
	state.AppendLog("download failed code=reverse_dcc_port_forward_required port=36080")
	server := New("127.0.0.1:0", state, &testService{}, localization.New(localization.English))

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var snapshot observe.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if snapshot.PairingStatus != "Waiting for approval" {
		t.Fatalf("unexpected pairing status: %q", snapshot.PairingStatus)
	}
	if snapshot.LastDownloadStatus != "completed" {
		t.Fatalf("unexpected download status: %q", snapshot.LastDownloadStatus)
	}
	if !strings.Contains(snapshot.LastError, "36080") || strings.Contains(snapshot.LastError, "reverse_dcc") {
		t.Fatalf("unexpected visible error: %q", snapshot.LastError)
	}
	if len(snapshot.Logs) != 1 || !strings.Contains(snapshot.Logs[0], "reverse_dcc_port_forward_required") {
		t.Fatalf("technical log was unexpectedly localized: %#v", snapshot.Logs)
	}
}

func TestActionErrorIsLocalized(t *testing.T) {
	state := observe.NewState("https://autofetch.de", "client", "test", 10)
	service := &testService{testErr: errors.New("connection refused")}
	server := New("127.0.0.1:0", state, service, localization.New(localization.German))

	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	rec := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") || !strings.Contains(rec.Body.String(), "Server") {
		t.Fatalf("unexpected localized error body: %s", rec.Body.String())
	}
}

type historyTestService struct{ testService }

func (s *historyTestService) DownloadHistory(context.Context, int) ([]worker.DownloadHistoryItem, error) {
	return []worker.DownloadHistoryItem{{JobID: "job-1", Filename: "test.rar", Phase: "transferred", LastResult: "SUCCESS"}}, nil
}
func TestHistoryIsReadOnlyAndLoopbackOnly(t *testing.T) {
	srv := New("127.0.0.1:0", observe.NewState("https://autofetch.de", "client", "test", 20), &historyTestService{}, localization.New(localization.German))
	for _, tc := range []struct {
		method, remote string
		status         int
	}{
		{http.MethodGet, "127.0.0.1:1234", http.StatusOK},
		{http.MethodGet, "203.0.113.4:1234", http.StatusForbidden},
		{http.MethodPost, "127.0.0.1:1234", http.StatusMethodNotAllowed},
	} {
		req := httptest.NewRequest(tc.method, "/api/history", nil)
		req.RemoteAddr = tc.remote
		rec := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Errorf("%s remote=%s status=%d want=%d", tc.method, tc.remote, rec.Code, tc.status)
		}
		if tc.status == http.StatusOK && (!strings.Contains(rec.Body.String(), "test.rar") || rec.Header().Get("Cache-Control") != "no-store") {
			t.Fatalf("history missing or cached: %s", rec.Body.String())
		}
	}
}

type archiveTestService struct{ testService }

func (s *archiveTestService) ArchiveHistory(context.Context, int) ([]archive.Item, error) {
	return []archive.Item{{JobID: "job-1", Source: "test.zip", Status: "completed"}}, nil
}
func TestArchiveHistoryLoopbackOnly(t *testing.T) {
	srv := New("127.0.0.1:0", observe.NewState("https://autofetch.de", "client", "test", 20), &archiveTestService{}, localization.New(localization.English))
	for _, tc := range []struct {
		method, remote string
		want           int
	}{
		{http.MethodGet, "127.0.0.1:1234", http.StatusOK},
		{http.MethodGet, "203.0.113.4:1234", http.StatusForbidden},
		{http.MethodPost, "127.0.0.1:1234", http.StatusMethodNotAllowed},
	} {
		req := httptest.NewRequest(tc.method, "/api/archives", nil)
		req.RemoteAddr = tc.remote
		rec := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("status=%d want=%d", rec.Code, tc.want)
		}
		if rec.Code == http.StatusOK && (!strings.Contains(rec.Body.String(), "test.zip") || rec.Header().Get("Cache-Control") != "no-store") {
			t.Fatalf("missing archive information: %s", rec.Body.String())
		}
	}
}

func TestLocalHistoryAndArchiveControlsAreVisibleAndEscaped(t *testing.T) {
	for _, language := range []string{localization.German, localization.English} {
		srv := New("127.0.0.1:0", observe.NewState("https://autofetch.de", "client", "test", 20), &testService{}, localization.New(language))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{`id="download-records"`, `id="archive-records"`, `fetch('/api/history'`, `fetch('/api/archives/retry'`, `X-Autofetch-Local-Action`, `textContent=title`, `textContent=detail`, `setInterval(refreshDownloads,12000)`} {
			if !strings.Contains(body, want) {
				t.Errorf("language=%s missing %q", language, want)
			}
		}
		if strings.Contains(body, "innerHTML=") {
			t.Fatal("history should never place untrusted filenames into innerHTML")
		}
		if language == localization.German && !strings.Contains(body, "Lokale Downloadhistorie") {
			t.Fatal("missing German heading")
		}
		if language == localization.English && !strings.Contains(body, "Local download history") {
			t.Fatal("missing English heading")
		}
	}
}

type retryTestService struct {
	testService
	calledWith string
}

func (s *retryTestService) RetryArchive(_ context.Context, jobID string) error {
	s.calledWith = jobID
	return nil
}

func TestArchiveRetryRequiresLoopbackAndExplicitAction(t *testing.T) {
	svc := &retryTestService{}
	srv := New("127.0.0.1:23324", observe.NewState("https://autofetch.de", "client", "test", 20), svc, localization.New(localization.English))
	for _, tc := range []struct {
		name, remote, host, origin, action string
		want                               int
	}{
		{"ok", "127.0.0.1:1000", "127.0.0.1:23324", "http://127.0.0.1:23324", "retry", http.StatusOK},
		{"remote client", "192.0.2.10:1000", "127.0.0.1:23324", "http://127.0.0.1:23324", "retry", http.StatusForbidden},
		{"missing header", "127.0.0.1:1000", "127.0.0.1:23324", "http://127.0.0.1:23324", "", http.StatusForbidden},
		{"host rebinding", "127.0.0.1:1000", "attacker.test:23324", "", "retry", http.StatusForbidden},
		{"cross origin", "127.0.0.1:1000", "127.0.0.1:23324", "http://attacker.test", "retry", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/archives/retry", strings.NewReader(`{"job_id":"job-123"}`))
			req.RemoteAddr, req.Host = tc.remote, tc.host
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Autofetch-Local-Action", tc.action)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			srv.http.Handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status=%d want=%d", rec.Code, tc.want)
			}
		})
	}
	if svc.calledWith != "job-123" {
		t.Fatalf("unexpected retry job: %q", svc.calledWith)
	}
}

type archiveSettingsTestService struct {
	testService
	options config.ArchiveOptions
	updated int
}

func (s *archiveSettingsTestService) ArchiveOptions() config.ArchiveOptions { return s.options }
func (s *archiveSettingsTestService) UpdateArchiveOptions(o config.ArchiveOptions) error {
	s.updated++
	s.options = o
	return nil
}
func TestArchiveSettingsOnlyAtCentralPortal(t *testing.T) {
	svc := &archiveSettingsTestService{options: config.ArchiveOptions{Enabled: true, Layout: "same", DeleteOriginal: true}}
	srv := New("127.0.0.1:23324", observe.NewState("https://autofetch.de", "client", "test", 20), svc, localization.New(localization.German))
	page := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if page.Code != http.StatusOK || strings.Contains(page.Body.String(), `id="archive-delete"`) {
		t.Fatalf("client must not expose archive policy")
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/archive-settings", nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("unexpected response %d", rec.Code)
	}
	if svc.updated != 0 {
		t.Fatal("client modified server settings")
	}
}
