// Package nickservflow coordinates an explicitly initiated NickServ action
// with durable network credentials. Both the desktop and WebUI use the same
// download.ManualNickServ IRC protocol implementation; no download job calls this.
package nickservflow

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/download"
	"github.com/autofetch-de/autofetch-client/internal/nickservstate"
)

type Settings interface {
	IRCConfig() config.IRCConfig
	DownloadDir() string
	UpdateLocalSettings(string, config.IRCConfig, bool, string) error
}

type Action string

const (
	Register            Action = "register"
	Retry               Action = "retry"
	Verify              Action = "verify"
	Reset               Action = "reset"
	ProceedUnregistered Action = "unregistered"
)

type Request struct {
	Host              string
	Nick              string // The nick displayed to the user when this action was started.
	Action            Action
	Email             string
	Code              string
	RulesAcknowledged bool
	OnProgress        func(download.NickServProgress)
}

type Result struct {
	Identified   bool
	Pending      bool
	Reset        bool
	Unregistered bool
	Status       nickservstate.Status
}
type Exchange func(context.Context, download.NickServRequest) (download.NickServResult, error)

func findNetwork(cfg *config.IRCConfig, host string) *config.IRCNetwork {
	for i := range cfg.Networks {
		if strings.EqualFold(cfg.Networks[i].Host, host) {
			return &cfg.Networks[i]
		}
	}
	return nil
}

func Run(ctx context.Context, s Settings, req Request, exchange Exchange) (Result, error) {
	manualExchange := exchange == nil
	if manualExchange {
		exchange = download.ManualNickServ
	}
	if req.Host == "" || req.Nick == "" {
		return Result{}, errors.New("select and save an IRC network and nick first")
	}
	cfg := s.IRCConfig().WithDefaults()
	cfg.Networks = append([]config.IRCNetwork(nil), cfg.Networks...)
	n := findNetwork(&cfg, req.Host)
	if n == nil || !strings.EqualFold(n.Nick, req.Nick) {
		return Result{}, errors.New("network identity changed; reopen IRC settings")
	}
	persist := func() error { return s.UpdateLocalSettings(s.DownloadDir(), cfg, false, cfg.RegistrationEmail) }
	switch req.Action {
	case Reset:
		if !n.NickServ.RegistrationPending {
			return Result{Reset: true}, nil
		}
		n.NickServ.Password = ""
		n.NickServ.Enabled = false
		n.NickServ.RequiresRegistration = true
		n.NickServ.RegistrationPending = false
		n.NickServ.RegistrationStatus = ""
		if err := persist(); err != nil {
			return Result{}, fmt.Errorf("save NickServ reset: %w", err)
		}
		return Result{Reset: true}, nil
	case ProceedUnregistered:
		if !n.NickServ.RequiresRegistration || n.NickServ.RegistrationPending {
			return Result{}, errors.New("registration is not available to skip")
		}
		n.NickServ.Enabled = false
		n.NickServ.RequiresRegistration = false
		n.NickServ.Password = ""
		n.NickServ.RegistrationStatus = ""
		if err := persist(); err != nil {
			return Result{}, fmt.Errorf("save unregistered identity: %w", err)
		}
		return Result{Unregistered: true}, nil
	case Register, Retry:
		if !n.TLS {
			return Result{}, errors.New("NickServ registration requires TLS")
		}
		if !req.RulesAcknowledged {
			return Result{}, errors.New("confirm that the IRC network permits this registration")
		}
		email := strings.TrimSpace(req.Email)
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email || len(email) > 128 || strings.ContainsAny(email, " \t\r\n") {
			return Result{}, errors.New("enter a valid registration email address")
		}
		// Remember the address for this network only. A retry may explicitly
		// change it; a failed REGISTER still retains the chosen address.
		if req.Action == Register {
			if n.NickServ.RegistrationPending || n.NickServ.Password != "" {
				return Result{}, errors.New("registration already started; verify or explicitly retry with the saved credentials")
			}
			password, err := download.NewNickServPassword()
			if err != nil {
				return Result{}, fmt.Errorf("generate NickServ password: %w", err)
			}
			n.RegistrationEmail = email
			n.NickServ.Password = password
			n.NickServ.Enabled = false
			n.NickServ.RegistrationPending = true
			n.NickServ.RegistrationStatus = nickservstate.Unconfirmed
			n.NickServ.RequiresRegistration = true
			// Save before REGISTER: a lost server response must not lose the credential.
			if err := persist(); err != nil {
				return Result{}, fmt.Errorf("save pending NickServ credentials: %w", err)
			}
		} else if !n.NickServ.RegistrationPending || n.NickServ.Password == "" {
			return Result{}, errors.New("no saved pending credentials; reset and start registration")
		} else if n.RegistrationEmail != email {
			n.RegistrationEmail = email
			if err := persist(); err != nil {
				return Result{}, fmt.Errorf("save NickServ registration email: %w", err)
			}
		}
	case Verify:
		if !n.TLS {
			return Result{}, errors.New("NickServ verification requires TLS")
		}
		if !n.NickServ.RegistrationPending || n.NickServ.Password == "" {
			return Result{}, errors.New("no pending NickServ registration to verify")
		}
	default:
		return Result{}, errors.New("unsupported NickServ action")
	}

	action := download.NickServVerify
	if req.Action == Register || req.Action == Retry {
		action = download.NickServRegister
	} else if strings.TrimSpace(req.Code) != "" {
		action = download.NickServConfirm
	}
	outcome, err := exchange(ctx, download.NickServRequest{Host: n.Host, Port: n.Port, Nick: n.Nick, Password: n.NickServ.Password, Email: strings.TrimSpace(req.Email), Code: strings.TrimSpace(req.Code), Action: action, OnProgress: req.OnProgress})
	if manualExchange && err != nil && outcome.Status == "" {
		outcome.Status = nickservstate.Unconfirmed // TLS/handshake failure; no server verdict.
	}
	// A failed/ambiguous network exchange must never discard the password.
	// Only explicitly reported evidence changes the durable UI status. Re-read
	// settings first so a concurrent nick/password edit is never overwritten.
	if outcome.Status != "" && !outcome.Identified {
		latest := s.IRCConfig().WithDefaults()
		latest.Networks = append([]config.IRCNetwork(nil), latest.Networks...)
		selected := findNetwork(&latest, req.Host)
		if selected == nil || !strings.EqualFold(selected.Nick, req.Nick) || !selected.NickServ.RegistrationPending || selected.NickServ.Password != n.NickServ.Password {
			return Result{}, errors.New("network credentials changed during NickServ action; reopen settings")
		}
		if selected.NickServ.RegistrationStatus != outcome.Status {
			selected.NickServ.RegistrationStatus = outcome.Status
			if saveErr := s.UpdateLocalSettings(s.DownloadDir(), latest, false, latest.RegistrationEmail); saveErr != nil {
				return Result{}, fmt.Errorf("save NickServ registration status: %w", saveErr)
			}
		}
	}
	if err != nil {
		return Result{Status: outcome.Status}, err
	}
	if outcome.Identified {
		// Re-read after the network request; never overwrite a nick changed while
		// this request was in flight or settings saved from another UI.
		latest := s.IRCConfig().WithDefaults()
		latest.Networks = append([]config.IRCNetwork(nil), latest.Networks...)
		selected := findNetwork(&latest, req.Host)
		if selected == nil || !strings.EqualFold(selected.Nick, req.Nick) || !selected.NickServ.RegistrationPending || selected.NickServ.Password != n.NickServ.Password {
			return Result{}, errors.New("network credentials changed during NickServ verification; reopen settings")
		}
		selected.NickServ.RegistrationPending = false
		selected.NickServ.RequiresRegistration = false
		selected.NickServ.Enabled = true
		selected.NickServ.Command = "IDENTIFY"
		selected.NickServ.RegistrationStatus = nickservstate.Identified
		if err := s.UpdateLocalSettings(s.DownloadDir(), latest, false, latest.RegistrationEmail); err != nil {
			return Result{}, fmt.Errorf("save verified NickServ identity: %w", err)
		}
		return Result{Identified: true, Status: nickservstate.Identified}, nil
	}
	if outcome.Pending {
		return Result{Pending: true, Status: outcome.Status}, nil
	}
	return Result{}, errors.New("NickServ did not confirm registration; check its notices and verify the saved nick")
}
