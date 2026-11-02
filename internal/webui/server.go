package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/archive"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/download"
	internalirc "github.com/autofetch-de/autofetch-client/internal/irc"
	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/observe"
	"github.com/autofetch-de/autofetch-client/internal/update"
	"github.com/autofetch-de/autofetch-client/internal/worker"
)

const DefaultPort = 23324

type service interface {
	Start() error
	Stop() error
	StartRePair() error
	TestConnection(context.Context) error
	DownloadDir() string
	IRCConfig() config.IRCConfig
	UpdateLocalSettings(dir string, ircCfg config.IRCConfig, autoRegister bool, registrationEmail string) error
}

type Server struct {
	state          *observe.State
	service        service
	http           *http.Server
	addr           string
	l              *localization.Localizer
	bindHost       string
	lan            bool
	accessMu       sync.RWMutex
	password       []byte
	nickServManual func(context.Context, download.NickServRequest) (download.NickServResult, error)
}

func New(addr string, state *observe.State, service service, l *localization.Localizer) *Server {
	if l == nil {
		l = localization.New(localization.English)
	}
	mux := http.NewServeMux()
	s := &Server{state: state, service: service, addr: addr, l: l}
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/update", s.handleUpdate)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/history/hide", s.handleHistoryHide)
	mux.HandleFunc("/api/archives", s.handleArchives)
	mux.HandleFunc("/api/archives/retry", s.handleArchiveRetry)
	mux.HandleFunc("/api/archive-settings", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "managed in autofetch portal", http.StatusGone)
	})
	mux.HandleFunc("/api/start", s.handleStart)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/test", s.handleTest)
	mux.HandleFunc("/api/repair", s.handleRepair)
	mux.HandleFunc("/irc/setup", s.handleIRCSetup)
	mux.HandleFunc("/irc/setup/register", s.handleIRCRegister)
	mux.HandleFunc("/irc/setup/register-retry", s.handleIRCRegisterRetry)
	mux.HandleFunc("/irc/setup/verify", s.handleIRCVerify)
	mux.HandleFunc("/irc/setup/reset", s.handleIRCReset)
	mux.HandleFunc("/irc/setup/unregistered", s.handleIRCUnregistered)
	mux.HandleFunc("/settings", s.handleSettings)
	mux.HandleFunc("/settings/webui-password", s.handleSetWebUIPassword)
	s.http = &http.Server{Addr: addr, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.guardRequest(w, r) {
			mux.ServeHTTP(w, r)
		}
	})}
	return s
}

func NewWithOptions(addr string, state *observe.State, service service, l *localization.Localizer, options AccessOptions) (*Server, error) {
	s := New(addr, state, service, l)
	if err := s.configureAccess(options); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) Start() error {
	// Validate even legacy New() callers before listening; never expose the
	// default no-password server on a non-loopback interface.
	if s.bindHost == "" {
		if err := s.configureAccess(AccessOptions{}); err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	go func() {
		if err := s.http.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("local ui server failed: %v", err)
		}
	}()
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
func (s *Server) URL() string                        { return "http://" + s.addr }

func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	rawSnap := s.state.Snapshot()
	snap := s.localizeSnapshot(rawSnap)
	network, channel := parseIRCAuthContext(rawSnap.LastError)
	setupLink := ircSetupLink(network, channel)
	data := map[string]any{"Title": s.l.T("web.title"), "Lang": s.l.Language(), "T": s.l.T, "Snapshot": snap, "ShowIRCAuth": hasIRCAuthProblem(rawSnap.LastError), "IRCSetupLink": setupLink, "IRCNetwork": network, "IRCChannel": channel}
	_ = pageTemplate.Execute(w, data)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	raw := s.state.Snapshot()
	network, channel := parseIRCAuthContext(raw.LastError)
	// Include structured auth diagnostics so the guide can appear while the
	// dashboard is already open. Do not expose raw IRC messages or passwords.
	out := struct {
		observe.Snapshot
		IRCAuthRequired  bool   `json:"irc_auth_required"`
		IRCAuthNetwork   string `json:"irc_auth_network,omitempty"`
		IRCAuthChannel   string `json:"irc_auth_channel,omitempty"`
		IRCAuthSetupLink string `json:"irc_auth_setup_link,omitempty"`
	}{
		Snapshot:         s.localizeSnapshot(raw),
		IRCAuthRequired:  hasIRCAuthProblem(raw.LastError),
		IRCAuthNetwork:   network,
		IRCAuthChannel:   channel,
		IRCAuthSetupLink: ircSetupLink(network, channel),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reader, ok := s.service.(interface{ UpdateStatus() update.Status })
	if !ok {
		http.Error(w, "unavailable", http.StatusNotImplemented)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reader.UpdateStatus())
}

// Local history contains filenames. Do not expose it to non-loopback callers,
// even if the user chose to bind the status UI to a network interface.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowedPrivateData(r) {
		http.Error(w, "local access only", http.StatusForbidden)
		return
	}
	reader, ok := s.service.(interface {
		DownloadHistory(context.Context, int) ([]worker.DownloadHistoryItem, error)
	})
	if !ok {
		http.Error(w, "history unavailable", http.StatusNotImplemented)
		return
	}
	items, err := reader.DownloadHistory(r.Context(), 100)
	if err != nil {
		http.Error(w, "history unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(items)
}

// Hide only the *presentation* of local history. Keep the durable receipts,
// including postprocessed archive destinations, for safe client recovery.
func (s *Server) handleHistoryHide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowedLocalAction(r, "history-hide") {
		http.Error(w, "local access only", http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
		return
	}
	hider, ok := s.service.(interface {
		HideDownloadHistory(context.Context, string, bool) error
	})
	if !ok {
		http.Error(w, "history unavailable", http.StatusNotImplemented)
		return
	}
	var body struct {
		JobID string `json:"job_id"`
		All   bool   `json:"all"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || (body.All && body.JobID != "") || (!body.All && (len(body.JobID) > 128 || strings.TrimSpace(body.JobID) == "")) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := hider.HideDownloadHistory(r.Context(), body.JobID, body.All); err != nil {
		s.writeJSONError(w, http.StatusConflict, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// /api/archives mirrors the privacy restriction on /api/history.
func (s *Server) handleArchives(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowedPrivateData(r) {
		http.Error(w, "local access only", http.StatusForbidden)
		return
	}
	reader, ok := s.service.(interface {
		ArchiveHistory(context.Context, int) ([]archive.Item, error)
	})
	if !ok {
		http.Error(w, "archive history unavailable", http.StatusNotImplemented)
		return
	}
	items, err := reader.ArchiveHistory(r.Context(), 100)
	if err != nil {
		http.Error(w, "archive history unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(items)
}

// Retry mutations require an explicit custom header and a trusted peer.
// The LAN middleware also verifies the Host and same-origin POST.
func (s *Server) handleArchiveRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowedLocalAction(r, "retry") {
		http.Error(w, "local access only", http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
		return
	}
	retry, ok := s.service.(interface {
		RetryArchive(context.Context, string) error
	})
	if !ok {
		http.Error(w, "retry unavailable", http.StatusNotImplemented)
		return
	}
	var request struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.JobID) == "" || len(request.JobID) > 128 {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	if err := retry.RetryArchive(r.Context(), request.JobID); err != nil {
		s.writeJSONError(w, http.StatusConflict, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleArchiveSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowedLocalAction(r, "archive-settings") {
		http.Error(w, "local access only", http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
		return
	}
	updater, ok := s.service.(interface {
		UpdateArchiveOptions(config.ArchiveOptions) error
	})
	if !ok {
		http.Error(w, "archive settings unavailable", http.StatusNotImplemented)
		return
	}
	var options config.ArchiveOptions
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := options.Validate(); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	if err := updater.UpdateArchiveOptions(options); err != nil {
		s.writeJSONError(w, http.StatusConflict, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) localizedSnapshot() observe.Snapshot {
	return s.localizeSnapshot(s.state.Snapshot())
}

func (s *Server) localizeSnapshot(snap observe.Snapshot) observe.Snapshot {
	snap.PairingStatus = s.l.Status(snap.PairingStatus)
	snap.LastDownloadStatus = s.l.Status(snap.LastDownloadStatus)
	snap.LastError = s.l.UserError(snap.LastError)
	snap.LastPoll = s.l.FormatTimestamp(snap.LastPoll)
	snap.PairingExpiry = s.l.FormatTimestamp(snap.PairingExpiry)
	return snap
}

func (s *Server) writeJSONError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": s.l.UserError(err.Error())})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.service.Start(); err != nil {
		s.writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.service.Stop(); err != nil {
		s.writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	err := s.service.TestConnection(ctx)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": s.l.UserError(err.Error())})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleRepair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.service.StartRePair(); err != nil {
		s.writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.renderSettings(w, r, "")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	// Leave the password unchanged when both fields are empty. Password
	// writes require a same-origin request even on localhost (login CSRF).
	password, repeat := r.PostFormValue("webui_password"), r.PostFormValue("webui_password_repeat")
	if password != "" || repeat != "" {
		if !s.allowedRemote(r) || !s.validLocalHost(r) || r.Header.Get("Origin") != "http://"+r.Host {
			http.Error(w, "same-origin action required", http.StatusForbidden)
			return
		}
		if password != repeat {
			s.renderSettings(w, r, s.l.T("webui.password_mismatch"))
			return
		}
		if err := config.ValidateWebUIPassword(password); err != nil {
			s.renderSettings(w, r, err.Error())
			return
		}
	}
	downloadDir := strings.TrimSpace(r.FormValue("download_dir"))
	if downloadDir == "" {
		downloadDir = strings.TrimSpace(s.service.DownloadDir())
	}
	// A password-only save must not restart a running download worker.
	// Local settings are persisted only if the download folder changed.
	if filepath.Clean(downloadDir) != filepath.Clean(s.service.DownloadDir()) {
		ircCfg := s.service.IRCConfig().WithDefaults()
		if err := s.service.UpdateLocalSettings(downloadDir, ircCfg, ircCfg.AutoRegister, ircCfg.RegistrationEmail); err != nil {
			s.renderSettings(w, r, err.Error())
			return
		}
	}
	if password != "" {
		// Hold the access lock during persistence so later requests cannot
		// enter with stale credentials once the change is completed.
		s.accessMu.Lock()
		var err error
		if len(s.password) == 0 {
			if setter, ok := s.service.(interface{ SetWebUIPassword(string) error }); ok {
				err = setter.SetWebUIPassword(password)
			} else {
				err = fmt.Errorf("WebUI password setup unavailable")
			}
		} else {
			if changer, ok := s.service.(interface{ ChangeWebUIPassword(string) error }); ok {
				err = changer.ChangeWebUIPassword(password)
			} else {
				err = fmt.Errorf("WebUI password change unavailable")
			}
		}
		if err == nil {
			s.password = []byte(password)
		}
		s.accessMu.Unlock()
		if err != nil {
			s.renderSettings(w, r, err.Error())
			return
		}
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// handleSetWebUIPassword is a one-time bootstrap action. The password is never
// sent to the portal or stored in client.json. A same-origin POST is mandatory
// even for loopback, to prevent login-CSRF and unauthenticated LAN lockout.
func (s *Server) handleSetWebUIPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowedRemote(r) || !s.validLocalHost(r) || r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "same-origin action required", http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		http.Error(w, "form encoding required", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	password := r.PostFormValue("webui_password")
	if password != r.PostFormValue("webui_password_repeat") {
		s.renderSettings(w, r, s.l.T("webui.password_mismatch"))
		return
	}
	if err := config.ValidateWebUIPassword(password); err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	setter, ok := s.service.(interface{ SetWebUIPassword(string) error })
	if !ok {
		http.Error(w, "password setup unavailable", http.StatusNotImplemented)
		return
	}
	s.accessMu.Lock()
	if len(s.password) != 0 {
		s.accessMu.Unlock()
		http.Error(w, "password already configured", http.StatusConflict)
		return
	}
	// Hold the access lock while persisting so new requests cannot enter the
	// unprotected mode after the password is successfully installed.
	err := setter.SetWebUIPassword(password)
	if err == nil {
		s.password = []byte(password)
	}
	s.accessMu.Unlock()
	if err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	// The next request requires HTTP Basic Auth (user: autofetch).
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, errMsg string) {
	_, canSetup := s.service.(interface{ SetWebUIPassword(string) error })
	_, canChange := s.service.(interface{ ChangeWebUIPassword(string) error })
	s.accessMu.RLock()
	hasPassword := len(s.password) != 0
	s.accessMu.RUnlock()
	data := map[string]any{
		"ShowWebUIPasswordFields": (canSetup && !hasPassword) || (canChange && hasPassword),
		"WebUIPasswordEnabled":    hasPassword,
		"Title":                   s.l.T("settings.title"),
		"Lang":                    s.l.Language(),
		"T":                       s.l.T,
		"Error":                   s.l.UserError(errMsg),
		"DownloadDir":             s.service.DownloadDir(),
	}

	_ = settingsTemplate.Execute(w, data)
}

func (s *Server) handleIRCSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.renderIRCSetup(w, r, "")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	downloadDir := strings.TrimSpace(r.FormValue("download_dir"))
	if downloadDir == "" {
		downloadDir = strings.TrimSpace(s.service.DownloadDir())
	}
	ircCfg := s.service.IRCConfig().WithDefaults()
	// WithDefaults returns a shallow copy. Never mutate its shared Networks
	// backing array: saved NickServ/SASL secrets must survive a settings POST.
	ircCfg.Networks = append([]config.IRCNetwork(nil), ircCfg.Networks...)
	ircCfg.DefaultNick = strings.TrimSpace(r.FormValue("default_nick"))
	ircCfg.AutoRegister = false // Registration requires an explicit WebUI action.
	// The registration email is provided by the explicit NickServ form, not the network settings form.
	// Preserve any legacy saved value instead of silently clearing it when saving a nick.
	ircCfg.ReverseDCCEnabled = r.FormValue("reverse_dcc_enabled") != ""
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("reverse_dcc_port_min"))); err == nil {
		ircCfg.ReverseDCCPortMin = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("reverse_dcc_port_max"))); err == nil {
		ircCfg.ReverseDCCPortMax = v
	}
	ircCfg = ircCfg.WithDefaults()

	networkHost := strings.TrimSpace(r.FormValue("host"))
	if networkHost == "" {
		if err := s.service.UpdateLocalSettings(downloadDir, ircCfg, ircCfg.AutoRegister, ircCfg.RegistrationEmail); err != nil {
			s.renderIRCSetup(w, r, err.Error())
			return
		}
		http.Redirect(w, r, "/irc/setup", http.StatusSeeOther)
		return
	}
	generateNick := r.FormValue("generate_nick") != ""
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	if port <= 0 {
		if r.FormValue("tls") != "" {
			port = 6697
		} else {
			port = 6667
		}
	}
	tlsEnabled := r.FormValue("tls") != ""
	name := strings.TrimSpace(r.FormValue("name"))
	idx := -1
	for i := range ircCfg.Networks {
		if strings.EqualFold(strings.TrimSpace(ircCfg.Networks[i].Host), networkHost) {
			idx = i
			break
		}
	}
	if idx < 0 {
		ircCfg.Networks = append(ircCfg.Networks, config.IRCNetwork{})
		idx = len(ircCfg.Networks) - 1
	}
	n := &ircCfg.Networks[idx]
	n.Name = name
	n.Host = networkHost
	n.Port = port
	n.TLS = tlsEnabled
	// Channels are observed from server jobs; the settings form must not edit them.
	oldNick := strings.TrimSpace(n.Nick)
	n.Nick = strings.TrimSpace(r.FormValue("nick"))
	n.Username = strings.TrimSpace(r.FormValue("username"))
	n.Realname = strings.TrimSpace(r.FormValue("realname"))
	if generateNick {
		newNick := internalirc.GenerateDefaultNick()
		n.Nick = newNick
		if strings.TrimSpace(n.Username) == "" || strings.EqualFold(strings.TrimSpace(n.Username), oldNick) || strings.EqualFold(strings.TrimSpace(n.Username), strings.TrimSpace(r.FormValue("nick"))) {
			n.Username = newNick
		}
	}
	n.NickServ.Enabled = r.FormValue("nickserv_enabled") != ""
	n.NickServ.Command = strings.TrimSpace(r.FormValue("nickserv_command"))
	existing := findNetworkByHost(s.service.IRCConfig().WithDefaults(), networkHost)
	if existing != nil {
		n.NickServ.RegistrationPending = existing.NickServ.RegistrationPending && strings.EqualFold(strings.TrimSpace(existing.Nick), strings.TrimSpace(n.Nick))
		n.NickServ.RequiresRegistration = existing.NickServ.RequiresRegistration && strings.EqualFold(strings.TrimSpace(existing.Nick), strings.TrimSpace(n.Nick))
		// A NickServ password belongs to the registered nick, not the network.
		// Never silently reuse it when the user selects or generates another nick.
		n.NickServ.Password = ""
		if strings.EqualFold(strings.TrimSpace(existing.Nick), strings.TrimSpace(n.Nick)) {
			n.NickServ.Password = existing.NickServ.Password
		}
		n.SASL.Username = existing.SASL.Username
		n.SASL.Password = existing.SASL.Password
	}
	if r.FormValue("clear_nickserv_password") != "" {
		n.NickServ.Password = ""
		n.NickServ.RegistrationPending = false
		n.NickServ.RequiresRegistration = false
	} else if pw := r.FormValue("nickserv_password"); pw != "" {
		n.NickServ.Password = pw
		n.NickServ.RegistrationPending = false // Existing account credentials.
		n.NickServ.RequiresRegistration = false
	}
	if generateNick {
		n.NickServ.RequiresRegistration = true
		n.NickServ.RegistrationPending = false
		n.NickServ.Enabled = false
	}
	if !strings.EqualFold(strings.TrimSpace(oldNick), strings.TrimSpace(n.Nick)) && n.NickServ.Password == "" {
		n.NickServ.RequiresRegistration = true
		n.NickServ.RegistrationPending = false
		n.NickServ.Enabled = false
	}
	n.SASL.Enabled = r.FormValue("sasl_enabled") != ""
	if r.FormValue("clear_sasl_username") != "" {
		n.SASL.Username = ""
	} else if u := strings.TrimSpace(r.FormValue("sasl_username")); u != "" {
		n.SASL.Username = u
	}
	if r.FormValue("clear_sasl_password") != "" {
		n.SASL.Password = ""
	} else if pw := r.FormValue("sasl_password"); pw != "" {
		n.SASL.Password = pw
	}
	if err := s.service.UpdateLocalSettings(downloadDir, ircCfg, ircCfg.AutoRegister, ircCfg.RegistrationEmail); err != nil {
		s.renderIRCSetup(w, r, err.Error())
		return
	}
	v := url.Values{}
	if networkHost != "" {
		v.Set("network", networkHost)
	}
	target := "/irc/setup"
	if len(v) > 0 {
		target += "?" + v.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) renderIRCSetup(w http.ResponseWriter, r *http.Request, errMsg string) {
	ircCfg := s.service.IRCConfig().WithDefaults()
	networkParam := strings.TrimSpace(r.URL.Query().Get("network"))
	channelParam := strings.TrimSpace(r.URL.Query().Get("channel"))
	selected := config.IRCNetwork{TLS: true, Port: 6697}
	if len(ircCfg.Networks) > 0 {
		selected = ircCfg.Networks[0]
	}
	for _, n := range ircCfg.Networks {
		if networkParam != "" && strings.EqualFold(strings.TrimSpace(n.Host), networkParam) {
			selected = n
			break
		}
	}
	if networkParam != "" {
		selected.Host = networkParam
		if selected.Name == "" {
			selected.Name = networkParam
		}
	}
	// The optional channel query parameter is context from a failed job, not
	// an editable network setting or a replacement for observed channels.
	data := map[string]any{"Title": s.l.T("irc.title"), "Lang": s.l.Language(), "T": s.l.T, "Error": s.l.UserError(errMsg), "DefaultNick": ircCfg.DefaultNick, "AutoRegister": ircCfg.AutoRegister, "RegistrationEmail": ircCfg.RegistrationEmail, "ReverseDCCEnabled": ircCfg.ReverseDCCEnabled, "ReverseDCCPortMin": ircCfg.ReverseDCCPortMin, "ReverseDCCPortMax": ircCfg.ReverseDCCPortMax, "Network": selected, "RequestedChannel": channelParam, "Networks": ircCfg.Networks, "Notice": r.URL.Query().Get("notice")}
	_ = ircSetupTemplate.Execute(w, data)
}

func findNetworkByHost(ircCfg config.IRCConfig, host string) *config.IRCNetwork {
	host = strings.TrimSpace(host)
	for i := range ircCfg.Networks {
		if strings.EqualFold(strings.TrimSpace(ircCfg.Networks[i].Host), host) {
			return &ircCfg.Networks[i]
		}
	}
	return nil
}

func ircSetupLink(network, channel string) string {
	values := url.Values{}
	if network != "" {
		values.Set("network", network)
	}
	if channel != "" {
		values.Set("channel", channel)
	}
	if len(values) == 0 {
		return "/irc/setup"
	}
	return "/irc/setup?" + values.Encode()
}

func hasIRCAuthProblem(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "irc_registered_nick_required") ||
		strings.Contains(lower, "nickserv identify failed") ||
		strings.Contains(lower, "nickserv register failed") ||
		strings.Contains(lower, "sasl_auth_failed")
}

func parseIRCAuthContext(msg string) (string, string) {
	parts := strings.Split(msg, ";")
	var network, channel string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(strings.ToLower(p), "network=") {
			network = strings.TrimSpace(strings.TrimPrefix(p, p[:8]))
		}
		if strings.HasPrefix(strings.ToLower(p), "channel=") {
			channel = strings.TrimSpace(strings.TrimPrefix(p, p[:8]))
		}
	}
	if network == "" {
		if i := strings.Index(strings.ToLower(msg), "network="); i >= 0 {
			rest := msg[i+8:]
			for j, r := range rest {
				if r == ';' || r == ' ' {
					network = strings.TrimSpace(rest[:j])
					break
				}
			}
			if network == "" {
				network = strings.TrimSpace(rest)
			}
		}
	}
	if channel == "" {
		if i := strings.Index(strings.ToLower(msg), "channel="); i >= 0 {
			rest := msg[i+8:]
			for j, r := range rest {
				if r == ';' || r == ' ' {
					channel = strings.TrimSpace(rest[:j])
					break
				}
			}
			if channel == "" {
				channel = strings.TrimSpace(rest)
			}
		}
	}
	return network, channel
}

var pageTemplate = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f6f7f9;color:#1f2937}.wrap{max-width:980px;margin:24px auto;padding:0 16px}.card{background:#fff;border-radius:12px;box-shadow:0 1px 6px rgba(0,0,0,.08);padding:16px 18px;margin-bottom:16px}h1,h2{margin:0 0 12px}h1{font-size:24px}h2{font-size:20px}.grid{display:grid;grid-template-columns:220px 1fr;gap:10px 14px}.value{overflow-wrap:anywhere}.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:16px}.button,button{border:0;border-radius:10px;padding:10px 16px;text-decoration:none;cursor:pointer;font-size:14px}.primary{background:#111827;color:#fff}.secondary{background:#e5e7eb;color:#111827}.danger{background:#fee2e2;color:#991b1b}.status{font-weight:700}.note{font-size:13px;color:#6b7280}.warning{background:#fff7ed;color:#9a3412;padding:12px;border-radius:10px;margin-top:12px}pre{background:#111827;color:#e5e7eb;padding:14px;border-radius:10px;min-height:180px;max-height:360px;overflow:auto;white-space:pre-wrap}.pair-code{font-size:30px;font-weight:800;letter-spacing:.12em;text-align:center;padding:14px}.records{display:grid;gap:9px}.record{border:1px solid #e5e7eb;border-radius:10px;padding:10px 12px;display:flex;gap:10px;align-items:center;justify-content:space-between;min-width:0}.record-main{min-width:0;overflow-wrap:anywhere}.record-title{font-size:14px;font-weight:600}.record-meta{font-size:12px;color:#6b7280;margin-top:3px}.record-actions{flex-shrink:0}.record-actions button{padding:6px 10px}.record-actions{display:flex;gap:6px}.collapsible>summary{cursor:pointer;font-size:20px;font-weight:600;list-style-position:inside}.collapsible>summary:focus-visible{outline:2px solid #2563eb;outline-offset:5px}.collapsible[open]>summary{margin-bottom:14px}.collapsible .actions{margin:0 0 12px}.empty{font-size:13px;color:#6b7280}.hidden{display:none}@media(max-width:640px){.grid{grid-template-columns:1fr}.pair-code{font-size:24px}}
  </style>
</head>
<body data-archive-idle="{{call .T "archive.progress_idle"}}" data-archive-verifying="{{call .T "archive.progress_verifying"}}" data-archive-preflight="{{call .T "archive.progress_preflight"}}" data-archive-extracting="{{call .T "archive.progress_extracting"}}" data-archive-nested="{{call .T "archive.progress_nested"}}" data-archive-publishing="{{call .T "archive.progress_publishing"}}" data-archive-completed="{{call .T "archive.progress_completed"}}" data-history-empty="{{call .T "history.empty"}}" data-history-error="{{call .T "history.error"}}" data-history-attempts="{{call .T "history.attempts"}}" data-history-server-ack="{{call .T "history.server_ack"}}" data-history-retry="{{call .T "history.retry"}}" data-history-retrying="{{call .T "history.retrying"}}" data-history-retry-confirm="{{call .T "history.retry_confirm"}}" data-history-retry-failed="{{call .T "history.retry_failed"}}" data-history-retry-ok="{{call .T "history.retry_ok"}}" data-history-deleted="{{call .T "history.deleted"}}" data-history-remove="{{call .T "history.remove"}}" data-history-clear="{{call .T "history.clear"}}" data-history-remove-confirm="{{call .T "history.remove_confirm"}}" data-history-clear-confirm="{{call .T "history.clear_confirm"}}" data-history-hide-error="{{call .T "history.hide_failed"}}" data-history-hide-ok="{{call .T "history.hide_ok"}}" data-working="{{call .T "web.working"}}" data-ok="{{call .T "web.ok"}}" data-failed="{{call .T "web.failed"}}" data-connection-successful="{{call .T "notice.connection_successful"}}" data-log-copied="{{call .T "notice.log_copied"}}" data-log-copy-failed="{{call .T "notice.log_copy_failed"}}" data-code-copied="{{call .T "notice.pairing_code_copied"}}" data-copy-failed="{{call .T "notice.copy_failed"}}" data-yes="{{call .T "web.yes"}}" data-no="{{call .T "web.no"}}">
<div class="wrap">
  <div id="update-banner" class="card hidden" role="status"><strong id="update-message"></strong> <a id="update-download" class="button secondary" rel="noopener noreferrer" target="_blank">{{call .T "update.download"}}</a></div>
  <div class="card"><h1>{{.Title}}</h1><div id="action-result" class="status"></div></div>
  <div id="pairing-card" class="card">
    <h2>{{call .T "web.pairing"}}</h2>
    <div id="pairing_status" class="status"></div>
    <div id="pairing_code" class="pair-code">—</div>
    <div id="pairing_expiry" class="note"></div>
    <div class="actions"><button class="primary" onclick="copyCode()">{{call .T "web.copy_pairing_code"}}</button><a id="pairing-link" class="button secondary" target="_blank" rel="noopener">{{call .T "action.open_pairing_page"}}</a></div>
  </div>
  <div class="card">
    <h2>{{call .T "web.runtime"}}</h2>
    <div class="grid">
      <div>{{call .T "web.connected"}}</div><div id="connected" class="value">—</div>
      <div>{{call .T "web.running"}}</div><div id="running" class="value">—</div>
      <div>{{call .T "web.server"}}</div><div id="server_url" class="value">—</div>
      <div>{{call .T "web.client_id"}}</div><div id="client_id" class="value">—</div>
      <div>{{call .T "web.last_poll"}}</div><div id="last_poll" class="value">—</div>
      <div>{{call .T "web.current_job"}}</div><div id="current_job" class="value">—</div>
      <div>{{call .T "web.last_download"}}</div><div id="last_download" class="value">—</div>
      <div>{{call .T "web.last_error"}}</div><div id="last_error" class="value">—</div>
    </div>
    <div id="irc-auth-warning" class="warning {{if not .ShowIRCAuth}}hidden{{end}}" role="alert"><strong>{{call .T "web.irc_auth_required"}}</strong> <strong id="irc-auth-network">{{.IRCNetwork}}</strong> <span id="irc-auth-channel">{{.IRCChannel}}</span><p>{{call .T "web.irc_auth_instructions"}}</p><div class="actions"><a id="irc-auth-setup-link" class="button secondary" href="{{.IRCSetupLink}}">{{call .T "web.open_irc_settings"}}</a></div></div>
    <div class="actions">
      <button class="primary" onclick="action('/api/start')">{{call .T "web.start"}}</button>
      <button class="secondary" onclick="action('/api/stop')">{{call .T "web.stop"}}</button>
      <button class="secondary" onclick="action('/api/test')">{{call .T "action.test_connection"}}</button>
      <button class="danger" onclick="action('/api/repair')">{{call .T "web.repair"}}</button>
      <a class="button secondary" href="/settings">{{call .T "action.settings"}}</a>
      <a class="button secondary" href="/irc/setup">{{call .T "irc.title"}}</a>
    </div>
  </div>
  <div class="card"><h2>{{call .T "archive.progress_title"}}</h2><div id="archive-progress" class="value note">{{call .T "archive.progress_idle"}}</div></div>
  <details class="card collapsible" id="history-panel"><summary>{{call .T "history.downloads_title"}}</summary><div class="actions"><button class="secondary" id="history-clear" type="button" onclick="hideHistory(null,true)">{{call .T "history.clear"}}</button></div><p class="note">{{call .T "history.presentation_only"}}</p><div id="download-records" class="records"><span class="empty">{{call .T "history.loading"}}</span></div></details>
  <details class="card collapsible" id="archive-panel"><summary>{{call .T "history.archives_title"}}</summary><div id="archive-records" class="records"><span class="empty">{{call .T "history.loading"}}</span></div><p class="note">{{call .T "history.local_only"}}</p></details>
  <details class="card collapsible" id="log-panel"><summary>{{call .T "web.logs"}}</summary><pre id="logs"></pre><div class="actions"><button class="secondary" onclick="copyLogs()">{{call .T "action.copy_log"}}</button></div></details>
</div>
<script>
const text = document.body.dataset;
async function refreshUpdate(){
 try {
  const res=await fetch('/api/update',{cache:'no-store'});
  if(!res.ok)return;
  const status=await res.json();
  const banner=document.getElementById('update-banner');
  banner.classList.toggle('hidden',!status.available);
  if(status.available){
   document.getElementById('update-message').textContent='{{call .T "update.available_short"}} '+status.latest_version;
   const link=new URL(status.download_url);
   if(link.protocol==='https:' || (link.hostname==='localhost' && link.protocol==='http:')) document.getElementById('update-download').href=link.href;else banner.classList.add('hidden');
  }
 }catch(_){}
}
refreshUpdate();setInterval(refreshUpdate,60000);

let lastCode = '';
let logSelecting = false;
const logs = document.getElementById('logs');
logs.addEventListener('mousedown',()=>{logSelecting=true});
window.addEventListener('mouseup',()=>{logSelecting=false});
function setText(id,value){document.getElementById(id).textContent=value||'—'}
function updateView(s){
  lastCode=s.pairing_code||'';
  document.getElementById('pairing-card').classList.toggle('hidden',!!s.paired);
  setText('pairing_status',s.pairing_status);
  setText('pairing_code',s.pairing_code);
  setText('pairing_expiry',s.pairing_expiry);
  const link=document.getElementById('pairing-link');
  let url=s.pairing_url||'https://autofetch.de/clients/new';
  if(s.pairing_code){const u=new URL(url);u.searchParams.set('pairing_code',s.pairing_code);url=u.toString()}
  link.href=url;
  setText('connected',s.connected?text.yes:text.no);setText('running',s.running?text.yes:text.no);
  setText('server_url',s.server_url);setText('client_id',s.client_id);setText('last_poll',s.last_poll);setText('current_job',s.current_job);
  const lastDownload=[s.last_download,s.last_download_status].filter(Boolean).join(' · ');setText('last_download',lastDownload);setText('last_error',s.last_error);
  const authWarning=document.getElementById('irc-auth-warning');
  authWarning.classList.toggle('hidden',!s.irc_auth_required);
  if(s.irc_auth_required){
    document.getElementById('irc-auth-network').textContent=s.irc_auth_network||'';
    document.getElementById('irc-auth-channel').textContent=s.irc_auth_channel||'';
    const target=s.irc_auth_setup_link||'/irc/setup';
    if(target==='/irc/setup'||target.startsWith('/irc/setup?')) document.getElementById('irc-auth-setup-link').href=target;
  }
  if(!logSelecting&&!window.getSelection().toString())logs.textContent=(s.logs||[]).join('\n');
}
// All filenames, paths and server-derived values enter the DOM using textContent.
// Never interpolate history content into HTML or an event-handler attribute.
const MAX_HISTORY_ROWS=20;
function humanBytes(value){
  const n=Number(value);
  if(!Number.isFinite(n)||n<=0)return '';
  return (n/1048576).toFixed(1)+' MiB';
}
function humanDate(value){
  if(!value)return '';
  const d=new Date(value);
  return Number.isNaN(d.getTime())?'':d.toLocaleString(document.documentElement.lang||undefined);
}
function emptyRecords(node,label){
  node.replaceChildren();
  const empty=document.createElement('span');empty.className='empty';empty.textContent=label;
  node.appendChild(empty);
}
function addRecord(node,title,detail,action){
  const row=document.createElement('div');row.className='record';
  const main=document.createElement('div');main.className='record-main';
  const name=document.createElement('div');name.className='record-title';name.textContent=title||'—';
  const meta=document.createElement('div');meta.className='record-meta';meta.textContent=detail;
  main.append(name,meta);row.appendChild(main);
  if(action){const side=document.createElement('div');side.className='record-actions';side.appendChild(action);row.appendChild(side)}
  node.appendChild(row);
}
function renderDownloads(items){
  const node=document.getElementById('download-records');node.replaceChildren();
  if(!Array.isArray(items)||!items.length){emptyRecords(node,text.historyEmpty);return}
  for(const item of items.slice(0,MAX_HISTORY_ROWS)){
    const parts=[item.last_result||item.phase||'',humanBytes(item.bytes),text.historyAttempts+': '+(Number(item.attempt_count)||0),item.last_error_code||'',item.report_state||'',humanDate(item.updated_at)].filter(Boolean);
    let action=null;
    if(item.job_id){
      action=document.createElement('button');action.className='secondary';action.type='button';action.textContent=text.historyRemove;
      action.addEventListener('click',()=>hideHistory(item.job_id,false));
    }
    addRecord(node,item.filename||'',parts.join(' · '),action);
  }
}
function renderArchives(items){
  const node=document.getElementById('archive-records');node.replaceChildren();
  if(!Array.isArray(items)||!items.length){emptyRecords(node,text.historyEmpty);return}
  for(const item of items.slice(0,MAX_HISTORY_ROWS)){
    const name=String(item.source||'').split(/[/\\]/).pop();
    const detail=[item.status||'',item.error_code||'',(item.original_deleted?text.historyDeleted:''),humanDate(item.updated_at)].filter(Boolean).join(' · ');
    let action=null;
    if(item.status==='failed'&&item.job_id){
      action=document.createElement('button');action.className='secondary';action.type='button';action.textContent=text.historyRetry;
      action.addEventListener('click',()=>retryExtraction(item.job_id,action));
    }
    addRecord(node,name,detail,action);
  }
}
async function refreshDownloads(){
  try{
    const response=await fetch('/api/history',{cache:'no-store'});
    if(!response.ok)throw new Error('history unavailable');
    renderDownloads(await response.json());
  }catch(_){emptyRecords(document.getElementById('download-records'),text.historyError)}
}
async function hideHistory(jobID,all){
  if(!window.confirm(all?text.historyClearConfirm:text.historyRemoveConfirm))return;
  const clear=document.getElementById('history-clear');clear.disabled=true;
  try{
    const response=await fetch('/api/history/hide',{
      method:'POST',headers:{'Content-Type':'application/json','X-Autofetch-Local-Action':'history-hide'},
      body:JSON.stringify(all?{all:true}:{job_id:jobID})
    });
    if(!response.ok)throw new Error('history hide rejected');
    document.getElementById('action-result').textContent=text.historyHideOk;
  }catch(_){document.getElementById('action-result').textContent=text.historyHideError}
  finally{clear.disabled=false;await refreshDownloads()}
}
async function retryExtraction(jobID,button){
  if(!window.confirm(text.historyRetryConfirm))return;
  button.disabled=true;button.textContent=text.historyRetrying;
  try{
    const response=await fetch('/api/archives/retry',{
      method:'POST',headers:{'Content-Type':'application/json','X-Autofetch-Local-Action':'retry'},
      body:JSON.stringify({job_id:jobID})
    });
    if(!response.ok)throw new Error('retry rejected');
    document.getElementById('action-result').textContent=text.historyRetryOk;
  }catch(_){document.getElementById('action-result').textContent=text.historyRetryFailed}
  finally{button.disabled=false;button.textContent=text.historyRetry;await refreshArchives()}
}
async function refreshArchives(){
  const node=document.getElementById('archive-progress');
  try {
    const res=await fetch('/api/archives',{cache:'no-store'});
    if(!res.ok){node.textContent=text.archiveIdle;emptyRecords(document.getElementById('archive-records'),text.historyError);return}
    const items=await res.json();
    renderArchives(items);
    const active=Array.isArray(items)?items.find(x=>x.status==='extracting'):null;
    // Keep completed extractions visible briefly. Fast TAR/ZIP runs can finish
    // between the UI's two-second polling intervals.
    const recent=Array.isArray(items)?items.find(x=>x.status==='completed' && x.updated_at &&
      Date.now()-Date.parse(x.updated_at)>=0 && Date.now()-Date.parse(x.updated_at)<120000):null;
    const current=active||recent;
    if(!current){node.textContent=text.archiveIdle;return}
    const phase=(text['archive'+(current.phase||'verifying').replace(/^./,c=>c.toUpperCase())]||current.phase||'');
    const filename=(current.source||'').replace(/.*[/\\]/,'');
    const bytes=Number(current.progress_bytes)||0;
    const total=Number(current.progress_total_bytes)||0;
    const format=n=>(n/1048576).toFixed(1)+' MiB';
    const done=current.status==='completed';
    const label=total>0?format(bytes)+' / '+format(total)+((current.phase==='extracting'||done)?' ('+(done?100:Math.min(100,Math.floor(bytes*100/total)))+'%)':''):format(bytes);
    node.textContent=filename+' · '+phase+' · '+label;
  } catch(_){node.textContent=text.archiveIdle;emptyRecords(document.getElementById('archive-records'),text.historyError)}
}
async function refresh(){try{const res=await fetch('/api/status');updateView(await res.json())}catch(_){} await refreshArchives()}
async function action(path){const out=document.getElementById('action-result');out.textContent=text.working;const res=await fetch(path,{method:'POST'});let msg=res.ok?text.ok:text.failed;try{const data=await res.json();if(data&&data.error)msg=data.error;if(data&&data.ok&&path==='/api/test')msg=text.connectionSuccessful}catch(_){}out.textContent=msg;refresh()}
async function copyLogs(){try{await navigator.clipboard.writeText(logs.textContent||'');document.getElementById('action-result').textContent=text.logCopied}catch(_){document.getElementById('action-result').textContent=text.logCopyFailed}}
async function copyCode(){if(!lastCode)return;try{await navigator.clipboard.writeText(lastCode);document.getElementById('action-result').textContent=text.codeCopied}catch(_){document.getElementById('action-result').textContent=text.copyFailed}}
refresh();refreshDownloads();setInterval(refresh,2000);setInterval(refreshDownloads,12000);
</script>
</body></html>`))

var settingsTemplate = template.Must(template.New("settings").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title><style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f6f7f9;color:#1f2937}
.wrap{max-width:900px;margin:24px auto;padding:0 16px}.card{background:#fff;border-radius:12px;box-shadow:0 1px 6px rgba(0,0,0,.08);padding:16px 18px}
label{display:block;font-size:14px;font-weight:600;margin:12px 0 6px}
input{width:100%;box-sizing:border-box;padding:10px 12px;border:1px solid #d1d5db;border-radius:10px}
.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:22px}.button,button{border:0;border-radius:10px;padding:10px 16px;text-decoration:none;cursor:pointer;font-size:14px}
.primary{background:#111827;color:#fff}.secondary{background:#e5e7eb;color:#111827}
.note{font-size:13px;color:#6b7280;margin-top:10px}.error{background:#fef2f2;color:#991b1b;padding:10px 12px;border-radius:10px;margin-bottom:12px}
.login-info{border:1px solid #94a3b8;border-left:5px solid #334155;background:#f8fafc;border-radius:10px;padding:12px 16px;margin:16px 0}
.login-info p{margin:8px 0 0}.login-user{font-size:17px;font-weight:650}.login-user code{background:#e2e8f0;padding:3px 9px;border-radius:5px;color:#0f172a}
</style></head>
<body><div class="wrap"><div class="card"><h1>{{.Title}}</h1><p>{{call .T "settings.general_intro"}}</p>
{{if .Error}}<div class="error" role="alert">{{.Error}}</div>{{end}}
<form method="post" action="/settings">
<label for="download-dir">{{call .T "settings.download_folder"}}</label><input id="download-dir" name="download_dir" value="{{.DownloadDir}}">
<p class="note">{{call .T "settings.irc_separate_hint"}}</p>
{{if .ShowWebUIPasswordFields}}
<hr><h2>{{call .T "webui.password_heading"}}</h2>
{{if .WebUIPasswordEnabled}}<p class="note">{{call .T "webui.password_active_hint"}}</p>{{else}}<p class="note">{{call .T "webui.password_setup_hint"}}</p>{{end}}
<div class="login-info" role="note"><strong>{{call .T "webui.login_heading"}}</strong><p class="login-user">{{call .T "webui.login_username"}} <code>autofetch</code></p><p>{{call .T "webui.login_after_save"}}</p></div>
<label for="webui-password">{{call .T "webui.new_password"}}</label>
<input id="webui-password" name="webui_password" type="password" minlength="12" maxlength="256" autocomplete="new-password" placeholder="{{call .T "webui.leave_unchanged"}}">
<label for="webui-password-repeat">{{call .T "webui.repeat_password"}}</label>
<input id="webui-password-repeat" name="webui_password_repeat" type="password" minlength="12" maxlength="256" autocomplete="new-password" placeholder="{{call .T "webui.leave_unchanged"}}">
<p class="note">{{call .T "webui.password_http_warning"}}</p>
{{else if .WebUIPasswordEnabled}}<hr><h2>{{call .T "webui.password_heading"}}</h2><p class="note">{{call .T "webui.password_active"}}</p>{{end}}
<div class="actions"><button class="primary" type="submit">{{call .T "action.save"}}</button><a class="button secondary" href="/irc/setup">{{call .T "irc.title"}}</a><a class="button secondary" href="/">{{call .T "action.back"}}</a></div>
</form></div></div></body></html>`))

var ircSetupTemplate = template.Must(template.New("ircsetup").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title><style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f6f7f9;color:#1f2937}
.wrap{max-width:1040px;margin:24px auto;padding:0 16px}.card{background:#fff;border-radius:12px;box-shadow:0 1px 6px rgba(0,0,0,.08);padding:20px}
.layout{display:grid;grid-template-columns:245px minmax(0,1fr);gap:24px}.netlist{display:flex;flex-direction:column;gap:8px}
.netitem{display:block;padding:10px 12px;border:1px solid #d1d5db;border-radius:10px;text-decoration:none;color:#111827;background:#fff;overflow-wrap:anywhere}
.netitem.active{border-color:#111827;background:#f9fafb}.netitem small{display:block;color:#64748b;margin-top:4px}
.step{border:1px solid #e2e8f0;border-radius:12px;padding:18px;margin-bottom:16px}.step h2{font-size:19px;margin:0 0 8px}.step-number{font-size:13px;color:#475569;font-weight:700;margin-bottom:6px}
label{display:block;font-size:14px;font-weight:600;margin:14px 0 6px}input{width:100%;box-sizing:border-box;padding:10px 12px;border:1px solid #cbd5e1;border-radius:9px;font:inherit}
input[type=checkbox]{width:auto;margin-right:8px}.checkbox-label{display:flex;align-items:flex-start;gap:5px;line-height:1.5;font-weight:400}.checkbox-label input{flex:none;margin-top:4px}
.row{display:grid;grid-template-columns:1fr 1fr;gap:14px}.nick-row{display:flex;align-items:end;gap:8px}.nick-row .nick-field{flex:1;min-width:0}
.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}.button,button{border:0;border-radius:9px;padding:10px 14px;text-decoration:none;cursor:pointer;font:inherit;font-size:14px;line-height:1.3}
.primary{background:#111827;color:white}.secondary{background:#e5e7eb;color:#111827}.note{font-size:13px;color:#64748b;line-height:1.5;margin:8px 0}.status{border-left:3px solid #64748b;background:#f8fafc;padding:10px 12px;margin:12px 0;border-radius:6px}
.error{background:#fef2f2;color:#991b1b;padding:12px;border-radius:9px;margin-bottom:14px;overflow-wrap:anywhere}
details{border-top:1px solid #e2e8f0;padding:13px 0;margin-top:14px}summary{font-weight:600;cursor:pointer}details[open] summary{margin-bottom:12px}
.registration-live{margin-top:16px;padding:14px;border:1px solid #cbd5e1;background:#f8fafc;border-radius:10px}
.registration-live[hidden]{display:none}
.registration-live ol{padding-left:21px;margin:10px 0 0;max-height:280px;overflow:auto;font-size:14px;line-height:1.7;overflow-wrap:anywhere}
.registration-live .live-warning{font-size:12px;color:#64748b}
.registration-live .live-error{color:#991b1b;font-weight:600}
.registration-live .live-remaining{font-weight:600;color:#334155}
.observed-channels{display:flex;flex-wrap:wrap;gap:8px;list-style:none;padding:0}.observed-channels li{background:#f1f5f9;padding:6px 10px;border-radius:8px}
.bottom-links{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px;align-items:center}.bottom-links .button{display:inline-flex;align-items:center;justify-content:center;min-height:40px;box-sizing:border-box}
@media(max-width:760px){.layout,.row{grid-template-columns:1fr}.card{padding:14px}.nick-row{align-items:stretch;flex-direction:column}.nick-row button{align-self:flex-start}.step{padding:14px}}
</style></head>
<body><div class="wrap"><div class="card"><h1>{{.Title}}</h1><p class="note">{{call .T "irc.setup_intro_simple"}}</p><p class="note">{{call .T "irc.data_privacy"}}</p>{{if .Error}}<div class="error" role="alert">{{.Error}}</div>{{end}}
<div class="layout"><aside><h2>{{call .T "irc.networks"}}</h2><p class="note">{{call .T "irc.web_networks_hint"}}</p><div class="netlist">{{range .Networks}}<a class="netitem {{if eq .Host $.Network.Host}}active{{end}}" href="/irc/setup?network={{.Host}}"><strong>{{if .Name}}{{.Name}}{{else}}{{.Host}}{{end}}</strong><small>{{.Host}}:{{.Port}}</small></a>{{end}}</div></aside>
<main>
<section class="step" aria-labelledby="nick-step"><div class="step-number">{{call .T "irc.step_one"}}</div><h2 id="nick-step">{{call .T "irc.choose_nick_heading"}}</h2><p class="note">{{call .T "irc.choose_nick_hint"}}</p>
<form method="post" action="/irc/setup">
<div class="nick-row"><div class="nick-field"><label for="network-nick">{{call .T "irc.nick"}}</label><input id="network-nick" name="nick" value="{{.Network.Nick}}" autocomplete="off"></div><button class="secondary" type="submit" name="generate_nick" value="1">{{call .T "action.generate_nick"}}</button></div>
{{if .Network.NickServ.RegistrationPending}}<p class="note">{{call .T "irc.nick_change_pending_warning"}}</p>{{end}}
<details><summary>{{call .T "irc.advanced_connection_heading"}}</summary>
<div class="row"><div><label>{{call .T "irc.display_name"}}</label><input name="name" value="{{.Network.Name}}"></div><div><label>{{call .T "irc.host"}}</label><input name="host" value="{{.Network.Host}}" required></div></div>
<div class="row"><div><label>{{call .T "irc.port"}}</label><input name="port" value="{{.Network.Port}}" inputmode="numeric"></div><div><label class="checkbox-label"><input type="checkbox" name="tls" {{if .Network.TLS}}checked{{end}}>{{call .T "irc.use_tls"}}</label></div></div>
<div class="row"><div><label>{{call .T "irc.username"}}</label><input name="username" value="{{.Network.Username}}"></div><div><label>{{call .T "irc.realname"}}</label><input name="realname" value="{{.Network.Realname}}"></div></div>
<details><summary>{{call .T "irc.existing_credentials_heading"}}</summary>
<p class="note">{{call .T "irc.existing_credentials_hint"}}</p><p class="note">{{call .T "irc.nickserv_setup_hint"}}</p>
<label class="checkbox-label"><input type="checkbox" name="nickserv_enabled" {{if .Network.NickServ.Enabled}}checked{{end}}>{{call .T "irc.use_nickserv"}}</label>
<div class="row"><div><label>{{call .T "irc.nickserv_command"}}</label><input name="nickserv_command" value="{{.Network.NickServ.Command}}"></div><div><label>{{call .T "irc.nickserv_password"}}</label><input type="password" name="nickserv_password" placeholder="{{call .T "irc.leave_unchanged"}}" autocomplete="new-password"></div></div>
<label class="checkbox-label"><input type="checkbox" name="clear_nickserv_password">{{call .T "action.delete_nickserv_password"}}</label>
<h3>SASL</h3><label class="checkbox-label"><input type="checkbox" name="sasl_enabled" {{if .Network.SASL.Enabled}}checked{{end}}>{{call .T "irc.use_sasl"}}</label>
<div class="row"><div><label>{{call .T "irc.sasl_username"}}</label><input name="sasl_username" placeholder="{{call .T "irc.leave_unchanged"}}"></div><div><label>{{call .T "irc.sasl_password"}}</label><input type="password" name="sasl_password" placeholder="{{call .T "irc.leave_unchanged"}}" autocomplete="new-password"></div></div>
<label class="checkbox-label"><input type="checkbox" name="clear_sasl_username">{{call .T "action.delete_sasl_access"}}</label>
<label class="checkbox-label"><input type="checkbox" name="clear_sasl_password">{{call .T "action.delete_sasl_access"}}</label>
</details>
<details><summary>{{call .T "irc.global_settings"}}</summary>
<label>{{call .T "irc.default_nick"}}</label><input name="default_nick" value="{{.DefaultNick}}">
<h3>Reverse DCC</h3><label class="checkbox-label"><input type="checkbox" name="reverse_dcc_enabled" {{if .ReverseDCCEnabled}}checked{{end}}>{{call .T "irc.accept_reverse_dcc"}}</label>
<div class="row"><div><label>{{call .T "irc.port_from"}}</label><input name="reverse_dcc_port_min" value="{{.ReverseDCCPortMin}}"></div><div><label>{{call .T "irc.port_to"}}</label><input name="reverse_dcc_port_max" value="{{.ReverseDCCPortMax}}"></div></div><p class="note">{{call .T "irc.reverse_dcc_web_hint"}}</p>
</details>
</details>
<div class="actions"><button class="primary" type="submit">{{call .T "irc.save_identity_button"}}</button></div>
</form></section>
<section class="step" aria-labelledby="register-step"><div class="step-number">{{call .T "irc.step_two"}}</div><h2 id="register-step">{{call .T "irc.manual_heading"}}</h2><p class="note">{{call .T "irc.auto_register_rules_hint"}}</p>
<div class="registration-live" id="nickserv-live" role="status" aria-live="polite" aria-atomic="false" hidden
 data-starting="{{call .T "irc.live_starting"}}" data-connecting="{{call .T "irc.live_connecting"}}"
 data-connected="{{call .T "irc.live_connected"}}" data-ready="{{call .T "irc.live_ready"}}"
 data-sent="{{call .T "irc.live_sent"}}" data-waiting="{{call .T "irc.live_waiting"}}"
 data-retry="{{call .T "irc.live_retry"}}" data-identified="{{call .T "irc.live_identified"}}"
 data-done="{{call .T "irc.live_done"}}" data-error="{{call .T "irc.live_error"}}"
 data-network-reply="{{call .T "irc.live_network_reply"}}" data-remaining="{{call .T "irc.live_remaining"}}"
 data-seconds="{{call .T "irc.live_seconds"}}" data-connection-error="{{call .T "irc.live_connection_error"}}">
<strong>{{call .T "irc.live_heading"}}</strong><p class="note">{{call .T "irc.live_intro"}}</p>
<ol id="nickserv-live-events"></ol><p id="nickserv-live-wait" class="live-remaining" hidden></p>
<p class="live-warning">{{call .T "irc.live_warning"}}</p>
<a id="nickserv-live-reload" href="/irc/setup?network={{urlquery .Network.Host}}" hidden>{{call .T "irc.live_reload"}}</a>
</div>
{{if eq .Notice "identified"}}<p class="status" role="status">{{call .T "irc.manual_success"}}</p>{{end}}
{{if eq .Notice "unregistered"}}<p class="status" role="status">{{call .T "irc.manual_unregistered_notice"}}</p>{{end}}
{{if eq .Notice "reset"}}<p class="status" role="status">{{call .T "irc.manual_reset_notice"}}</p>{{end}}
{{if eq .Notice "pending"}}<p class="status" role="status">{{call .T "irc.manual_pending_notice"}}</p>{{end}}
{{if not .Network.Host}}<p class="note">{{call .T "irc.save_first_hint"}}</p>
{{else if not .Network.Nick}}<p class="note">{{call .T "irc.save_first_hint"}}</p>
{{else if .Network.NickServ.RegistrationPending}}
<p class="status" role="status">{{call .T "irc.manual_pending"}}</p>
{{if .Network.NickServ.Password}}
<p class="note">{{call .T "irc.pending_next_hint"}}</p>
<form method="post" action="/irc/setup/verify"><input type="hidden" name="host" value="{{.Network.Host}}">
<label for="confirm-code">{{call .T "irc.manual_code"}}</label><input id="confirm-code" name="confirmation_code" autocomplete="off" maxlength="128"><p class="note">{{call .T "irc.manual_link"}}</p>
<div class="actions"><button type="submit" class="primary">{{call .T "irc.manual_verify"}}</button></div></form>
{{else}}<p class="note">{{call .T "irc.pending_missing_credentials"}}</p>{{end}}
<details><summary>{{call .T "irc.registration_problems_heading"}}</summary>
{{if .Network.NickServ.Password}}<form method="post" action="/irc/setup/register-retry"><input type="hidden" name="host" value="{{.Network.Host}}">
<p class="note">{{call .T "irc.manual_retry_hint"}}</p><label for="retry-email">{{call .T "irc.manual_email"}}</label><input id="retry-email" type="email" name="registration_email" value="{{.Network.RegistrationEmail}}" required autocomplete="email" maxlength="128">
<label class="checkbox-label"><input type="checkbox" name="network_rules_ack" value="yes" required>{{call .T "irc.manual_ack"}}</label>
<div class="actions"><button type="submit" class="secondary">{{call .T "irc.manual_retry_button"}}</button></div></form>{{end}}
<form method="post" action="/irc/setup/reset"><input type="hidden" name="host" value="{{.Network.Host}}"><p class="note">{{call .T "irc.reset_warning"}}</p><button type="submit" class="secondary">{{call .T "irc.manual_reset"}}</button></form>
</details>
{{else if and .Network.NickServ.Enabled .Network.NickServ.Password}}
<p class="status" role="status">{{call .T "irc.manual_active"}}</p><p class="note">{{call .T "irc.setup_done_hint"}}</p>
{{else if .Network.NickServ.Password}}
<p class="status">{{call .T "irc.credentials_present_hint"}}</p>
{{else}}
{{if .Network.NickServ.RequiresRegistration}}<p class="status">{{call .T "irc.manual_required"}}</p>{{end}}
<p class="note">{{call .T "irc.register_new_intro"}}</p>
<form method="post" action="/irc/setup/register"><input type="hidden" name="host" value="{{.Network.Host}}">
<label for="registration-email">{{call .T "irc.manual_email"}}</label><input id="registration-email" type="email" name="registration_email" value="{{.Network.RegistrationEmail}}" required autocomplete="email" maxlength="128" placeholder="name@example.org">
<p class="note">{{call .T "irc.registration_email_hint"}}</p>
<label class="checkbox-label"><input type="checkbox" name="network_rules_ack" value="yes" required>{{call .T "irc.manual_ack"}}</label>
<div class="actions"><button type="submit" class="primary">{{call .T "irc.manual_register"}}</button></div></form>
<p class="note">{{call .T "irc.manual_rules"}}</p>
{{if .Network.NickServ.RequiresRegistration}}<details><summary>{{call .T "irc.no_nickserv_heading"}}</summary><form method="post" action="/irc/setup/unregistered"><input type="hidden" name="host" value="{{.Network.Host}}"><p class="note">{{call .T "irc.manual_unregistered_hint"}}</p><button type="submit" class="secondary">{{call .T "irc.manual_unregistered_button"}}</button></form></details>{{end}}
{{end}}
</section>
<details><summary>{{call .T "irc.job_channels_heading"}}{{if .Network.Channels}} ({{len .Network.Channels}}){{end}}</summary>
<p class="note">{{call .T "irc.job_channels_hint"}}</p>
{{if .Network.Channels}}<ul class="observed-channels">{{range .Network.Channels}}<li><code>{{.}}</code></li>{{end}}</ul>{{else}}<p class="note">{{call .T "irc.job_channels_empty"}}</p>{{end}}
{{if .RequestedChannel}}<p class="note">{{call .T "irc.job_channel_context"}} <code>{{.RequestedChannel}}</code></p>{{end}}</details>
<nav class="bottom-links" aria-label="{{call .T "irc.title"}}"><a class="button secondary" href="/settings">{{call .T "settings.title"}}</a><a class="button secondary" href="/">{{call .T "action.back"}}</a></nav>
</main></div></div></div>
<script>
(function(){
  const live=document.getElementById('nickserv-live');
  const events=document.getElementById('nickserv-live-events');
  const wait=document.getElementById('nickserv-live-wait');
  const reload=document.getElementById('nickserv-live-reload');
  if(!live || !events)return;
  let busy=false, countdown=null;
  function stopCountdown(){if(countdown!==null){clearInterval(countdown);countdown=null;}wait.hidden=true;}
  function append(text,kind){
    const li=document.createElement('li');
    li.textContent=text;
    if(kind==='error')li.className='live-error';
    events.appendChild(li);
    while(events.children.length>30)events.removeChild(events.firstChild);
    events.scrollTop=events.scrollHeight;
  }
  function handle(e){
    if(!e || typeof e.kind!=='string')return;
    if(e.kind==='notice'){
      append(live.dataset.networkReply+String(e.message||'').slice(0,240),'notice');
    }else if(e.kind==='waiting'){
      append(live.dataset.waiting,'waiting');
      stopCountdown();
      const initial=Math.min(90,Math.max(0,Number(e.wait_seconds)||0));
      const until=Date.now()+initial*1000;
      const update=function(){
        const remaining=Math.max(0,Math.ceil((until-Date.now())/1000));
        wait.textContent=live.dataset.remaining+' '+remaining+' '+live.dataset.seconds;
        wait.hidden=remaining===0;
        if(remaining===0)stopCountdown();
      };
      update();
      if(initial>0)countdown=setInterval(update,1000);
    }else if(e.kind==='error'){
      stopCountdown();
      append(live.dataset.error,'error');
      if(e.message)append(String(e.message).slice(0,500),'error');
      reload.hidden=false;
    }else if(e.kind==='done'){
      stopCountdown();
      append(live.dataset.done,'done');
      if(typeof e.location==='string' && e.location.startsWith('/irc/setup?')){
        window.location.assign(e.location);
      }else{reload.hidden=false;}
    }else if(Object.prototype.hasOwnProperty.call(live.dataset,e.kind)){
      append(live.dataset[e.kind],e.kind);
    }
  }
  ['/irc/setup/register','/irc/setup/register-retry','/irc/setup/verify'].forEach(function(path){
    document.querySelectorAll('form[action="'+path+'"]').forEach(function(form){
      form.addEventListener('submit',async function(ev){
        if(busy){ev.preventDefault();return;}
        if(!window.fetch || !window.ReadableStream)return;
        ev.preventDefault();busy=true;
        live.hidden=false;reload.hidden=true;events.textContent='';stopCountdown();
        live.scrollIntoView({behavior:'smooth',block:'nearest'});
        const buttons=document.querySelectorAll('#register-step button');
        buttons.forEach(function(button){button.disabled=true;});
        try{
          const resp=await fetch(form.action,{
            method:'POST',credentials:'same-origin',cache:'no-store',
            headers:{'Accept':'application/x-ndjson'},
            body:new URLSearchParams(new FormData(form))
          });
          if(!resp.ok){
            append(live.dataset.error,'error');
            append((await resp.text()).slice(0,240),'error');
            reload.hidden=false;
            return;
          }
          if(!resp.body)throw new Error('HTTP stream unavailable');
          const reader=resp.body.getReader(),decoder=new TextDecoder();
          let buffer='',complete=false;
          function parse(line){
            if(!line.trim())return;
            const event=JSON.parse(line);
            if(event.kind==='done'||event.kind==='error')complete=true;
            handle(event);
          }
          for(;;){
            const next=await reader.read();
            if(next.done)break;
            buffer+=decoder.decode(next.value,{stream:true});
            if(buffer.length>32768)throw new Error('Unexpectedly long response');
            let i;
            while((i=buffer.indexOf('\n'))>=0){const line=buffer.slice(0,i);buffer=buffer.slice(i+1);parse(line);}
          }
          if(buffer.trim())parse(buffer);
          if(!complete)throw new Error(live.dataset.connectionError);
        }catch(err){
          stopCountdown();
          append(live.dataset.connectionError,'error');
          reload.hidden=false;
        }finally{
          stopCountdown();busy=false;
          buttons.forEach(function(button){button.disabled=false;});
        }
      });
    });
  });
})();
</script></body></html>`))
