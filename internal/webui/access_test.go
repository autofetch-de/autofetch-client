package webui

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/observe"
)

func secureUI(t *testing.T, addr string, opts AccessOptions) (*Server, error) {
	t.Helper()
	return NewWithOptions(addr, observe.NewState("https://autofetch.de", "", "test", 10), &archiveRetryService{}, localization.New(localization.English), opts)
}

func passwordFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "webui-password")
	if err := os.WriteFile(p, []byte("correct-strong-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLANBindingFailsClosed(t *testing.T) {
	password := passwordFixture(t)
	cases := []struct {
		addr string
		opts AccessOptions
		ok   bool
	}{
		{"127.0.0.1:23324", AccessOptions{}, true},
		{"localhost:23324", AccessOptions{}, true},
		{"[::1]:23324", AccessOptions{}, true},
		{"192.168.1.50:23324", AccessOptions{}, false},
		{"192.168.1.50:23324", AccessOptions{PasswordFile: password}, true},
		{"192.168.1.50:23324", AccessOptions{AllowUnauthenticatedLAN: true}, true},
		{"192.168.1.50:23324", AccessOptions{PasswordFile: password, AllowUnauthenticatedLAN: true}, true},
		{"[fd12::1234]:23324", AccessOptions{PasswordFile: password}, true},
		{"0.0.0.0:23324", AccessOptions{PasswordFile: password}, false},
		{"[::]:23324", AccessOptions{PasswordFile: password}, false},
		{":23324", AccessOptions{PasswordFile: password}, false},
		{"8.8.8.8:23324", AccessOptions{PasswordFile: password}, false},
		{"autofetch.local:23324", AccessOptions{PasswordFile: password}, false},
	}
	for _, tc := range cases {
		_, err := secureUI(t, tc.addr, tc.opts)
		if (err == nil) != tc.ok {
			t.Errorf("addr=%s opts=%+v err=%v", tc.addr, tc.opts, err)
		}
	}
	// Legacy New() remains safe if someone bypasses NewWithOptions.
	s := New("0.0.0.0:0", observe.NewState("", "", "", 10), &testService{}, localization.New(localization.English))
	if err := s.Start(); err == nil {
		t.Fatal("unsafe legacy bind accepted")
	}
}

func TestLANPasswordFileValidation(t *testing.T) {
	p := passwordFixture(t)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(p, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := secureUI(t, "192.168.1.50:23324", AccessOptions{PasswordFile: p}); err == nil {
			t.Fatal("world-readable secret accepted")
		}
		if err := os.Chmod(p, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(p, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := secureUI(t, "192.168.1.50:23324", AccessOptions{PasswordFile: p}); err == nil {
		t.Fatal("weak password accepted")
	}
	if err := os.WriteFile(p, []byte("strong-password-for-test"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(p, link); err == nil {
		if _, err := secureUI(t, "192.168.1.50:23324", AccessOptions{PasswordFile: link}); err == nil {
			t.Fatal("password symlink accepted")
		}
	}
}

func TestLANAccessControlsProtectEveryRoute(t *testing.T) {
	s, err := secureUI(t, "192.168.1.50:23324", AccessOptions{PasswordFile: passwordFixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, method, path, remote, host, origin, user, password, contentType, action, body string
		status                                                                              int
	}{
		{name: "index login", method: "GET", path: "/", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", status: 401},
		{name: "status login", method: "GET", path: "/api/status", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", status: 401},
		{name: "bad password", method: "GET", path: "/api/status", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", user: "autofetch", password: "wrong", status: 401},
		{name: "wrong username", method: "GET", path: "/api/status", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", user: "admin", password: "correct-strong-password", status: 401},
		{name: "authenticated status", method: "GET", path: "/api/status", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", user: "autofetch", password: "correct-strong-password", status: 200},
		{name: "public remote denied", method: "GET", path: "/api/status", remote: "203.0.113.5:1234", host: "192.168.1.50:23324", user: "autofetch", password: "correct-strong-password", status: 403},
		{name: "rebind host denied", method: "GET", path: "/api/status", remote: "192.168.1.12:1234", host: "evil.example:23324", user: "autofetch", password: "correct-strong-password", status: 403},
		{name: "foreign origin denied", method: "POST", path: "/api/stop", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", origin: "http://evil.example", user: "autofetch", password: "correct-strong-password", status: 403},
		{name: "missing origin denied", method: "POST", path: "/api/stop", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", user: "autofetch", password: "correct-strong-password", status: 403},
		{name: "same origin management", method: "POST", path: "/api/stop", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", origin: "http://192.168.1.50:23324", user: "autofetch", password: "correct-strong-password", status: 200},
		{name: "history protected", method: "GET", path: "/api/history", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", user: "autofetch", password: "correct-strong-password", status: 501}, // no service reader
		{name: "retry authenticated", method: "POST", path: "/api/archives/retry", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", origin: "http://192.168.1.50:23324", user: "autofetch", password: "correct-strong-password", contentType: "application/json", action: "retry", body: `{"job_id":"job-1"}`, status: 200},
		{name: "retry no header", method: "POST", path: "/api/archives/retry", remote: "192.168.1.12:1234", host: "192.168.1.50:23324", origin: "http://192.168.1.50:23324", user: "autofetch", password: "correct-strong-password", contentType: "application/json", body: `{"job_id":"job-1"}`, status: 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://192.168.1.50:23324"+tc.path, strings.NewReader(tc.body))
			req.RemoteAddr, req.Host = tc.remote, tc.host
			if tc.user != "" {
				req.SetBasicAuth(tc.user, tc.password)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.action != "" {
				req.Header.Set("X-Autofetch-Local-Action", tc.action)
			}
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d, body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing security headers")
			}
		})
	}
}

func TestLANWithoutPasswordStillGuardsHostAndPOST(t *testing.T) {
	s, err := secureUI(t, "192.168.1.50:23324", AccessOptions{AllowUnauthenticatedLAN: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, host, origin string
		status               int
	}{
		{"GET", "192.168.1.50:23324", "", 200},
		{"GET", "attacker.example:23324", "", 403},
		{"POST", "192.168.1.50:23324", "", 403},
		{"POST", "192.168.1.50:23324", "http://192.168.1.50:23324", 200},
	} {
		path := "/api/status"
		if tc.method == "POST" {
			path = "/api/stop"
		}
		req := httptest.NewRequest(tc.method, "http://192.168.1.50:23324"+path, nil)
		req.RemoteAddr = "192.168.1.12:1234"
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		rec := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Errorf("method=%s host=%s got=%d want=%d", tc.method, tc.host, rec.Code, tc.status)
		}
	}
}

func TestStartReportsBindFailuresSynchronously(t *testing.T) {
	s, err := secureUI(t, "127.0.0.1:0", AccessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s2, err := secureUI(t, ln.Addr().String(), AccessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Start(); err == nil {
		t.Fatal("bind conflict was not returned")
	}
	// Binding conflicts should fail immediately instead of logging from a goroutine.
	// Use an allocated fixed-port listener rather than relying on port 23324.
}

func TestPersistedPasswordBeatsLegacyNoPasswordFlagOnRestart(t *testing.T) {
	path := passwordFixture(t)
	s, err := secureUI(t, "192.168.1.50:23324", AccessOptions{PasswordFile: path, AllowUnauthenticatedLAN: true})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.50:23324/api/status", nil)
	req.RemoteAddr = "192.168.1.25:1234"
	req.Host = "192.168.1.50:23324"
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("password was bypassed on restart: %d", rec.Code)
	}
	req.SetBasicAuth("autofetch", "correct-strong-password")
	rec = httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("password did not authenticate: %d", rec.Code)
	}
}
