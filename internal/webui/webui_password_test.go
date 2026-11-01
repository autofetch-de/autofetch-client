package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/observe"
)

type webUIPasswordService struct {
	testService
	saved         string
	calls         int
	changes       int
	settingsCalls int
}

func (s *webUIPasswordService) SetWebUIPassword(password string) error {
	s.saved = password
	s.calls++
	return nil
}

func (s *webUIPasswordService) ChangeWebUIPassword(password string) error {
	s.saved = password
	s.changes++
	return nil
}

func (s *webUIPasswordService) UpdateLocalSettings(string, config.IRCConfig, bool, string) error {
	s.settingsCalls++
	return nil
}

func TestPasswordSetupFromNoPasswordLANActivatesImmediately(t *testing.T) {
	service := &webUIPasswordService{}
	server, err := NewWithOptions("192.168.1.50:23324", observe.NewState("", "", "test", 10), service, localization.New(localization.German), AccessOptions{AllowUnauthenticatedLAN: true})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, origin string, form url.Values, basic bool) *httptest.ResponseRecorder {
		var body string
		if form != nil {
			body = form.Encode()
		}
		req := httptest.NewRequest(method, "http://192.168.1.50:23324"+path, strings.NewReader(body))
		req.RemoteAddr = "192.168.1.17:3456"
		req.Host = "192.168.1.50:23324"
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if basic {
			req.SetBasicAuth("autofetch", "strong-bootstrap-password")
		}
		rec := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("GET", "/settings", "", nil, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "webui_password_repeat") {
		t.Fatalf("password setup not shown: %d %s", rec.Code, rec.Body.String())
	}
	form := url.Values{"webui_password": {"strong-bootstrap-password"}, "webui_password_repeat": {"strong-bootstrap-password"}}
	if rec := request("POST", "/settings/webui-password", "", form, false); rec.Code != 403 {
		t.Fatalf("missing Origin accepted: %d", rec.Code)
	}
	if rec := request("POST", "/settings/webui-password", "http://evil.example", form, false); rec.Code != 403 {
		t.Fatalf("cross-origin accepted: %d", rec.Code)
	}
	if service.calls != 0 {
		t.Fatal("unauthorized attempt called password storage")
	}
	invalid := url.Values{"webui_password": {"strong-bootstrap-password"}, "webui_password_repeat": {"different-password"}}
	if rec := request("POST", "/settings/webui-password", "http://192.168.1.50:23324", invalid, false); rec.Code != 200 || service.calls != 0 {
		t.Fatalf("mismatch changed password: %d", rec.Code)
	}
	if rec := request("POST", "/settings/webui-password", "http://192.168.1.50:23324", form, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("bootstrap failed: %d: %s", rec.Code, rec.Body.String())
	}
	if service.calls != 1 || service.saved != "strong-bootstrap-password" {
		t.Fatal("password not passed to storage correctly")
	}
	if rec := request("GET", "/settings", "", nil, false); rec.Code != 401 {
		t.Fatalf("UI remains unprotected: %d", rec.Code)
	}
	if rec := request("GET", "/settings", "", nil, true); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Passwortschutz") {
		t.Fatalf("login or status not correct: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("POST", "/settings/webui-password", "http://192.168.1.50:23324", form, false); rec.Code != 401 {
		t.Fatalf("repeated unauthenticated setup allowed: %d", rec.Code)
	}
	if rec := request("POST", "/settings/webui-password", "http://192.168.1.50:23324", form, true); rec.Code != 409 {
		t.Fatalf("second setup accepted: %d", rec.Code)
	}
}

func TestSettingsSingleSaveOptionalPasswordAndRotation(t *testing.T) {
	service := &webUIPasswordService{testService: testService{downloadDir: "/tmp/test-downloads"}}
	server, err := NewWithOptions("192.168.1.50:23324", observe.NewState("", "", "test", 10), service, localization.New(localization.German), AccessOptions{AllowUnauthenticatedLAN: true})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, origin, credential string, form url.Values) *httptest.ResponseRecorder {
		var body string
		if form != nil {
			body = form.Encode()
		}
		req := httptest.NewRequest(method, "http://192.168.1.50:23324"+path, strings.NewReader(body))
		req.RemoteAddr = "192.168.1.17:12345"
		req.Host = "192.168.1.50:23324"
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if credential != "" {
			req.SetBasicAuth("autofetch", credential)
		}
		rec := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(rec, req)
		return rec
	}
	origin := "http://192.168.1.50:23324"
	// Password fields are optional, the only Save button is below them,
	// and the login username is visibly called out.
	page := request("GET", "/settings", "", "", nil)
	if page.Code != http.StatusOK {
		t.Fatal(page.Code)
	}
	body := page.Body.String()
	if strings.Count(body, `<form `) != 1 || strings.Count(body, `type="submit"`) != 1 || strings.Contains(body, "Passwort aktivieren") || strings.Contains(body, `required`) {
		t.Fatalf("settings is not a single optional form: %s", body)
	}
	if strings.Index(body, `name="webui_password_repeat"`) > strings.Index(body, `<div class="actions">`) {
		t.Fatal("actions are above password fields")
	}
	if !strings.Contains(body, `<code>autofetch</code>`) || !strings.Contains(body, "Leer lassen") || !strings.Contains(body, `class="login-info"`) {
		t.Fatal("username or optional password guidance missing")
	}
	// Saving without password must never turn on authentication.
	blank := url.Values{"download_dir": {"/tmp/test-downloads"}}
	if rec := request("POST", "/settings", origin, "", blank); rec.Code != http.StatusSeeOther || service.calls != 0 || service.changes != 0 {
		t.Fatalf("blank save changed password: %d", rec.Code)
	}
	if service.settingsCalls != 0 {
		t.Fatal("unchanged settings unnecessarily restarted the worker")
	}
	if rec := request("GET", "/settings", "", "", nil); rec.Code != http.StatusOK {
		t.Fatal("blank save enabled auth")
	}
	form := url.Values{"download_dir": {"/tmp/test-downloads"}, "webui_password": {"first-strong-password"}, "webui_password_repeat": {"first-strong-password"}}
	if rec := request("POST", "/settings", "", "", form); rec.Code != http.StatusForbidden || service.calls != 0 {
		t.Fatalf("missing origin accepted: %d", rec.Code)
	}
	if rec := request("POST", "/settings", "http://evil.example", "", form); rec.Code != http.StatusForbidden || service.calls != 0 {
		t.Fatalf("wrong origin accepted: %d", rec.Code)
	}
	bad := url.Values{"download_dir": {"/tmp/test-downloads"}, "webui_password": {"first-strong-password"}, "webui_password_repeat": {"wrong-password"}}
	if rec := request("POST", "/settings", origin, "", bad); rec.Code != http.StatusOK || service.calls != 0 {
		t.Fatalf("mismatch accepted: %d", rec.Code)
	}
	if rec := request("POST", "/settings", origin, "", form); rec.Code != http.StatusSeeOther || service.calls != 1 {
		t.Fatalf("initial save failed: %d", rec.Code)
	}
	if rec := request("GET", "/settings", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatal("password did not take effect")
	}
	if rec := request("GET", "/settings", "", "first-strong-password", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Leer lassen") {
		t.Fatal("authenticated settings missing optional change")
	}
	if rec := request("POST", "/settings", origin, "first-strong-password", blank); rec.Code != http.StatusSeeOther || service.changes != 0 {
		t.Fatalf("blank field changed existing password: %d", rec.Code)
	}
	change := url.Values{"download_dir": {"/tmp/test-downloads"}, "webui_password": {"second-strong-password"}, "webui_password_repeat": {"second-strong-password"}}
	if rec := request("POST", "/settings", origin, "first-strong-password", change); rec.Code != http.StatusSeeOther || service.changes != 1 || service.saved != "second-strong-password" {
		t.Fatalf("password rotation failed: %d", rec.Code)
	}
	if rec := request("GET", "/settings", "", "first-strong-password", nil); rec.Code != http.StatusUnauthorized {
		t.Fatal("old password still accepted")
	}
	if rec := request("GET", "/settings", "", "second-strong-password", nil); rec.Code != http.StatusOK {
		t.Fatal("new password not accepted")
	}
}
