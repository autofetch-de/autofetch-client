package webui

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/download"
)

func (s *Server) ircRegisterCall(ctx context.Context, req download.NickServRequest) (download.NickServResult, error) {
	if s.nickServManual != nil {
		return s.nickServManual(ctx, req)
	}
	return download.ManualNickServ(ctx, req)
}

func (s *Server) redirectIRCRegistration(w http.ResponseWriter, r *http.Request, host, notice string) {
	v := url.Values{}
	v.Set("network", host)
	v.Set("notice", notice)
	http.Redirect(w, r, "/irc/setup?"+v.Encode(), http.StatusSeeOther)
}

func (s *Server) ircRegistrationPost(w http.ResponseWriter, r *http.Request, action download.NickServAction, retryExisting bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Registration changes an external identity and saves a password. Never
	// accept a blind cross-site form POST, even on localhost.
	if !s.allowedRemote(r) || !s.validLocalHost(r) || r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "same-origin action required", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	host := strings.TrimSpace(r.PostFormValue("host"))
	if host == "" {
		http.Error(w, "network required", http.StatusBadRequest)
		return
	}
	cfg := s.service.IRCConfig().WithDefaults()
	cfg.Networks = append([]config.IRCNetwork(nil), cfg.Networks...)
	n := findNetworkByHost(cfg, host)
	if n == nil || n.Host != host || n.Nick == "" {
		http.Error(w, "save the IRC network and nick first", http.StatusBadRequest)
		return
	}
	if !n.TLS {
		http.Error(w, "NickServ registration requires a verified TLS connection", http.StatusBadRequest)
		return
	}
	if action == download.NickServRegister && r.PostFormValue("network_rules_ack") != "yes" {
		http.Error(w, "confirm that the network permits this registration", http.StatusBadRequest)
		return
	}
	if action == download.NickServRegister && retryExisting && (!n.NickServ.RegistrationPending || n.NickServ.Password == "") {
		http.Error(w, "no existing pending credentials for this nick", http.StatusConflict)
		return
	}
	if n.NickServ.RegistrationPending && action == download.NickServRegister && !retryExisting && n.NickServ.Password != "" {
		http.Error(w, "registration is already pending; verify the nick before explicitly retrying with the saved credentials", http.StatusConflict)
		return
	}
	if action != download.NickServRegister && (!n.NickServ.RegistrationPending || n.NickServ.Password == "") {
		http.Error(w, "no pending registration for this nick", http.StatusConflict)
		return
	}
	email := strings.TrimSpace(r.PostFormValue("registration_email"))
	if action == download.NickServRegister {
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email || len(email) > 128 || strings.ContainsAny(email, " \t\r\n") {
			http.Error(w, "valid registration email required", http.StatusBadRequest)
			return
		}
		if n.NickServ.Password != "" && !n.NickServ.RegistrationPending {
			http.Error(w, "this nick has configured credentials; use Verify or change the nick", http.StatusConflict)
			return
		}
		if !retryExisting {
			pass, err := download.NewNickServPassword()
			if err != nil {
				http.Error(w, "could not create secure credentials", http.StatusInternalServerError)
				return
			}
			n.RegistrationEmail = email
			n.NickServ.Password = pass
			n.NickServ.Enabled = false
			n.NickServ.RegistrationPending = true
			n.NickServ.RequiresRegistration = true
			// Persist before sending REGISTER, because the response may be lost.
			if err := s.service.UpdateLocalSettings(s.service.DownloadDir(), cfg, false, cfg.RegistrationEmail); err != nil {
				http.Error(w, "could not securely store pending registration", http.StatusInternalServerError)
				return
			}
		} else if n.RegistrationEmail != email {
			// Retrying with a different address updates only this network.
			n.RegistrationEmail = email
			if err := s.service.UpdateLocalSettings(s.service.DownloadDir(), cfg, false, cfg.RegistrationEmail); err != nil {
				http.Error(w, "could not store registration email", http.StatusInternalServerError)
				return
			}
		}
	}
	code := strings.TrimSpace(r.PostFormValue("confirmation_code"))
	if action != download.NickServRegister && code != "" {
		action = download.NickServConfirm
	}
	var stream *registrationStream
	if strings.Contains(r.Header.Get("Accept"), "application/x-ndjson") {
		stream = startRegistrationStream(w)
		stream.event("starting", "", "", 0)
	}
	out, err := s.ircRegisterCall(r.Context(), download.NickServRequest{
		Host: n.Host, Port: n.Port, Nick: n.Nick, Password: n.NickServ.Password, Email: email, Code: code, Action: action,
		OnProgress: func(p download.NickServProgress) {
			if stream != nil {
				stream.event(p.Kind, p.Message, "", p.WaitSeconds)
			}
		},
	})
	if err != nil {
		// Keep the generated credential and pending state. Show the existing
		// verification controls instead of replacing the UI with an error page.
		// The download layer has already redacted any NickServ notice.
		if stream != nil {
			stream.event("error", err.Error(), "", 0)
			return
		}
		s.renderIRCRegistrationError(w, r, host, err.Error())
		return
	}
	if out.Identified {
		updated := s.service.IRCConfig().WithDefaults()
		updated.Networks = append([]config.IRCNetwork(nil), updated.Networks...)
		current := findNetworkByHost(updated, host)
		if current == nil || !strings.EqualFold(current.Nick, n.Nick) {
			if stream != nil {
				stream.event("error", "network changed during verification", "", 0)
			} else {
				http.Error(w, "network changed during verification", http.StatusConflict)
			}
			return
		}
		current.NickServ.RegistrationPending = false
		current.NickServ.RequiresRegistration = false
		current.NickServ.Enabled = true
		current.NickServ.Command = "IDENTIFY"
		if err := s.service.UpdateLocalSettings(s.service.DownloadDir(), updated, false, updated.RegistrationEmail); err != nil {
			if stream != nil {
				stream.event("error", "could not save verified identity", "", 0)
			} else {
				http.Error(w, "could not save verified identity", http.StatusInternalServerError)
			}
			return
		}
		if stream != nil {
			stream.event("done", "", registrationRedirectURL(host, "identified"), 0)
		} else {
			s.redirectIRCRegistration(w, r, host, "identified")
		}
		return
	}
	if out.Pending {
		if stream != nil {
			stream.event("done", "", registrationRedirectURL(host, "pending"), 0)
		} else {
			s.redirectIRCRegistration(w, r, host, "pending")
		}
		return
	}
	if stream != nil {
		stream.event("error", "No confirmation received from NickServ. Return to IRC setup and use Verify for the saved nick; do not register again.", "", 0)
	} else {
		s.renderIRCRegistrationError(w, r, host, "No confirmation received from NickServ. Return to IRC setup and use Verify for the saved nick; do not register again.")
	}
}

func (s *Server) renderIRCRegistrationError(w http.ResponseWriter, r *http.Request, host, message string) {
	clone := r.Clone(r.Context())
	path := *r.URL
	q := path.Query()
	q.Set("network", host)
	path.RawQuery = q.Encode()
	clone.URL = &path
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	s.renderIRCSetup(w, clone, message)
}

func (s *Server) handleIRCRegister(w http.ResponseWriter, r *http.Request) {
	s.ircRegistrationPost(w, r, download.NickServRegister, false)
}

// An explicit retry is only possible with an already pending nick and its
// existing credential. It never generates another nick or password.
func (s *Server) handleIRCRegisterRetry(w http.ResponseWriter, r *http.Request) {
	s.ircRegistrationPost(w, r, download.NickServRegister, true)
}
func (s *Server) handleIRCReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowedRemote(r) || !s.validLocalHost(r) || r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "same-origin action required", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	host := strings.TrimSpace(r.PostFormValue("host"))
	cfg := s.service.IRCConfig().WithDefaults()
	cfg.Networks = append([]config.IRCNetwork(nil), cfg.Networks...)
	n := findNetworkByHost(cfg, host)
	if n == nil {
		http.Error(w, "unknown IRC network", http.StatusNotFound)
		return
	}
	// A previous reset may have been persisted even if the old worker restart
	// returned an error. Treat a second click from a stale page as a no-op.
	if !n.NickServ.RegistrationPending {
		s.redirectIRCRegistration(w, r, host, "")
		return
	}
	n.NickServ.RegistrationPending = false
	n.NickServ.RequiresRegistration = true
	n.NickServ.Enabled = false
	n.NickServ.Password = ""
	if err := s.service.UpdateLocalSettings(s.service.DownloadDir(), cfg, false, cfg.RegistrationEmail); err != nil {
		log.Printf("NickServ registration reset failed for network %q: %v", host, err)
		http.Error(w, "could not save registration reset; check client logs and configuration filesystem", http.StatusInternalServerError)
		return
	}
	s.redirectIRCRegistration(w, r, host, "reset")
}

func (s *Server) handleIRCVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		// A blank code means check identity after an email activation link.
		// A nonblank code requests NickServ CONFIRM before IDENTIFY.
		s.ircRegistrationPost(w, r, download.NickServVerify, false)
		return
	}
	http.Error(w, errors.New("method not allowed").Error(), http.StatusMethodNotAllowed)
}

// Some IRC networks have no NickServ at all. Let an informed user proceed
// without registration for public channels, never automatically after a 477.
func (s *Server) handleIRCUnregistered(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowedRemote(r) || !s.validLocalHost(r) || r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "same-origin action required", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	host := strings.TrimSpace(r.PostFormValue("host"))
	cfg := s.service.IRCConfig().WithDefaults()
	cfg.Networks = append([]config.IRCNetwork(nil), cfg.Networks...)
	n := findNetworkByHost(cfg, host)
	if n == nil || !n.NickServ.RequiresRegistration || n.NickServ.RegistrationPending {
		http.Error(w, "no new unregistered nickname to enable", http.StatusConflict)
		return
	}
	n.NickServ.RequiresRegistration = false
	n.NickServ.Enabled = false
	n.NickServ.Password = ""
	if err := s.service.UpdateLocalSettings(s.service.DownloadDir(), cfg, false, cfg.RegistrationEmail); err != nil {
		http.Error(w, "could not save settings", http.StatusInternalServerError)
		return
	}
	s.redirectIRCRegistration(w, r, host, "unregistered")
}
