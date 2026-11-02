package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
)

// registrationStream streams an explicit browser action directly over the POST.
// No background task, new polling endpoint or persistent registration log is needed.
// Events carry only fixed phase identifiers and sanitized NickServ notices.
type registrationStream struct {
	w     http.ResponseWriter
	f     http.Flusher
	count int
}

func startRegistrationStream(w http.ResponseWriter) *registrationStream {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &registrationStream{w: w, f: f}
}

func (s *registrationStream) event(kind, message, location string, waitSeconds int) {
	if s == nil || (s.count >= 38 && kind != "done" && kind != "error") {
		return
	}
	s.count++
	// Only a server-authored relative location can be sent to the browser.
	if len(message) > 500 {
		message = message[:500]
	}
	_ = json.NewEncoder(s.w).Encode(map[string]any{
		"kind": kind, "message": message, "location": location, "wait_seconds": waitSeconds,
	})
	s.f.Flush()
}

func registrationRedirectURL(host, notice string) string {
	v := url.Values{}
	v.Set("network", host)
	v.Set("notice", notice)
	return "/irc/setup?" + v.Encode()
}
