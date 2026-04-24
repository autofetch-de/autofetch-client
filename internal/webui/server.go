package webui

import (
	"context"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/observe"
)

const DefaultPort = 23324

type service interface {
	Start() error
	Stop() error
	StartRePair() error
	TestConnection(context.Context) error
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
	_ = pageTemplate.Execute(w, map[string]string{"Title": "autofetch status"})
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
    <div class="label" style="margin-bottom:8px;">Log-Auszug</div>
    <pre id="logs"></pre>
  </div>
</div>
<script>
let lastCode = '';

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
  document.getElementById('logs').textContent = (s.logs || []).join('\n');
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
