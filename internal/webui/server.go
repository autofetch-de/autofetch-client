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
	"github.com/autofetch-de/autofetch-client/internal/localization"
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
	l       *localization.Localizer
}

func New(addr string, state *observe.State, service service, l *localization.Localizer) *Server {
	if l == nil {
		l = localization.New(localization.English)
	}
	mux := http.NewServeMux()
	s := &Server{state: state, service: service, addr: addr, l: l}
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
	rawSnap := s.state.Snapshot()
	snap := s.localizeSnapshot(rawSnap)
	network, channel := parseIRCAuthContext(rawSnap.LastError)
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
	data := map[string]any{"Title": s.l.T("web.title"), "Lang": s.l.Language(), "T": s.l.T, "Snapshot": snap, "ShowIRCAuth": strings.Contains(strings.ToLower(rawSnap.LastError), "irc_registered_nick_required"), "IRCSetupLink": setupLink, "IRCNetwork": network, "IRCChannel": channel}
	_ = pageTemplate.Execute(w, data)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.localizedSnapshot())
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
		"Title":       s.l.T("settings.title"),
		"Lang":        s.l.Language(),
		"T":           s.l.T,
		"Error":       s.l.UserError(errMsg),
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
	data := map[string]any{"Title": s.l.T("irc.title"), "Lang": s.l.Language(), "T": s.l.T, "Error": s.l.UserError(errMsg), "DefaultNick": ircCfg.DefaultNick, "AutoRegister": ircCfg.AutoRegister, "RegistrationEmail": ircCfg.RegistrationEmail, "ReverseDCCEnabled": ircCfg.ReverseDCCEnabled, "ReverseDCCPortMin": ircCfg.ReverseDCCPortMin, "ReverseDCCPortMax": ircCfg.ReverseDCCPortMax, "Network": selected, "Channel": channel, "Networks": ircCfg.Networks}
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
<html lang="{{.Lang}}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f6f7f9;color:#1f2937}.wrap{max-width:980px;margin:24px auto;padding:0 16px}.card{background:#fff;border-radius:12px;box-shadow:0 1px 6px rgba(0,0,0,.08);padding:16px 18px;margin-bottom:16px}h1,h2{margin:0 0 12px}h1{font-size:24px}h2{font-size:20px}.grid{display:grid;grid-template-columns:220px 1fr;gap:10px 14px}.value{overflow-wrap:anywhere}.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:16px}.button,button{border:0;border-radius:10px;padding:10px 16px;text-decoration:none;cursor:pointer;font-size:14px}.primary{background:#111827;color:#fff}.secondary{background:#e5e7eb;color:#111827}.danger{background:#fee2e2;color:#991b1b}.status{font-weight:700}.note{font-size:13px;color:#6b7280}.warning{background:#fff7ed;color:#9a3412;padding:12px;border-radius:10px;margin-top:12px}pre{background:#111827;color:#e5e7eb;padding:14px;border-radius:10px;min-height:180px;max-height:360px;overflow:auto;white-space:pre-wrap}.pair-code{font-size:30px;font-weight:800;letter-spacing:.12em;text-align:center;padding:14px}.hidden{display:none}@media(max-width:640px){.grid{grid-template-columns:1fr}.pair-code{font-size:24px}}
  </style>
</head>
<body data-working="{{call .T "web.working"}}" data-ok="{{call .T "web.ok"}}" data-failed="{{call .T "web.failed"}}" data-connection-successful="{{call .T "notice.connection_successful"}}" data-log-copied="{{call .T "notice.log_copied"}}" data-log-copy-failed="{{call .T "notice.log_copy_failed"}}" data-code-copied="{{call .T "notice.pairing_code_copied"}}" data-copy-failed="{{call .T "notice.copy_failed"}}" data-yes="{{call .T "web.yes"}}" data-no="{{call .T "web.no"}}">
<div class="wrap">
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
    {{if .ShowIRCAuth}}<div class="warning">{{call .T "web.irc_auth_required"}} {{if .IRCNetwork}}<strong>{{.IRCNetwork}}</strong>{{end}}{{if .IRCChannel}} · {{.IRCChannel}}{{end}}<div class="actions"><a class="button secondary" href="{{.IRCSetupLink}}">{{call .T "web.open_irc_settings"}}</a></div></div>{{end}}
    <div class="actions">
      <button class="primary" onclick="action('/api/start')">{{call .T "web.start"}}</button>
      <button class="secondary" onclick="action('/api/stop')">{{call .T "web.stop"}}</button>
      <button class="secondary" onclick="action('/api/test')">{{call .T "action.test_connection"}}</button>
      <button class="danger" onclick="action('/api/repair')">{{call .T "web.repair"}}</button>
      <a class="button secondary" href="/settings">{{call .T "action.settings"}}</a>
      <a class="button secondary" href="/irc/setup">{{call .T "irc.title"}}</a>
    </div>
  </div>
  <div class="card"><h2>{{call .T "web.logs"}}</h2><pre id="logs"></pre><div class="actions"><button class="secondary" onclick="copyLogs()">{{call .T "action.copy_log"}}</button></div></div>
</div>
<script>
const text = document.body.dataset;
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
  if(!logSelecting&&!window.getSelection().toString())logs.textContent=(s.logs||[]).join('\n');
}
async function refresh(){try{const res=await fetch('/api/status');updateView(await res.json())}catch(_){}}
async function action(path){const out=document.getElementById('action-result');out.textContent=text.working;const res=await fetch(path,{method:'POST'});let msg=res.ok?text.ok:text.failed;try{const data=await res.json();if(data&&data.error)msg=data.error;if(data&&data.ok&&path==='/api/test')msg=text.connectionSuccessful}catch(_){}out.textContent=msg;refresh()}
async function copyLogs(){try{await navigator.clipboard.writeText(logs.textContent||'');document.getElementById('action-result').textContent=text.logCopied}catch(_){document.getElementById('action-result').textContent=text.logCopyFailed}}
async function copyCode(){if(!lastCode)return;try{await navigator.clipboard.writeText(lastCode);document.getElementById('action-result').textContent=text.codeCopied}catch(_){document.getElementById('action-result').textContent=text.copyFailed}}
refresh();setInterval(refresh,2000);
</script>
</body></html>`))

var settingsTemplate = template.Must(template.New("settings").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title><style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f6f7f9;color:#1f2937}.wrap{max-width:900px;margin:24px auto;padding:0 16px}.card{background:#fff;border-radius:12px;box-shadow:0 1px 6px rgba(0,0,0,.08);padding:16px 18px}label{display:block;font-size:14px;font-weight:600;margin:12px 0 6px}input{width:100%;box-sizing:border-box;padding:10px 12px;border:1px solid #d1d5db;border-radius:10px}.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}.button,button{border:0;border-radius:10px;padding:10px 16px;text-decoration:none;cursor:pointer;font-size:14px}.primary{background:#111827;color:#fff}.secondary{background:#e5e7eb;color:#111827}.note{font-size:13px;color:#6b7280;margin-top:12px}.error{background:#fef2f2;color:#991b1b;padding:10px 12px;border-radius:10px;margin-bottom:12px}</style></head>
<body><div class="wrap"><div class="card"><h1>{{.Title}}</h1><p>{{call .T "settings.general_intro"}}</p>{{if .Error}}<div class="error">{{.Error}}</div>{{end}}<form method="post" action="/settings"><label>{{call .T "settings.download_folder"}}</label><input name="download_dir" value="{{.DownloadDir}}"><p class="note">{{call .T "settings.irc_separate_hint"}}</p><div class="actions"><button class="primary" type="submit">{{call .T "action.save"}}</button><a class="button secondary" href="/irc/setup">{{call .T "irc.title"}}</a><a class="button secondary" href="/">{{call .T "action.back"}}</a></div></form></div></div></body></html>`))

var ircSetupTemplate = template.Must(template.New("ircsetup").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title><style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f6f7f9;color:#1f2937}.wrap{max-width:980px;margin:24px auto;padding:0 16px}.card{background:#fff;border-radius:12px;box-shadow:0 1px 6px rgba(0,0,0,.08);padding:16px 18px}label{display:block;font-size:14px;font-weight:600;margin:12px 0 6px}input{width:100%;box-sizing:border-box;padding:10px 12px;border:1px solid #d1d5db;border-radius:10px}input[type=checkbox]{width:auto;margin-right:8px}.row{display:grid;grid-template-columns:1fr 1fr;gap:14px}.layout{display:grid;grid-template-columns:280px 1fr;gap:16px}.netlist{display:flex;flex-direction:column;gap:10px}.netitem{display:block;padding:10px 12px;border:1px solid #d1d5db;border-radius:10px;text-decoration:none;color:#111827;background:#fff}.netitem.active{border-color:#111827;background:#f9fafb}.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}.button,button{border:0;border-radius:10px;padding:10px 16px;text-decoration:none;cursor:pointer;font-size:14px}.primary{background:#111827;color:#fff}.secondary{background:#e5e7eb;color:#111827}.note{font-size:13px;color:#6b7280;margin-top:12px}.error{background:#fef2f2;color:#991b1b;padding:10px 12px;border-radius:10px;margin-bottom:12px}@media(max-width:760px){.row,.layout{grid-template-columns:1fr}}</style></head>
<body><div class="wrap"><div class="card"><h1>{{.Title}}</h1><p>{{call .T "irc.data_privacy"}}</p>{{if .Error}}<div class="error">{{.Error}}</div>{{end}}<div class="layout"><div><h2>{{call .T "irc.networks"}}</h2><div class="note">{{call .T "irc.web_networks_hint"}}</div><div class="netlist">{{range .Networks}}<a class="netitem {{if eq .Host $.Network.Host}}active{{end}}" href="/irc/setup?network={{.Host}}"><strong>{{if .Name}}{{.Name}}{{else}}{{.Host}}{{end}}</strong><small>{{.Host}}:{{.Port}}{{if .Channels}} · {{index .Channels 0}}{{end}}</small></a>{{end}}</div></div><div><form method="post" action="/irc/setup"><h2>{{call .T "irc.global_settings"}}</h2><label>{{call .T "irc.default_nick"}}</label><input name="default_nick" value="{{.DefaultNick}}"><div><label><input type="checkbox" name="auto_register" {{if .AutoRegister}}checked{{end}}>{{call .T "irc.auto_register_if_required"}}</label></div><label>{{call .T "irc.registration_email"}}</label><input type="email" name="registration_email" value="{{.RegistrationEmail}}"><h2>Reverse DCC</h2><div><label><input type="checkbox" name="reverse_dcc_enabled" {{if .ReverseDCCEnabled}}checked{{end}}>{{call .T "irc.accept_reverse_dcc"}}</label></div><div class="row"><div><label>{{call .T "irc.port_from"}}</label><input name="reverse_dcc_port_min" value="{{.ReverseDCCPortMin}}"></div><div><label>{{call .T "irc.port_to"}}</label><input name="reverse_dcc_port_max" value="{{.ReverseDCCPortMax}}"></div></div><p class="note">{{call .T "irc.reverse_dcc_web_hint"}}</p><h2>{{call .T "irc.network"}}</h2><div class="row"><div><label>{{call .T "irc.display_name"}}</label><input name="name" value="{{.Network.Name}}"></div><div><label>{{call .T "irc.host"}}</label><input name="host" value="{{.Network.Host}}"></div></div><div class="row"><div><label>{{call .T "irc.channel"}}</label><input name="channel" value="{{.Channel}}"></div><div><label>{{call .T "irc.port"}}</label><input name="port" value="{{.Network.Port}}"></div></div><div><label><input type="checkbox" name="tls" {{if .Network.TLS}}checked{{end}}>{{call .T "irc.use_tls"}}</label></div><div class="row"><div><label>{{call .T "irc.nick"}}</label><input name="nick" value="{{.Network.Nick}}"></div><div><label>{{call .T "irc.username"}}</label><input name="username" value="{{.Network.Username}}"></div></div><label>{{call .T "irc.realname"}}</label><input name="realname" value="{{.Network.Realname}}"><h2>NickServ</h2><div><label><input type="checkbox" name="nickserv_enabled" {{if .Network.NickServ.Enabled}}checked{{end}}>{{call .T "irc.use_nickserv"}}</label></div><div class="row"><div><label>{{call .T "irc.nickserv_command"}}</label><input name="nickserv_command" value="{{.Network.NickServ.Command}}"></div><div><label>{{call .T "irc.nickserv_password"}}</label><input type="password" name="nickserv_password" placeholder="{{call .T "irc.leave_unchanged"}}"></div></div><div><label><input type="checkbox" name="clear_nickserv_password">{{call .T "action.delete_nickserv_password"}}</label></div><h2>SASL</h2><div><label><input type="checkbox" name="sasl_enabled" {{if .Network.SASL.Enabled}}checked{{end}}>{{call .T "irc.use_sasl"}}</label></div><div class="row"><div><label>{{call .T "irc.sasl_username"}}</label><input name="sasl_username" placeholder="{{call .T "irc.leave_unchanged"}}"></div><div><label>{{call .T "irc.sasl_password"}}</label><input type="password" name="sasl_password" placeholder="{{call .T "irc.leave_unchanged"}}"></div></div><div><label><input type="checkbox" name="clear_sasl_username">{{call .T "action.delete_sasl_access"}}</label></div><div><label><input type="checkbox" name="clear_sasl_password">{{call .T "action.delete_sasl_access"}}</label></div><div class="actions"><button class="secondary" type="submit" name="generate_nick" value="1">{{call .T "action.generate_nick"}}</button><button class="primary" type="submit">{{call .T "action.save"}}</button><a class="button secondary" href="/settings">{{call .T "settings.title"}}</a><a class="button secondary" href="/">{{call .T "action.back"}}</a></div></form></div></div></div></div></body></html>`))
