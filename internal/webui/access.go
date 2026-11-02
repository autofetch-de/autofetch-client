package webui

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/autofetch-de/autofetch-client/internal/config"
)

// AccessOptions controls remote exposure. A password file is deliberately
// used instead of accepting a secret on the command line or in client.json.
type AccessOptions struct {
	PasswordFile            string
	AllowUnauthenticatedLAN bool
}

func (s *Server) configureAccess(options AccessOptions) error {
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil || port == "" {
		return fmt.Errorf("invalid webui listen address: %q", s.addr)
	}
	var ip net.IP
	switch strings.ToLower(host) {
	case "localhost":
		ip = net.ParseIP("127.0.0.1")
	default:
		ip = net.ParseIP(host)
	}
	// Do not accept unspecified addresses, external/public IPs or DNS names.
	// Explicit RFC1918 / ULA address only, never * or 0.0.0.0.
	if ip == nil || ip.IsUnspecified() || !(ip.IsLoopback() || ip.IsPrivate()) {
		return fmt.Errorf("webui address must be localhost, loopback or an explicit private IP: %q", host)
	}
	s.bindHost = host
	s.lan = !ip.IsLoopback()
	if s.lan && options.PasswordFile == "" && !options.AllowUnauthenticatedLAN {
		return errors.New("webui LAN access requires --webui-password-file or explicit --webui-lan-no-password")
	}
	if options.PasswordFile != "" {
		// A persisted password always wins over an older bootstrap flag.
		// This is essential when systemd still passes --webui-lan-no-password
		// after the user enabled authentication in the WebUI.
		info, err := os.Lstat(options.PasswordFile)
		if err != nil {
			return fmt.Errorf("webui password file: %w", err)
		}
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return errors.New("webui password file must be regular and private (0600 or 0400)")
		}
		if info.Size() > 4096 {
			return errors.New("webui password file is too large")
		}
		value, err := os.ReadFile(options.PasswordFile)
		if err != nil {
			return fmt.Errorf("webui password file: %w", err)
		}
		// A trailing newline from `printf`/password managers is optional.
		secret := strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r")
		if err := config.ValidateWebUIPassword(secret); err != nil {
			return err
		}
		s.password = []byte(secret)
	}
	return nil
}

func isLoopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func (s *Server) allowedRemote(r *http.Request) bool {
	if !s.lan {
		return isLoopbackRemote(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback())
}

func (s *Server) validLocalHost(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		return false
	}
	if !s.lan {
		ip := net.ParseIP(host)
		return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
	}
	// Bind to exactly one private IP. Do not trust Host aliases, even if they
	// happen to resolve to the same machine (DNS rebinding).
	return host == s.bindHost && r.Host == s.addr
}

func (s *Server) allowedPrivateData(r *http.Request) bool { return s.allowedRemote(r) }

func (s *Server) allowedLocalAction(r *http.Request, action string) bool {
	if !s.allowedRemote(r) || r.Header.Get("X-Autofetch-Local-Action") != action {
		return false
	}
	if !s.validLocalHost(r) {
		return false
	}
	return r.Header.Get("Origin") == "" || r.Header.Get("Origin") == "http://"+r.Host
}

func (s *Server) guardRequest(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	// Keep off-site referrers suppressed while allowing browsers to submit
	// same-origin forms with a non-null Origin header on the LAN.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	if s.lan && !s.allowedRemote(r) {
		http.Error(w, "local network only", http.StatusForbidden)
		return false
	}
	if s.lan {
		if !s.validLocalHost(r) {
			http.Error(w, "invalid host", http.StatusForbidden)
			return false
		}
		// No credentialed CSRF (including browser form POST) and no
		// cross-origin unauthenticated management actions on the LAN.
		if r.Method == http.MethodPost && r.Header.Get("Origin") != "http://"+r.Host {
			http.Error(w, "same-origin POST required", http.StatusForbidden)
			return false
		}
	}
	s.accessMu.RLock()
	defer s.accessMu.RUnlock()
	if len(s.password) != 0 {
		username, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(username), []byte("autofetch")) != 1 || subtle.ConstantTimeCompare([]byte(pass), s.password) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="autofetch local UI", charset="UTF-8"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return false
		}
	}
	return true
}
