package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/download"
	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/observe"
)

type registrationService struct {
	testService
	writes int
}

func (s *registrationService) UpdateLocalSettings(_ string, c config.IRCConfig, _ bool, _ string) error {
	s.irc = c
	s.writes++
	return nil
}
func (s *registrationService) DownloadDir() string { return "/tmp/test-downloads" }

func testManualRegistrationUI(t *testing.T) (*Server, *registrationService) {
	t.Helper()
	svc := &registrationService{}
	svc.irc.Networks = []config.IRCNetwork{{Host: "irc.example.net", Port: 6697, TLS: true, Nick: "myNick", NickServ: config.IRCNickServ{}}}
	srv := New("127.0.0.1:23324", observe.NewState("https://autofetch.de", "client", "test", 10), svc, localization.New(localization.English))
	return srv, svc
}
func postRegistration(t *testing.T, srv *Server, path, origin string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:23324"+path, strings.NewReader(values.Encode()))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}
func TestManualRegistrationSameOriginAndExplicitNetworkApproval(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	calls := 0
	srv.nickServManual = func(context.Context, download.NickServRequest) (download.NickServResult, error) {
		calls++
		return download.NickServResult{Pending: true}, nil
	}
	form := url.Values{"host": {"irc.example.net"}, "registration_email": {"user@example.org"}, "network_rules_ack": {"yes"}}
	for _, tc := range []struct {
		origin string
		want   int
	}{
		{"", 403}, {"http://attacker.invalid", 403}, {"http://127.0.0.1:23324", 303},
	} {
		rec := postRegistration(t, srv, "/irc/setup/register", tc.origin, form)
		if rec.Code != tc.want {
			t.Errorf("origin=%q status=%d expected=%d: %s", tc.origin, rec.Code, tc.want, rec.Body.String())
		}
	}
	if calls != 1 || svc.writes != 1 {
		t.Fatalf("unexpected registration calls=%d writes=%d", calls, svc.writes)
	}
	n := svc.irc.Networks[0]
	if !n.NickServ.RegistrationPending || !n.NickServ.RequiresRegistration || n.NickServ.Enabled || len(n.NickServ.Password) != 18 || n.RegistrationEmail != "user@example.org" {
		t.Fatalf("pending credential state incorrect: pending=%t enabled=%t length=%d email=%q", n.NickServ.RegistrationPending, n.NickServ.Enabled, len(n.NickServ.Password), n.RegistrationEmail)
	}
	if rec := postRegistration(t, srv, "/irc/setup/register", "http://127.0.0.1:23324", form); rec.Code != 409 || calls != 1 {
		t.Fatal("same registration attempted twice")
	}
	// A pending nickname cannot be silently activated merely by saving its settings.
	if rec := postRegistration(t, srv, "/irc/setup/verify", "", url.Values{"host": {"irc.example.net"}}); rec.Code != 403 {
		t.Fatal("missing Origin accepted for verification")
	}
}
func TestManualRegistrationVerifyAndReset(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	svc.irc.Networks[0].NickServ = config.IRCNickServ{RegistrationPending: true, Password: "saved-password"}
	received := download.NickServRequest{}
	srv.nickServManual = func(_ context.Context, req download.NickServRequest) (download.NickServResult, error) {
		received = req
		return download.NickServResult{Identified: true}, nil
	}
	origin := "http://127.0.0.1:23324"
	rec := postRegistration(t, srv, "/irc/setup/verify", origin, url.Values{"host": {"irc.example.net"}, "confirmation_code": {"abc-123"}})
	if rec.Code != 303 || received.Action != download.NickServConfirm || received.Password != "saved-password" || received.Code != "abc-123" {
		t.Fatalf("verification failed: code=%d action=%s", rec.Code, received.Action)
	}
	if svc.irc.Networks[0].NickServ.RegistrationPending || svc.irc.Networks[0].NickServ.RequiresRegistration || !svc.irc.Networks[0].NickServ.Enabled {
		t.Fatal("verified identity was not activated")
	}
	svc.irc.Networks[0].NickServ.RegistrationPending = true
	rec = postRegistration(t, srv, "/irc/setup/reset", origin, url.Values{"host": {"irc.example.net"}})
	if rec.Code != 303 || svc.irc.Networks[0].NickServ.Password != "" || svc.irc.Networks[0].NickServ.RegistrationPending {
		t.Fatal("pending registration not reset")
	}
	// A stale browser tab may send reset again. This must be safe/idempotent.
	rec = postRegistration(t, srv, "/irc/setup/reset", origin, url.Values{"host": {"irc.example.net"}})
	if rec.Code != 303 || svc.irc.Networks[0].NickServ.Password != "" {
		t.Fatalf("repeated reset should redirect without rewriting credentials: %d", rec.Code)
	}
}
func TestGenerateNickRequiresNewRegistration(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	svc.irc.Networks[0].NickServ = config.IRCNickServ{Enabled: true, Password: "old-account-password"}
	f := url.Values{"host": {"irc.example.net"}, "nick": {"myNick"}, "generate_nick": {"1"}, "nickserv_enabled": {"on"}, "tls": {"on"}}
	rec := postRegistration(t, srv, "/irc/setup", "", f) // loopback IRC settings are legacy-allowed
	if rec.Code != 303 {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	n := svc.irc.Networks[0]
	if n.Nick == "myNick" || n.NickServ.RegistrationPending || !n.NickServ.RequiresRegistration || n.NickServ.Password != "" || n.NickServ.Enabled {
		t.Fatal("new nick incorrectly reused an authenticated account")
	}
}

func TestRegisterInvalidEmailDoesNotPersistCredentials(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	form := url.Values{"host": {"irc.example.net"}, "registration_email": {"not an email"}, "network_rules_ack": {"yes"}}
	rec := postRegistration(t, srv, "/irc/setup/register", "http://127.0.0.1:23324", form)
	if rec.Code != 400 || svc.writes != 0 || svc.irc.Networks[0].NickServ.Password != "" {
		t.Fatalf("invalid email changed credentials: status=%d writes=%d", rec.Code, svc.writes)
	}
}
func TestGeneratedNickDisplaysRegistrationButton(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	svc.irc.Networks[0].NickServ.RequiresRegistration = true
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:23324/irc/setup?network=irc.example.net", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `action="/irc/setup/register"`) || strings.Contains(rec.Body.String(), `action="/irc/setup/verify"`) {
		t.Fatalf("new nick cannot be registered: status=%d", rec.Code)
	}
}

func TestManualRegistrationErrorKeepsVerificationForm(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	srv.nickServManual = func(context.Context, download.NickServRequest) (download.NickServResult, error) {
		return download.NickServResult{}, errors.New("NickServ registration outcome unconfirmed; verify saved nick")
	}
	rec := postRegistration(t, srv, "/irc/setup/register", "http://127.0.0.1:23324", url.Values{
		"host": {"irc.example.net"}, "registration_email": {"user@example.org"}, "network_rules_ack": {"yes"},
	})
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "outcome unconfirmed") || !strings.Contains(rec.Body.String(), `action="/irc/setup/verify"`) {
		t.Fatalf("registration error should render verification UI, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.irc.Networks[0].NickServ.RegistrationPending || svc.irc.Networks[0].NickServ.Password == "" {
		t.Fatal("pending identity was cleared after an ambiguous response")
	}
}

func TestRegistrationExplicitRetryReusesSavedCredential(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	svc.irc.Networks[0].NickServ = config.IRCNickServ{RegistrationPending: true, RequiresRegistration: true, Password: "saved-pass"}
	calls := 0
	srv.nickServManual = func(_ context.Context, req download.NickServRequest) (download.NickServResult, error) {
		calls++
		if req.Action != download.NickServRegister || req.Nick != "myNick" || req.Password != "saved-pass" || req.Email != "user@example.org" {
			t.Errorf("retry altered identity or credentials: %+v", req)
		}
		return download.NickServResult{Pending: true}, nil
	}
	form := url.Values{"host": {"irc.example.net"}, "registration_email": {"user@example.org"}, "network_rules_ack": {"yes"}}
	if rec := postRegistration(t, srv, "/irc/setup/register-retry", "", form); rec.Code != 403 {
		t.Fatalf("missing Origin allowed retry: %d", rec.Code)
	}
	if rec := postRegistration(t, srv, "/irc/setup/register-retry", "http://evil.invalid", form); rec.Code != 403 {
		t.Fatalf("foreign Origin allowed retry: %d", rec.Code)
	}
	if rec := postRegistration(t, srv, "/irc/setup/register", "http://127.0.0.1:23324", form); rec.Code != 409 {
		t.Fatalf("original register allowed pending retry: %d", rec.Code)
	}
	withoutApproval := url.Values{"host": {"irc.example.net"}, "registration_email": {"user@example.org"}}
	if rec := postRegistration(t, srv, "/irc/setup/register-retry", "http://127.0.0.1:23324", withoutApproval); rec.Code != 400 {
		t.Fatalf("retry missing network acknowledgement accepted: %d", rec.Code)
	}
	if calls != 0 {
		t.Fatal("unauthorized retry reached IRC")
	}
	if rec := postRegistration(t, srv, "/irc/setup/register-retry", "http://127.0.0.1:23324", form); rec.Code != 303 {
		t.Fatalf("explicit retry failed: %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 || svc.irc.Networks[0].NickServ.Password != "saved-pass" || !svc.irc.Networks[0].NickServ.RegistrationPending {
		t.Fatal("retry did not preserve saved pending credential")
	}
	q := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:23324/irc/setup?network=irc.example.net", nil)
	page := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(page, q)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `action="/irc/setup/register-retry"`) {
		t.Fatal("pending setup did not offer explicit retry")
	}
}

func TestRegistrationStreamsProgressAndRedirectWithoutExposingCredentials(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	var savedPassword string
	srv.nickServManual = func(_ context.Context, req download.NickServRequest) (download.NickServResult, error) {
		savedPassword = req.Password
		req.OnProgress(download.NickServProgress{Kind: "connecting"})
		req.OnProgress(download.NickServProgress{Kind: "waiting", WaitSeconds: 30})
		req.OnProgress(download.NickServProgress{Kind: "notice", Message: "wait 30 seconds to register"})
		req.OnProgress(download.NickServProgress{Kind: "identified"})
		return download.NickServResult{Identified: true}, nil
	}
	form := url.Values{"host": {"irc.example.net"}, "registration_email": {"person@example.org"}, "network_rules_ack": {"yes"}}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:23324/irc/setup/register", strings.NewReader(form.Encode()))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://127.0.0.1:23324")
	req.Header.Set("Accept", "application/x-ndjson")
	result := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(result, req)
	if result.Code != http.StatusOK || !strings.Contains(result.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("stream response: %d headers=%v body=%s", result.Code, result.Header(), result.Body.String())
	}
	if savedPassword == "" || strings.Contains(result.Body.String(), savedPassword) || strings.Contains(result.Body.String(), "person@example.org") {
		t.Fatal("credentials not generated or leaked in progress stream")
	}
	var kinds []string
	var terminal map[string]any
	for _, row := range strings.Split(strings.TrimSpace(result.Body.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(row), &event); err != nil {
			t.Fatal(err)
		}
		kind, _ := event["kind"].(string)
		kinds = append(kinds, kind)
		terminal = event
		if kind == "waiting" && event["wait_seconds"] != float64(30) {
			t.Fatalf("missing wait duration: %#v", event)
		}
	}
	if got := strings.Join(kinds, ","); got != "starting,connecting,waiting,notice,identified,done" {
		t.Fatalf("wrong progress events: %s", got)
	}
	if !strings.HasPrefix(terminal["location"].(string), "/irc/setup?network=irc.example.net&notice=identified") || !svc.irc.Networks[0].NickServ.Enabled {
		t.Fatalf("registration did not finish: %#v", terminal)
	}
}

func TestRegistrationStreamErrorPreservesPendingCredentialsAndRejectsForeignOrigin(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	calls := 0
	srv.nickServManual = func(_ context.Context, req download.NickServRequest) (download.NickServResult, error) {
		calls++
		req.OnProgress(download.NickServProgress{Kind: "ready"})
		return download.NickServResult{}, errors.New("NickServ rejected registration")
	}
	form := url.Values{"host": {"irc.example.net"}, "registration_email": {"person@example.org"}, "network_rules_ack": {"yes"}}
	for _, origin := range []string{"http://evil.example", "http://127.0.0.1:23324"} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:23324/irc/setup/register", strings.NewReader(form.Encode()))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		req.Header.Set("Accept", "application/x-ndjson")
		result := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(result, req)
		if origin == "http://evil.example" {
			if result.Code != http.StatusForbidden || calls != 0 {
				t.Fatalf("foreign origin accepted: %d", result.Code)
			}
			continue
		}
		if result.Code != 200 || !strings.Contains(result.Body.String(), `"kind":"error"`) || !strings.Contains(result.Body.String(), "NickServ rejected registration") {
			t.Fatalf("stream error missing: %d %s", result.Code, result.Body.String())
		}
		if !svc.irc.Networks[0].NickServ.RegistrationPending || svc.irc.Networks[0].NickServ.Password == "" {
			t.Fatal("failure cleared pending credentials")
		}
	}
}

func TestNickServLiveProgressIsPresentInGuidedWebUI(t *testing.T) {
	srv, _ := testManualRegistrationUI(t)
	resp := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:23324/irc/setup?network=irc.example.net", nil))
	if resp.Code != http.StatusOK {
		t.Fatal(resp.Code)
	}
	html := resp.Body.String()
	for _, want := range []string{`id="nickserv-live"`, `id="nickserv-live-events"`, `application/x-ndjson`, `textContent`, `urlquery`, `data-waiting=`} {
		if want == `urlquery` {
			continue
		} // template function is evaluated, not embedded in HTML
		if !strings.Contains(html, want) {
			t.Fatalf("missing %s", want)
		}
	}
}

func TestRegistrationProgressArrivesBeforeNickServCompletes(t *testing.T) {
	srv, _ := testManualRegistrationUI(t)
	finish := make(chan struct{})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	srv.nickServManual = func(ctx context.Context, req download.NickServRequest) (download.NickServResult, error) {
		req.OnProgress(download.NickServProgress{Kind: "connected"})
		select {
		case <-ctx.Done():
			return download.NickServResult{}, ctx.Err()
		case <-finish:
			return download.NickServResult{Pending: true}, nil
		}
	}
	testServer := httptest.NewServer(srv.http.Handler)
	defer testServer.Close()
	form := url.Values{"host": {"irc.example.net"}, "registration_email": {"person@example.org"}, "network_rules_ack": {"yes"}}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, testServer.URL+"/irc/setup/register", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testServer.URL)
	req.Header.Set("Accept", "application/x-ndjson")
	response, err := testServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("stream rejected: %d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(first, `"kind":"starting"`) {
		t.Fatalf("first progress not flushed: %q %v", first, err)
	}
	second, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(second, `"kind":"connected"`) {
		t.Fatalf("second progress not flushed while exchange blocked: %q %v", second, err)
	}
	close(finish)
	last, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(last, `"kind":"done"`) {
		t.Fatalf("no completion after releasing exchange: %q %v", last, err)
	}
}
