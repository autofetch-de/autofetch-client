package webui

import (
	"context"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/config"
	internalirc "github.com/autofetch-de/autofetch-client/internal/irc"
	"github.com/autofetch-de/autofetch-client/internal/observe"
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
	state   *observe.State
	service service
	http    *http.Server
	addr    string
}

func New(addr string, state *observe.State, service service) *Server {
	mux := http.NewServeMux()
	s := &Server{state: state, service: service, addr: addr}
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/start", s.handleStart)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/test", s.handleTest)
	mux.HandleFunc("/api/repair", s.handleRepair)
	mux.HandleFunc("/irc/setup", s.handleIRCSetup)
	mux.HandleFunc("/settings", s.handleSettings)
	s.http = &http.Server{Addr: addr, Handler: mux}
	return s
}

func (s *Server) Start() error {
	go func() {
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
	snap := s.state.Snapshot()
	network, channel := parseIRCAuthContext(snap.LastError)
	setupLink := "/irc/setup"
	if network != "" || channel != "" {
		v := url.Values{}
		if network != "" {
			v.Set("network", network)
		}
		if channel != "" {
			v.Set("channel", channel)
		}
		setupLink += "?" + v.Encode()
	}
	data := map[string]any{"Title": "autofetch status", "Snapshot": snap, "ShowIRCAuth": strings.Contains(strings.ToLower(snap.LastError), "irc_registered_nick_required"), "IRCSetupLink": setupLink, "IRCNetwork": network, "IRCChannel": channel}
	_ = pageTemplate.Execute(w, data)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.state.Snapshot())
}

func writeJSONError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.service.Start(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
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
		writeJSONError(w, http.StatusInternalServerError, err)
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
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
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
		writeJSONError(w, http.StatusInternalServerError, err)
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	downloadDir := strings.TrimSpace(r.FormValue("download_dir"))
	if downloadDir == "" {
		downloadDir = strings.TrimSpace(s.service.DownloadDir())
	}
	ircCfg := s.service.IRCConfig().WithDefaults()
	if err := s.service.UpdateLocalSettings(downloadDir, ircCfg, ircCfg.AutoRegister, ircCfg.RegistrationEmail); err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, errMsg string) {
	data := map[string]any{
		"Title":       "Einstellungen",
		"Error":       errMsg,
		"DownloadDir": s.service.DownloadDir(),
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
	ircCfg.DefaultNick = strings.TrimSpace(r.FormValue("default_nick"))
	ircCfg.AutoRegister = r.FormValue("auto_register") != ""
	ircCfg.RegistrationEmail = strings.TrimSpace(r.FormValue("registration_email"))
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
	channel := strings.TrimSpace(r.FormValue("channel"))
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
	if channel != "" {
		n.Channels = []string{channel}
	}
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
		n.NickServ.Password = existing.NickServ.Password
		n.SASL.Username = existing.SASL.Username
		n.SASL.Password = existing.SASL.Password
	}
	if r.FormValue("clear_nickserv_password") != "" {
		n.NickServ.Password = ""
	} else if pw := r.FormValue("nickserv_password"); pw != "" {
		n.NickServ.Password = pw
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
	if channel != "" {
		v.Set("channel", channel)
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
	if channelParam != "" {
		selected.Channels = []string{channelParam}
	}

	channel := ""
	if len(selected.Channels) > 0 {
		channel = selected.Channels[0]
	}
	data := map[string]any{"Title": "IRC-Einstellungen", "Error": errMsg, "DefaultNick": ircCfg.DefaultNick, "AutoRegister": ircCfg.AutoRegister, "RegistrationEmail": ircCfg.RegistrationEmail, "ReverseDCCEnabled": ircCfg.ReverseDCCEnabled, "ReverseDCCPortMin": ircCfg.ReverseDCCPortMin, "ReverseDCCPortMax": ircCfg.ReverseDCCPortMax, "Network": selected, "Channel": channel, "Networks": ircCfg.Networks}
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
<html lang="de">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 0; background: #f6f7f9; color: #1f2937; }
    .wrap { max-width: 980px; margin: 24px auto; padding: 0 16px; }
    .card { background: white; border-radius: 12px; box-shadow: 0 1px 6px rgba(0,0,0,.08); padding: 16px 18px; margin-bottom: 16px; }
    h1, h2 { margin: 0 0 12px; }
    h1 { font-size: 24px; }
    h2 { font-size: 20px; }
    .grid { display: grid; grid-template-columns: 220px 1fr; gap: 10px 14px; }
    .label { color: #6b7280; }
    .status { font-weight: 600; }
    .online { color: #047857; }
    .offline { color: #b91c1c; }
    .buttons { display: flex; gap: 10px; flex-wrap: wrap; }
    button, a.button { border: 0; border-radius: 10px; padding: 10px 16px; cursor: pointer; font-size: 14px; text-decoration: none; display: inline-block; }
    .primary { background: #111827; color: white; }
    .secondary { background: #e5e7eb; color: #111827; }
    .code { display: inline-flex; align-items: center; gap: 10px; padding: 12px 14px; background: #111827; color: #fff; border-radius: 12px; font-weight: 700; font-size: 28px; letter-spacing: 2px; }
    .muted { color: #6b7280; }
    pre { white-space: pre-wrap; word-break: break-word; background: #111827; color: #e5e7eb; padding: 12px; border-radius: 10px; min-height: 220px; overflow: auto; }
    .hint { color: #6b7280; font-size: 13px; margin-top: 8px; }
    .hidden { display: none; }
  </style>
</head>
<body>
<div class="wrap">
  <div class="card">
    <h1>autofetch client</h1>
    <div class="buttons">
      <button class="primary" onclick="action('/api/start', 'Start angefordert')">Start</button>
      <a class="button secondary" href="/settings">Einstellungen</a>
      <button class="secondary" onclick="action('/api/stop', 'Stop angefordert')">Stop</button>
      <button class="secondary" onclick="action('/api/test')">Verbindung testen</button>
      <button class="secondary" onclick="action('/api/repair', 'Neues Pairing gestartet')">Neu koppeln</button>
    </div>
    <div class="hint" id="action-result"></div>
  </div>

  <div class="card" id="pairing-card">
    <h2>Client koppeln</h2>
    <p>Dieser Client ist noch nicht gekoppelt. Öffne bitte diese Seite:</p>
    <p><a id="pairing-link" target="_blank" rel="noreferrer"></a></p>
    <p>Gib dort anschließend diesen Kopplungscode ein:</p>
    <div class="code"><span id="pairing-code">-</span><button class="secondary" onclick="copyCode()">Kopieren</button></div>
    <p class="hint">Status: <span id="pairing-status">-</span></p>
    <p class="hint">Ablauf: <span id="pairing-expiry">-</span></p>
  </div>

  <div class="card hidden" id="status-card">
    <div class="grid">
      <div class="label">Status</div><div id="connected"></div>
      <div class="label">Server-URL</div><div id="server_url"></div>
      <div class="label">Client-ID</div><div id="client_id"></div>
      <div class="label">Letzter Poll</div><div id="last_poll"></div>
      <div class="label">Aktueller Job</div><div id="current_job"></div>
      <div class="label">Letzter Download</div><div id="last_download"></div>
      <div class="label">Letzte Fehlermeldung</div><div id="last_error"></div>
    </div>
  </div>

  <div class="card">
    <div style="display:flex;align-items:center;justify-content:space-between;gap:10px;margin-bottom:8px;"><div class="label">Log-Auszug</div><button class="secondary" onclick="copyLogs()">Log kopieren</button></div>
    <pre id="logs" onmousedown="logSelecting=true" onmouseup="setTimeout(() => logSelecting=false, 250)" onmouseleave="setTimeout(() => logSelecting=false, 250)"></pre>
  </div>
</div>
<script>
let lastCode = '';
let logSelecting = false;

function setText(id, value) {
  document.getElementById(id).textContent = value || '-';
}

function updateView(s) {
  const paired = !!s.paired;
  document.getElementById('pairing-card').classList.toggle('hidden', paired);
  document.getElementById('status-card').classList.toggle('hidden', !paired);

  setText('pairing-code', s.pairing_code);
  setText('pairing-status', s.pairing_status);
  setText('pairing-expiry', s.pairing_expiry);
  const link = document.getElementById('pairing-link');
  link.textContent = s.pairing_url || '-';
  link.href = s.pairing_url || '#';
  lastCode = s.pairing_code || '';

  const c = document.getElementById('connected');
  c.textContent = s.connected ? 'verbunden' : (s.running ? 'offline' : 'gestoppt');
  c.className = 'status ' + (s.connected ? 'online' : 'offline');
  setText('server_url', s.server_url);
  setText('client_id', s.client_id);
  setText('last_poll', s.last_poll);
  setText('current_job', s.current_job);
  setText('last_download', s.last_download);
  setText('last_error', s.last_error);
  if (!logSelecting && !window.getSelection().toString()) {
    document.getElementById('logs').textContent = (s.logs || []).join('\n');
  }
}

async function refresh() {
  const res = await fetch('/api/status');
  const s = await res.json();
  updateView(s);
}

async function action(path, successMsg) {
  const out = document.getElementById('action-result');
  out.textContent = 'arbeite...';
  const res = await fetch(path, { method: 'POST' });
  let msg = successMsg || (res.ok ? 'ok' : 'fehler');
  try {
    const data = await res.json();
    if (data && data.error) msg = data.error;
    if (data && data.ok && path === '/api/test') msg = 'Verbindung erfolgreich';
  } catch (_) {}
  out.textContent = msg;
  refresh();
}

async function copyLogs() {
  const text = document.getElementById('logs').textContent || '';
  try {
    await navigator.clipboard.writeText(text);
    document.getElementById('action-result').textContent = 'Log kopiert';
  } catch (_) {
    document.getElementById('action-result').textContent = 'Log konnte nicht kopiert werden';
  }
}

async function copyCode() {
  if (!lastCode) return;
  try {
    await navigator.clipboard.writeText(lastCode);
    document.getElementById('action-result').textContent = 'Kopplungscode kopiert';
  } catch (_) {
    document.getElementById('action-result').textContent = 'Kopieren fehlgeschlagen';
  }
}

refresh();
setInterval(refresh, 2000);
</script>
</body>
</html>`))

var settingsTemplate = template.Must(template.New("settings").Parse(`<!doctype html>
<html lang="de">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title><style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f6f7f9;color:#1f2937}.wrap{max-width:900px;margin:24px auto;padding:0 16px}.card{background:#fff;border-radius:12px;box-shadow:0 1px 6px rgba(0,0,0,.08);padding:16px 18px;margin-bottom:16px}label{display:block;font-size:14px;font-weight:600;margin:12px 0 6px}input{width:100%;box-sizing:border-box;padding:10px 12px;border:1px solid #d1d5db;border-radius:10px}input[type=checkbox]{width:auto;margin-right:8px}.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}.button,button{border:0;border-radius:10px;padding:10px 16px;text-decoration:none;cursor:pointer;font-size:14px}.primary{background:#111827;color:#fff}.secondary{background:#e5e7eb;color:#111827}.note{font-size:13px;color:#6b7280;margin-top:12px}.error{background:#fef2f2;color:#991b1b;padding:10px 12px;border-radius:10px;margin-bottom:12px}</style></head>
<body><div class="wrap"><div class="card"><h1>{{.Title}}</h1><p>Allgemeine Einstellungen des Clients.</p>{{if .Error}}<div class="error">{{.Error}}</div>{{end}}<form method="post" action="/settings"><label>Download-Ordner</label><input name="download_dir" value="{{.DownloadDir}}"><p class="note">IRC-Networks, NickServ, SASL, Default Nick und Auto-Register bearbeitest du auf der separaten IRC-Seite.</p><div class="actions"><button class="primary" type="submit">Speichern</button><a class="button secondary" href="/irc/setup">IRC-Einstellungen</a><a class="button secondary" href="/">Zurück</a></div></form></div></div></body></html>`))

var ircSetupTemplate = template.Must(template.New("ircsetup").Parse(`<!doctype html>
<html lang="de">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title><style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f6f7f9;color:#1f2937}.wrap{max-width:980px;margin:24px auto;padding:0 16px}.card{background:#fff;border-radius:12px;box-shadow:0 1px 6px rgba(0,0,0,.08);padding:16px 18px;margin-bottom:16px}label{display:block;font-size:14px;font-weight:600;margin:12px 0 6px}input{width:100%;box-sizing:border-box;padding:10px 12px;border:1px solid #d1d5db;border-radius:10px}input[type=checkbox]{width:auto;margin-right:8px}.row{display:grid;grid-template-columns:1fr 1fr;gap:14px}.layout{display:grid;grid-template-columns:280px 1fr;gap:16px}.netlist{display:flex;flex-direction:column;gap:10px}.netitem{display:block;padding:10px 12px;border:1px solid #d1d5db;border-radius:10px;text-decoration:none;color:#111827;background:#fff}.netitem strong{display:block}.netitem small{display:block;color:#6b7280;margin-top:4px}.netitem.active{border-color:#111827;background:#f9fafb}.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}.button,button{border:0;border-radius:10px;padding:10px 16px;text-decoration:none;cursor:pointer;font-size:14px}.primary{background:#111827;color:#fff}.secondary{background:#e5e7eb;color:#111827}.note{font-size:13px;color:#6b7280;margin-top:12px}.error{background:#fef2f2;color:#991b1b;padding:10px 12px;border-radius:10px;margin-bottom:12px}@media (max-width:760px){.row,.layout{grid-template-columns:1fr}}</style></head>
<body><div class="wrap"><div class="card"><h1>{{.Title}}</h1><p>Diese Daten werden nur lokal gespeichert und niemals an autofetch gesendet. Passwörter liegen in irc-secrets.json, nicht in client.json.</p>{{if .Error}}<div class="error">{{.Error}}</div>{{end}}<div class="layout"><div><h2>Networks</h2><div class="note">Networks und Channels werden automatisch aus Download-Aufträgen übernommen. Hier wählst du vorhandene Einträge aus und pflegst nur lokale NickServ-/SASL-Zugangsdaten sowie globale IRC-Optionen.</div><div class="netlist">{{range .Networks}}<a class="netitem {{if eq .Host $.Network.Host}}active{{end}}" href="/irc/setup?network={{.Host}}"><strong>{{if .Name}}{{.Name}}{{else}}{{.Host}}{{end}}</strong><small>{{.Host}}:{{.Port}}{{if .Channels}} · {{index .Channels 0}}{{end}}</small></a>{{end}}</div></div><div><form method="post" action="/irc/setup"><h2>Globale IRC-Einstellungen</h2><label>Default Nick</label><input name="default_nick" value="{{.DefaultNick}}"><div><label><input type="checkbox" name="auto_register" {{if .AutoRegister}}checked{{end}}>Nick automatisch registrieren, wenn ein Network dies verlangt</label></div><label>Registrierungs-E-Mail</label><input type="email" name="registration_email" value="{{.RegistrationEmail}}"><h2 style="margin-top:22px">Reverse DCC</h2><div><label><input type="checkbox" name="reverse_dcc_enabled" {{if .ReverseDCCEnabled}}checked{{end}}>Reverse-/Passive-DCC-Downloads annehmen</label></div><div class="row"><div><label>DCC-Port von</label><input name="reverse_dcc_port_min" value="{{.ReverseDCCPortMin}}"></div><div><label>DCC-Port bis</label><input name="reverse_dcc_port_max" value="{{.ReverseDCCPortMax}}"></div></div><p class="note">Manche Bots senden Dateien per Reverse DCC. Dafür muss dein Router eingehende TCP-Verbindungen im hier eingestellten Portbereich an die lokale IP dieses Geräts weiterleiten. Die öffentliche IP ermittelt der Client automatisch; nur bei Problemen kann sie per AUTOFETCH_DCC_PUBLIC_IP überschrieben werden.</p><h2 style="margin-top:22px">Network</h2><div class="row"><div><label>Anzeigename</label><input name="name" value="{{.Network.Name}}"></div><div><label>Host</label><input name="host" value="{{.Network.Host}}"></div></div><div class="row"><div><label>Channel</label><input name="channel" value="{{.Channel}}"></div><div><label>Port</label><input name="port" value="{{.Network.Port}}"></div></div><div><label><input type="checkbox" name="tls" {{if .Network.TLS}}checked{{end}}>TLS verwenden</label></div><div class="row"><div><label>Nick</label><input name="nick" value="{{.Network.Nick}}"></div><div><label>Username</label><input name="username" value="{{.Network.Username}}"></div></div><label>Realname</label><input name="realname" value="{{.Network.Realname}}"><h2>NickServ</h2><div><label><input type="checkbox" name="nickserv_enabled" {{if .Network.NickServ.Enabled}}checked{{end}}>NickServ verwenden</label></div><div class="row"><div><label>Command</label><input name="nickserv_command" value="{{.Network.NickServ.Command}}"></div><div><label>Passwort</label><input type="password" name="nickserv_password" value="" placeholder="unverändert lassen, um bestehendes Passwort zu behalten"></div></div><div><label><input type="checkbox" name="clear_nickserv_password">Bestehendes NickServ-Passwort löschen</label></div><h2>SASL</h2><div><label><input type="checkbox" name="sasl_enabled" {{if .Network.SASL.Enabled}}checked{{end}}>SASL verwenden</label></div><div class="row"><div><label>Username</label><input name="sasl_username" value="" placeholder="unverändert lassen, um bestehenden Wert zu behalten"></div><div><label>Passwort</label><input type="password" name="sasl_password" value="" placeholder="unverändert lassen, um bestehendes Passwort zu behalten"></div></div><div><label><input type="checkbox" name="clear_sasl_username">Bestehenden SASL-Username löschen</label></div><div><label><input type="checkbox" name="clear_sasl_password">Bestehendes SASL-Passwort löschen</label></div><div class="actions"><button class="secondary" type="submit" name="generate_nick" value="1">Neuen Nick generieren</button><button class="primary" type="submit">Speichern</button><a class="button secondary" href="/settings">Allgemeine Einstellungen</a><a class="button secondary" href="/">Zurück</a></div></form></div></div></div></div></body></html>`))
