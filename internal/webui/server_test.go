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

	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/observe"
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
