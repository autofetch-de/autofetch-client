package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/config"
)

func setupPage(t *testing.T, state config.IRCNickServ) string {
	t.Helper()
	s, svc := testManualRegistrationUI(t)
	svc.irc.Networks[0].NickServ = state
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/irc/setup?network=irc.example.net", nil)
	s.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET setup: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestIRCSetupNewNickHasObviousGenerateAndEmailSteps(t *testing.T) {
	html := setupPage(t, config.IRCNickServ{RequiresRegistration: true})
	for _, want := range []string{
		`id="network-nick"`, `name="generate_nick"`,
		`name="nick"`, `action="/irc/setup/register"`, `id="registration-email"`,
		`name="network_rules_ack"`, `name="host"`,
		`NickServ`, `type="email"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Index(html, `name="generate_nick"`) > strings.Index(html, `action="/irc/setup/register"`) {
		t.Fatal("nickname generation must precede the registration form")
	}
	if !strings.Contains(html, `<details><summary>`) || strings.Contains(html, `name="channel"`) {
		t.Fatal("advanced settings should be collapsed and channel editor removed")
	}
}

func TestIRCSetupPendingShowsVerifyBeforeTroubleshooting(t *testing.T) {
	html := setupPage(t, config.IRCNickServ{RegistrationPending: true, RequiresRegistration: true, Password: "stored-secret"})
	verify := strings.Index(html, `action="/irc/setup/verify"`)
	problems := strings.Index(html, `Registration problems?`)
	retry := strings.Index(html, `action="/irc/setup/register-retry"`)
	reset := strings.Index(html, `action="/irc/setup/reset"`)
	if verify < 0 || problems < verify || retry < problems || reset < problems {
		t.Fatalf("pending registration guidance order incorrect verify=%d problems=%d retry=%d reset=%d", verify, problems, retry, reset)
	}
	if strings.Contains(html, `action="/irc/setup/register"`) {
		t.Fatal("pending state must not show initial registration action")
	}
}

func TestIRCSetupPendingWithoutPasswordGuidesReset(t *testing.T) {
	html := setupPage(t, config.IRCNickServ{RegistrationPending: true, RequiresRegistration: true})
	if strings.Contains(html, `action="/irc/setup/verify"`) || strings.Contains(html, `action="/irc/setup/register-retry"`) {
		t.Fatal("pending registration without credentials must not offer impossible actions")
	}
	if !strings.Contains(html, `action="/irc/setup/reset"`) || !strings.Contains(html, `no saved credentials`) {
		t.Fatal("pending without password must explain recovery via reset")
	}
}

func TestIRCSetupStoredCredentialIsNotAskedToRegisterAgain(t *testing.T) {
	html := setupPage(t, config.IRCNickServ{Enabled: true, Password: "stored-secret"})
	if strings.Contains(html, `action="/irc/setup/register"`) || strings.Contains(html, `action="/irc/setup/register-retry"`) {
		t.Fatal("saved account must not be prompted to register again")
	}
	if !strings.Contains(html, `future downloads`) && !strings.Contains(html, `Future downloads`) {
		t.Fatal("completed state must explain automatic sign-in")
	}
}

// Footer navigation must look like the other action buttons rather than bare links.
func TestIRCSetupFooterActionsAreStyledButtons(t *testing.T) {
	html := setupPage(t, config.IRCNickServ{RequiresRegistration: true})
	for _, want := range []string{
		`<nav class="bottom-links"`,
		`class="button secondary" href="/settings"`,
		`class="button secondary" href="/"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("footer action missing button styling: %s", want)
		}
	}
}
