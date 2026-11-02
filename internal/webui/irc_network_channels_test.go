package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/config"
)

// The network owns one nick; channel selections are job-authored and display-only.
func TestIRCSettingsKeepServerChannelsAndDoNotOfferChannelEditor(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	svc.irc.Networks[0].Channels = []string{"#moviegods", "#mg-chat"}
	svc.irc.RegistrationEmail = "legacy@example.org"
	page := httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1:23324/irc/setup?network=irc.example.net&channel=%23other", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, page)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, ch := range []string{"#moviegods", "#mg-chat"} {
		if !strings.Contains(body, ch) {
			t.Errorf("server channel %q missing from setup", ch)
		}
	}
	if strings.Contains(body, `name="channel"`) {
		t.Fatal("settings must not let users edit server-supplied job channels")
	}
	if !strings.Contains(body, `name="registration_email"`) || !strings.Contains(body, `action="/irc/setup/register"`) {
		t.Fatal("explicit NickServ registration still needs an email input")
	}
	if !strings.Contains(body, "#other") {
		t.Fatal("failed job channel context not shown")
	}
	// Even a forged legacy channel field must not replace the observed list.
	values := url.Values{
		"host": {"irc.example.net"}, "name": {"Example"},
		"nick": {"myNick"}, "username": {"myNick"},
		"tls": {"on"}, "port": {"6697"},
		"channel": {"#injected"},
	}
	save := postRegistration(t, srv, "/irc/setup", "http://127.0.0.1:23324", values)
	if save.Code != http.StatusSeeOther {
		t.Fatalf("POST status=%d: %s", save.Code, save.Body.String())
	}
	channels := svc.irc.Networks[0].Channels
	if len(channels) != 2 || channels[0] != "#moviegods" || channels[1] != "#mg-chat" {
		t.Fatalf("network channels modified by UI save: %v", channels)
	}
	if svc.irc.RegistrationEmail != "legacy@example.org" {
		t.Fatalf("legacy registration email cleared by settings save: %q", svc.irc.RegistrationEmail)
	}
}

func TestIRCNetworkAuthIsNotPerChannel(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	svc.irc.Networks[0].Channels = []string{"#one", "#two"}
	svc.irc.Networks[0].NickServ = config.IRCNickServ{Enabled: true, Password: "registered-password"}
	form := url.Values{
		"host": {"irc.example.net"}, "nick": {"myNick"},
		"nickserv_enabled": {"on"}, "tls": {"on"}, "port": {"6697"},
	}
	if rec := postRegistration(t, srv, "/irc/setup", "http://127.0.0.1:23324", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("save failed: %d", rec.Code)
	}
	n := svc.irc.Networks[0]
	if !n.NickServ.Enabled || n.NickServ.Password != "registered-password" || len(n.Channels) != 2 {
		t.Fatalf("network identity or its observed channels were changed: %+v", n)
	}
}

func TestRegistrationEmailPrefilledOnlyFromSelectedNetwork(t *testing.T) {
	srv, svc := testManualRegistrationUI(t)
	svc.irc.RegistrationEmail = "legacy@example.org"
	svc.irc.Networks[0].RegistrationEmail = "selected@example.org"
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:23324/irc/setup?network=irc.example.net", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d", rec.Code)
	}
	page := rec.Body.String()
	if !strings.Contains(page, `id="registration-email" type="email" name="registration_email" value="selected@example.org"`) {
		t.Fatal("network registration email not prefilled")
	}
	if strings.Contains(page, `value="legacy@example.org"`) {
		t.Fatal("global legacy email must not be used for registration form")
	}
}
