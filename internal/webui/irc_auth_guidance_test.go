package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/observe"
)

func TestRegisteredNickErrorShowsSetupLinkAndInstructions(t *testing.T) {
	state := observe.NewState("https://autofetch.de", "", "test", 10)
	state.Error(&ircAuthErrorForTest{value: "irc_registered_nick_required: 477 nick #mg-chat; channel=#mg-chat; network=irc.abjects.net"})
	server := New("127.0.0.1:23324", state, &testService{}, localization.New(localization.German))
	rec := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"#mg-chat", "irc.abjects.net", "NickServ", "href=\"/irc/setup?channel=%23mg-chat&amp;network=irc.abjects.net\""} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q from auth guidance", want)
		}
	}
	if !strings.Contains(body, "Registriere deinen Nick") {
		t.Fatal("user-facing registration instructions missing")
	}
}

type ircAuthErrorForTest struct{ value string }

func (e *ircAuthErrorForTest) Error() string { return e.value }

func TestIRCSettingsExplainNickGeneration(t *testing.T) {
	srv := New("127.0.0.1:23324", observe.NewState("https://autofetch.de", "", "test", 10), &testService{}, localization.New(localization.German))
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/irc/setup?network=irc.abjects.net", nil))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "nicht automatisch registriert") || !strings.Contains(rec.Body.String(), "Abjects") {
		t.Fatal("settings must explain local nick generation and network-specific rules")
	}
}

type savedIRCSettingsService struct {
	testService
	updated config.IRCConfig
}

func (s *savedIRCSettingsService) UpdateLocalSettings(_ string, cfg config.IRCConfig, _ bool, _ string) error {
	s.updated = cfg
	return nil
}

func TestGenerateNickDoesNotKeepOldNickServPassword(t *testing.T) {
	svc := &savedIRCSettingsService{}
	svc.irc.Networks = []config.IRCNetwork{{Host: "irc.example.net", Port: 6697, TLS: true, Nick: "previousNick", NickServ: config.IRCNickServ{Enabled: true, Password: "previous-secret"}}}
	srv := New("127.0.0.1:23324", observe.NewState("https://autofetch.de", "", "test", 10), svc, localization.New(localization.German))
	form := url.Values{"host": {"irc.example.net"}, "nick": {"previousNick"}, "nickserv_enabled": {"on"}, "generate_nick": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/irc/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if len(svc.updated.Networks) != 1 || svc.updated.Networks[0].Nick == "previousNick" {
		t.Fatal("new nick not generated")
	}
	if svc.updated.Networks[0].NickServ.Password != "" {
		t.Fatal("old NickServ password incorrectly retained for newly generated nick")
	}
}

func TestStatusIncludesLiveIRCAuthInstructionsMetadata(t *testing.T) {
	state := observe.NewState("https://autofetch.de", "", "test", 10)
	state.Error(&ircAuthErrorForTest{value: "irc_registered_nick_required: 477 nick #mg-chat; channel=#mg-chat; network=irc.abjects.net"})
	srv := New("127.0.0.1:23324", state, &testService{}, localization.New(localization.German))
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	var status struct {
		LastError        string `json:"last_error"`
		IRCAuthRequired  bool   `json:"irc_auth_required"`
		IRCAuthNetwork   string `json:"irc_auth_network"`
		IRCAuthChannel   string `json:"irc_auth_channel"`
		IRCAuthSetupLink string `json:"irc_auth_setup_link"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.IRCAuthRequired || status.IRCAuthNetwork != "irc.abjects.net" || status.IRCAuthChannel != "#mg-chat" || status.IRCAuthSetupLink != "/irc/setup?channel=%23mg-chat&network=irc.abjects.net" {
		t.Fatalf("unexpected auth metadata: %+v", status)
	}
	if !strings.Contains(status.LastError, "NickServ") || strings.Contains(status.LastError, "477 nick") {
		t.Fatalf("unexpected error shown to user: %s", status.LastError)
	}
}
