package api

import "time"

// --- Pairing / registration ---

type RegisterStartRequest struct {
	ClientName  string `json:"client_name"`
	Platform    string `json:"platform"`
	Arch        string `json:"arch"`
	Version     string `json:"version"`
	Variant     string `json:"variant,omitempty"`
	BuildCommit string `json:"build_commit,omitempty"`
	BuildDate   string `json:"build_date,omitempty"`
	ClientProtocolInfo
}

type RegisterStartResponse struct {
	PairingID        string    `json:"pairing_id"`
	PairingCode      string    `json:"pairing_code"`
	ExpiresAt        time.Time `json:"expires_at"`
	PollAfterSeconds int       `json:"poll_after_seconds"`
}

type RegisterPollRequest struct {
	PairingID string `json:"pairing_id"`
}

type RegisterPollResponse struct {
	Status           string `json:"status"` // PENDING | APPROVED | EXPIRED | REJECTED
	PollAfterSeconds int    `json:"poll_after_seconds"`
	ClientID         string `json:"client_id,omitempty"`
	ClientToken      string `json:"client_token,omitempty"`
}

type ClientMetadata struct {
	Version     string `json:"version,omitempty"`
	Platform    string `json:"platform,omitempty"`
	Arch        string `json:"arch,omitempty"`
	Variant     string `json:"variant,omitempty"`
	BuildCommit string `json:"build_commit,omitempty"`
	BuildDate   string `json:"build_date,omitempty"`
}

type LeaseRequest struct {
	ClientID string `json:"client_id"`
	ClientMetadata
	ClientProtocolInfo
}

type LeaseResponse struct {
	Job               *LeasedJob `json:"job"`
	RetryAfterSeconds int        `json:"retry_after_seconds"`
}

type LeasedJob struct {
	JobID   string     `json:"job_id"`
	Type    string     `json:"type"`
	Payload JobPayload `json:"payload"`
	Lease   Lease      `json:"lease"`
}

type Lease struct {
	AttemptID string    `json:"attempt_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// --- Payload (nur Felder, die wir brauchen) ---

// ArchiveProcessing is a server-authored immutable per-job instruction.
// Missing or invalid instructions are download-only, never delete originals.
type ArchiveProcessing struct {
	Enabled        bool   `json:"enabled"`
	Layout         string `json:"layout"`
	DeleteOriginal bool   `json:"delete_original"`
	NestedDepth    int    `json:"nested_depth"`
}

func (p ArchiveProcessing) Validate() bool {
	if p.Layout != "separate" && p.Layout != "same" {
		return false
	}
	if p.NestedDepth < 0 || p.NestedDepth > 3 {
		return false
	}
	return p.Enabled || (!p.DeleteOriginal && p.NestedDepth == 0)
}

type JobPayload struct {
	SchemaVersion      int                 `json:"schema_version,omitempty"`
	IRCSourceSelection *IRCSourceSelection `json:"irc_source_selection,omitempty"`
	ArchiveProcessing  *ArchiveProcessing  `json:"archive_processing,omitempty"`
	Query              *QueryInfo          `json:"query,omitempty"`
	Dedupe             *DedupeInfo         `json:"dedupe,omitempty"`
	Download           *DownloadPayload    `json:"download,omitempty"`
	EffectiveSearch    *EffectiveSearch    `json:"effective_search,omitempty"`
	Candidates         []Candidate         `json:"candidates,omitempty"`
}

type IRCSourceSelection struct {
	Mode           string   `json:"mode"`
	TriedSourceIDs []string `json:"tried_source_ids"`
}

type QueryInfo struct {
	Name    string `json:"name"`
	QueryID string `json:"query_id"`
}

type DedupeInfo struct {
	DedupeKey string `json:"dedupe_key"`
}

type DownloadPayload struct {
	Instruction      DownloadInstruction `json:"instruction"`
	DownloadPath     *string             `json:"download_path"`     // legacy; we won't use for naming anymore
	FilenameTemplate *string             `json:"filename_template"` // legacy; we won't use for naming anymore
}

type DownloadInstruction struct {
	DownloadMethod string      `json:"download_method,omitempty"` // xdcc_numeric (default) | irc_command
	SourceID       string      `json:"source_id,omitempty"`       // authoritative selected source (pack UUID)
	Mode           string      `json:"mode"`                      // "single" | "xdcc"
	Quality        QualityInfo `json:"quality"`
	Provider       string      `json:"provider,omitempty"`
	GroupKey       string      `json:"group_key,omitempty"`

	// HTTP download fields (mode: "single")
	VideoURL     string `json:"video_url,omitempty"`
	SubtitleURL  string `json:"subtitle_url,omitempty"`
	ExpectedSize int64  `json:"expected_size,omitempty"`

	// IRC XDCC fields (mode: "xdcc")
	IRC *IRCXDCCInfo `json:"irc,omitempty"`

	DedupeKey   string `json:"dedupe_key,omitempty"`
	WebsiteURL  string `json:"website_url,omitempty"`
	CandidateID string `json:"candidate_id,omitempty"`

	// NEW: server-driven output plan (source of truth for path + filenames)
	Output *InstructionOutput `json:"output,omitempty"`
}

type IRCXDCCInfo struct {
	Host                 string   `json:"host,omitempty"`
	Port                 int      `json:"port,omitempty"`
	TLS                  bool     `json:"tls,omitempty"`
	TLSVerification      string   `json:"tls_verification,omitempty"`
	TLSSPKIPinSHA256     string   `json:"tls_spki_pin_sha256,omitempty"`
	Network              string   `json:"network,omitempty"` // legacy fallback
	Channel              string   `json:"channel,omitempty"`
	Bot                  string   `json:"bot,omitempty"`
	Package              any      `json:"package,omitempty"` // server may send number or string
	Filename             string   `json:"filename,omitempty"`
	PrerequisiteChannels []string `json:"prerequisite_channels,omitempty"`
	CommandTargetKind    string   `json:"command_target_kind,omitempty"`
	CommandTarget        string   `json:"command_target,omitempty"`
	DownloadCommand      string   `json:"download_command,omitempty"`
}

type InstructionOutput struct {
	Dir              string `json:"dir,omitempty"` // relative dir, or empty => write into download_base_path
	FilenameVideo    string `json:"filename_video"`
	FilenameSubtitle string `json:"filename_subtitle,omitempty"`
}

type QualityInfo struct {
	Selected  string `json:"selected,omitempty"`
	Requested string `json:"requested,omitempty"`
}

type EffectiveSearch struct {
	Counter *CounterInfo `json:"counter,omitempty"`
}

type CounterInfo struct {
	Value   string `json:"value"`   // e.g. "02"
	Numeric int    `json:"numeric"` // e.g. 2
	Pad     int    `json:"pad"`     // e.g. 2
	Step    int    `json:"step"`    // e.g. 1
}

type Candidate struct {
	Title       string `json:"title"`
	CandidateID string `json:"candidate_id"`
}

// --- Dedupe claim ---

type DedupeClaimRequest struct {
	ClientID   string         `json:"client_id"`
	JobID      string         `json:"job_id"`
	AttemptID  string         `json:"attempt_id"`
	DedupeKey  string         `json:"dedupe_key"`
	TTLSeconds int            `json:"ttl_seconds"`
	Quality    QualityInfo    `json:"quality,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type DedupeClaimResponse struct {
	Status string `json:"status"` // CLAIMED | IN_PROGRESS | ALREADY_SUCCEEDED

	ClaimID        string    `json:"claim_id,omitempty"`
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`

	ResultJobID string          `json:"result_job_id,omitempty"`
	Result      *CompleteResult `json:"result,omitempty"`
}

// --- Heartbeat ---

type HeartbeatRequest struct {
	ClientID      string        `json:"client_id"`
	AttemptID     string        `json:"attempt_id"`
	ExtendSeconds int           `json:"extend_seconds"`
	Progress      *ProgressInfo `json:"progress,omitempty"`
	DedupeKey     string        `json:"dedupe_key,omitempty"`
}

type ProgressInfo struct {
	// New, user-friendly progress contract (stored 1:1 as JSONB on server).
	Phase string `json:"phase"`
	Pct   int    `json:"pct"`

	// Optional numeric hints.
	BytesDone  *int64 `json:"bytes_done,omitempty"`
	BytesTotal *int64 `json:"bytes_total,omitempty"`
	SpeedBps   *int64 `json:"speed_bps,omitempty"`
}

type HeartbeatResponse struct {
	OK             bool      `json:"ok"`
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`
}

type APIErrorResponse struct {
	Message    string     `json:"message,omitempty"`
	Error      string     `json:"error"`
	CanceledAt *time.Time `json:"canceled_at,omitempty"`
}

// --- Complete ---

type CompleteRequest struct {
	ClientID  string         `json:"client_id"`
	AttemptID string         `json:"attempt_id"`
	Result    CompleteResult `json:"result"`
}

type CompleteResult struct {
	Status string         `json:"status"` // SUCCESS|FAILED|RETRYABLE_ERROR|NOT_FOUND_YET
	Error  string         `json:"error,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

type RuntimeConfigResponse struct {
	ClientName              string `json:"client_name,omitempty"`
	PollIntervalSec         int    `json:"poll_interval_sec"`
	MaxParallelDownloads    int    `json:"max_parallel_downloads"`
	BandwidthLimitKiBPerSec int    `json:"bandwidth_limit_kib_per_sec"`
	HeartbeatIntervalSec    int    `json:"heartbeat_interval_sec"`
	HeartbeatExtendSec      int    `json:"heartbeat_extend_sec"`
	DedupeClaimTTLSec       int    `json:"dedupe_claim_ttl_sec"`
}
