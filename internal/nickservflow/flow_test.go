package nickservflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/download"
	"github.com/autofetch-de/autofetch-client/internal/nickservstate"
)

type fakeSettings struct {
	cfg   config.IRCConfig
	saved int
	err   error
}

func (f *fakeSettings) IRCConfig() config.IRCConfig { return f.cfg }
func (f *fakeSettings) DownloadDir() string         { return "/tmp/downloads" }
func (f *fakeSettings) UpdateLocalSettings(_ string, cfg config.IRCConfig, _ bool, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.cfg = cfg
	f.saved++
	return nil
}
func makeSettings() *fakeSettings {
	return &fakeSettings{cfg: config.IRCConfig{DefaultNick: "test1", Networks: []config.IRCNetwork{{Host: "irc.example.org", Port: 6697, TLS: true, Nick: "test1", Channels: []string{"#a", "#b"}, NickServ: config.IRCNickServ{RequiresRegistration: true}}}}}
}
func request(action Action) Request {
	return Request{Host: "irc.example.org", Nick: "test1", Action: action, Email: "user@example.org", RulesAcknowledged: true}
}

func TestExplicitRegisterSavesBeforeNetworkRequest(t *testing.T) {
	f := makeSettings()
	called := 0
	result, err := Run(context.Background(), f, request(Register), func(_ context.Context, r download.NickServRequest) (download.NickServResult, error) {
		called++
		if f.saved != 1 || f.cfg.Networks[0].NickServ.Password == "" || !f.cfg.Networks[0].NickServ.RegistrationPending {
			t.Fatal("credentials must be persisted before network request")
		}
		if r.Password != f.cfg.Networks[0].NickServ.Password || r.Email != "user@example.org" || r.Action != download.NickServRegister {
			t.Fatal("wrong request")
		}
		return download.NickServResult{Pending: true}, nil
	})
	if err != nil || !result.Pending || called != 1 {
		t.Fatalf("result=%+v err=%v called=%d", result, err, called)
	}
	if len(f.cfg.Networks[0].Channels) != 2 {
		t.Fatal("channels must remain server managed")
	}
}
func TestRetryReusesStoredPasswordAndDoesNotOverwriteOnFailure(t *testing.T) {
	f := makeSettings()
	f.cfg.Networks[0].RegistrationEmail = "user@example.org"
	f.cfg.Networks[0].NickServ.Password = "saved"
	f.cfg.Networks[0].NickServ.RegistrationPending = true
	_, err := Run(context.Background(), f, request(Retry), func(_ context.Context, r download.NickServRequest) (download.NickServResult, error) {
		if r.Password != "saved" || r.Action != download.NickServRegister {
			t.Fatal("retry must use saved credential")
		}
		return download.NickServResult{}, errors.New("NickServ rejected")
	})
	if err == nil || f.saved != 0 || f.cfg.Networks[0].NickServ.Password != "saved" {
		t.Fatalf("lost retry credentials err=%v cfg=%+v", err, f.cfg.Networks[0].NickServ)
	}
}
func TestVerifyAcknowledgedSavesLoginState(t *testing.T) {
	f := makeSettings()
	f.cfg.Networks[0].NickServ.Password = "saved"
	f.cfg.Networks[0].NickServ.RegistrationPending = true
	out, err := Run(context.Background(), f, request(Verify), func(_ context.Context, r download.NickServRequest) (download.NickServResult, error) {
		if r.Password != "saved" || r.Action != download.NickServVerify {
			t.Fatal("wrong verify credentials")
		}
		return download.NickServResult{Identified: true}, nil
	})
	n := f.cfg.Networks[0]
	if err != nil || !out.Identified || n.NickServ.RegistrationPending || !n.NickServ.Enabled || n.NickServ.Password != "saved" || n.NickServ.Command != "IDENTIFY" {
		t.Fatalf("verification failed out=%+v cfg=%+v err=%v", out, n.NickServ, err)
	}
}
func TestResetOnlyPendingState(t *testing.T) {
	f := makeSettings()
	f.cfg.Networks[0].NickServ.Password = "saved"
	f.cfg.Networks[0].NickServ.RegistrationPending = true
	out, err := Run(context.Background(), f, request(Reset), nil)
	n := f.cfg.Networks[0]
	if err != nil || !out.Reset || n.Nick != "test1" || n.NickServ.Password != "" || n.NickServ.RegistrationPending || !n.NickServ.RequiresRegistration || len(n.Channels) != 2 {
		t.Fatalf("reset failed out=%+v n=%+v err=%v", out, n, err)
	}
}
func TestRegistrationGuardAndPersistenceFailure(t *testing.T) {
	f := makeSettings()
	r := request(Register)
	r.RulesAcknowledged = false
	if _, err := Run(context.Background(), f, r, nil); err == nil {
		t.Fatal("registration must require network rules acknowledgment")
	}
	r.RulesAcknowledged = true
	r.Email = "not-an-email"
	if _, err := Run(context.Background(), f, r, nil); err == nil {
		t.Fatal("invalid email accepted")
	}
	r.Email = "user@example.org"
	f.err = errors.New("disk read only")
	called := false
	_, err := Run(context.Background(), f, r, func(context.Context, download.NickServRequest) (download.NickServResult, error) {
		called = true
		return download.NickServResult{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "disk read only") || called || f.cfg.Networks[0].NickServ.Password != "" {
		t.Fatalf("must not register before persist err=%v called=%v", err, called)
	}
}
func TestChangedNickCannotModifyAnotherAccount(t *testing.T) {
	f := makeSettings()
	r := request(Reset)
	r.Nick = "other"
	if _, err := Run(context.Background(), f, r, nil); err == nil || f.saved > 0 {
		t.Fatal("mismatched nick should be rejected")
	}
}

func TestRegistrationEmailBelongsToSelectedNetwork(t *testing.T) {
	f := makeSettings()
	f.cfg.Networks = append(f.cfg.Networks, config.IRCNetwork{Host: "irc.other.net", Port: 6697, TLS: true, Nick: "other", RegistrationEmail: "other@example.org", NickServ: config.IRCNickServ{Password: "other-password", Enabled: true}})
	r := request(Register)
	r.Email = "first@example.org"
	_, err := Run(context.Background(), f, r, func(_ context.Context, actual download.NickServRequest) (download.NickServResult, error) {
		if f.cfg.Networks[0].RegistrationEmail != r.Email || f.cfg.Networks[1].RegistrationEmail != "other@example.org" || actual.Email != r.Email {
			t.Fatalf("wrong email persisted before REGISTER: %+v", f.cfg.Networks)
		}
		return download.NickServResult{Pending: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Action = Retry
	r.Email = "revised@example.org"
	_, err = Run(context.Background(), f, r, func(_ context.Context, actual download.NickServRequest) (download.NickServResult, error) {
		if f.cfg.Networks[0].RegistrationEmail != r.Email || f.cfg.Networks[1].RegistrationEmail != "other@example.org" || actual.Email != r.Email {
			t.Fatalf("retry touched other network or failed to persist revised address: %+v", f.cfg.Networks)
		}
		return download.NickServResult{}, errors.New("server rejected")
	})
	if err == nil || f.cfg.Networks[0].RegistrationEmail != "revised@example.org" || f.cfg.Networks[0].NickServ.Password == "" || f.cfg.Networks[1].NickServ.Password != "other-password" {
		t.Fatalf("retry lost network-specific settings: %+v err=%v", f.cfg.Networks, err)
	}
}

func TestRegisterPersistsExplicitConfirmationStatusAndPassword(t *testing.T) {
	f := makeSettings()
	result, err := Run(context.Background(), f, request(Register), func(_ context.Context, r download.NickServRequest) (download.NickServResult, error) {
		return download.NickServResult{Pending: true, Status: nickservstate.AwaitingConfirmation}, nil
	})
	n := f.cfg.Networks[0]
	if err != nil || result.Status != nickservstate.AwaitingConfirmation || !n.NickServ.RegistrationPending ||
		n.NickServ.RegistrationStatus != nickservstate.AwaitingConfirmation || len(n.NickServ.Password) != 18 {
		t.Fatalf("confirmation status or saved credentials lost: result=%+v err=%v state=%+v", result, err, n.NickServ)
	}
	if f.saved != 2 { // credentials first, confirmation status only after network reply
		t.Fatalf("expected persisted states before and after response, got %d", f.saved)
	}
}

func TestRetryPersistsRejectionWithoutDroppingSavedCredentials(t *testing.T) {
	f := makeSettings()
	f.cfg.Networks[0].NickServ.Password = "saved-password"
	f.cfg.Networks[0].NickServ.RegistrationPending = true
	f.cfg.Networks[0].NickServ.RegistrationStatus = nickservstate.AwaitingConfirmation
	f.cfg.Networks[0].RegistrationEmail = "user@example.org"
	out, err := Run(context.Background(), f, request(Retry), func(_ context.Context, r download.NickServRequest) (download.NickServResult, error) {
		return download.NickServResult{Status: nickservstate.Failed}, errors.New("NickServ rejected registration")
	})
	n := f.cfg.Networks[0]
	if err == nil || out.Status != nickservstate.Failed || n.NickServ.RegistrationStatus != nickservstate.Failed ||
		n.NickServ.Password != "saved-password" || !n.NickServ.RegistrationPending || f.saved != 1 {
		t.Fatalf("failed retry overwrote credentials: out=%+v err=%v status=%+v writes=%d", out, err, n.NickServ, f.saved)
	}
}

func TestVerifySuccessClearsPendingButRetainsPasswordAndSuccessfulStatus(t *testing.T) {
	f := makeSettings()
	f.cfg.Networks[0].NickServ.Password = "original-password"
	f.cfg.Networks[0].NickServ.RegistrationPending = true
	f.cfg.Networks[0].NickServ.RegistrationStatus = nickservstate.AwaitingConfirmation
	_, err := Run(context.Background(), f, request(Verify), func(_ context.Context, r download.NickServRequest) (download.NickServResult, error) {
		return download.NickServResult{Identified: true, Status: nickservstate.Identified}, nil
	})
	n := f.cfg.Networks[0]
	if err != nil || n.NickServ.RegistrationPending || !n.NickServ.Enabled || n.NickServ.Password != "original-password" || n.NickServ.RegistrationStatus != nickservstate.Identified {
		t.Fatalf("verified credentials or state wrong: %v %+v", err, n.NickServ)
	}
}
