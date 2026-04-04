package observe

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type DownloadSnapshot struct {
	Target     string  `json:"target"`
	Downloaded int64   `json:"downloaded"`
	Total      int64   `json:"total"`
	SpeedBps   float64 `json:"speed_bps"`
	ETASeconds int64   `json:"eta_seconds"`
}

type Snapshot struct {
	Running            bool              `json:"running"`
	Connected          bool              `json:"connected"`
	ServerURL          string            `json:"server_url"`
	ClientName         string            `json:"client_name"`
	ClientID           string            `json:"client_id"`
	LastPoll           string            `json:"last_poll"`
	CurrentJob         string            `json:"current_job"`
	LastDownload       string            `json:"last_download"`
	LastDownloadStatus string            `json:"last_download_status"`
	LastError          string            `json:"last_error"`
	Paired             bool              `json:"paired"`
	PairingActive      bool              `json:"pairing_active"`
	PairingStatus      string            `json:"pairing_status"`
	PairingCode        string            `json:"pairing_code"`
	PairingURL         string            `json:"pairing_url"`
	PairingExpiry      string            `json:"pairing_expiry"`
	Logs               []string          `json:"logs"`
	ActiveDownload     *DownloadSnapshot `json:"active_download,omitempty"`
}

type State struct {
	mu sync.RWMutex

	serverURL          string
	clientName         string
	clientID           string
	running            bool
	connected          bool
	lastPoll           time.Time
	currentJob         string
	lastDownload       string
	lastDownloadStatus string
	lastError          string
	logs               []string
	maxLogs            int

	paired        bool
	pairingActive bool
	pairingStatus string
	pairingCode   string
	pairingURL    string
	pairingExpiry time.Time

	activeDownload *DownloadSnapshot
}

func NewState(serverURL, clientID, clientName string, maxLogs int) *State {
	if maxLogs <= 0 {
		maxLogs = 100
	}
	s := &State{serverURL: serverURL, clientID: clientID, clientName: strings.TrimSpace(clientName), maxLogs: maxLogs}
	s.paired = strings.TrimSpace(clientID) != ""
	return s
}

func (s *State) SetClientName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(name)
	if name != "" {
		s.clientName = name
	}
}

func (s *State) SetIdentity(serverURL, clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(serverURL) != "" {
		s.serverURL = serverURL
	}
	if strings.TrimSpace(clientID) != "" {
		s.clientID = clientID
		s.paired = true
	}
}

func (s *State) SetRunning(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = v
	if !v {
		s.currentJob = ""
		s.activeDownload = nil
	}
}

func (s *State) SetConnected(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = v
}

func (s *State) SetPaired(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paired = v
	if !v {
		s.clientID = ""
		s.connected = false
		s.activeDownload = nil
	}
}

func (s *State) StartPairing(code, pairingURL string, expiry time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paired = false
	s.connected = false
	s.running = false
	s.clientID = ""
	s.currentJob = ""
	s.activeDownload = nil
	s.pairingActive = true
	s.pairingStatus = "Warte auf Bestätigung"
	s.pairingCode = strings.TrimSpace(code)
	s.pairingURL = strings.TrimSpace(pairingURL)
	s.pairingExpiry = expiry
}

func (s *State) PairingPending(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairingActive = true
	if strings.TrimSpace(status) == "" {
		status = "Warte auf Bestätigung"
	}
	s.pairingStatus = status
}

func (s *State) PairingApproved(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paired = true
	s.clientID = strings.TrimSpace(clientID)
	s.pairingActive = false
	s.pairingStatus = "Gekoppelt"
	s.pairingCode = ""
	s.pairingExpiry = time.Time{}
}

func (s *State) PairingFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairingActive = false
	s.paired = false
	if err != nil {
		s.pairingStatus = err.Error()
		s.lastError = err.Error()
	} else {
		s.pairingStatus = "Pairing fehlgeschlagen"
	}
}

func (s *State) Poll(ok bool, at time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastPoll = at
	s.connected = ok
	if err != nil {
		s.lastError = err.Error()
	}
}

func (s *State) JobLeased(jobID, jobType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentJob = strings.TrimSpace(fmt.Sprintf("%s (%s)", jobID, jobType))
}

func (s *State) JobCleared() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentJob = ""
	s.activeDownload = nil
}

func (s *State) DownloadStarted(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target = strings.TrimSpace(target)
	if target != "" {
		s.lastDownload = target
		s.lastDownloadStatus = "läuft"
		s.activeDownload = &DownloadSnapshot{Target: target}
	}
}

func (s *State) DownloadProgress(target string, downloaded, total int64, speedBps float64, eta time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}
	if s.activeDownload == nil || s.activeDownload.Target != target {
		s.activeDownload = &DownloadSnapshot{Target: target}
	}
	s.activeDownload.Downloaded = downloaded
	s.activeDownload.Total = total
	s.activeDownload.SpeedBps = speedBps
	if eta > 0 {
		s.activeDownload.ETASeconds = int64(eta.Round(time.Second).Seconds())
	} else {
		s.activeDownload.ETASeconds = 0
	}
}

func (s *State) DownloadFinished(target, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(target) != "" {
		s.lastDownload = strings.TrimSpace(target)
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "fertig"
	}
	s.lastDownloadStatus = status
	s.activeDownload = nil
}

func (s *State) Error(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = err.Error()
}

func (s *State) AppendLog(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, line)
	if len(s.logs) > s.maxLogs {
		s.logs = append([]string(nil), s.logs[len(s.logs)-s.maxLogs:]...)
	}
}

func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	logs := append([]string(nil), s.logs...)
	var active *DownloadSnapshot
	if s.activeDownload != nil {
		copy := *s.activeDownload
		active = &copy
	}
	return Snapshot{
		Running:            s.running,
		Connected:          s.connected,
		ServerURL:          s.serverURL,
		ClientName:         s.clientName,
		ClientID:           s.clientID,
		LastPoll:           formatTime(s.lastPoll),
		CurrentJob:         s.currentJob,
		LastDownload:       s.lastDownload,
		LastDownloadStatus: s.lastDownloadStatus,
		LastError:          s.lastError,
		Paired:             s.paired,
		PairingActive:      s.pairingActive,
		PairingStatus:      s.pairingStatus,
		PairingCode:        s.pairingCode,
		PairingURL:         s.pairingURL,
		PairingExpiry:      formatTime(s.pairingExpiry),
		Logs:               logs,
		ActiveDownload:     active,
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(time.RFC3339)
}
