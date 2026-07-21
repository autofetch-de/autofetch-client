package localization

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"
)

const (
	German  = "de"
	English = "en"
)

//go:embed locales/*.json
var catalogFS embed.FS

type catalog map[string]string

type Localizer struct {
	language string
}

var (
	catalogsOnce sync.Once
	catalogs     map[string]catalog
	catalogsErr  error
	portPattern  = regexp.MustCompile(`(?i)(?:port[=: ]+|tcp-port\s+)(\d{1,5})`)
	httpPattern  = regexp.MustCompile(`(?i)(?:http_|status[=: ]+)([1-5][0-9]{2})`)
)

func NormalizeLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case German, "de-de", "de_de", "german", "deutsch":
		return German
	case English, "en-us", "en_us", "en-gb", "en_gb", "english":
		return English
	default:
		return English
	}
}

func New(language string) *Localizer {
	loadCatalogs()
	return &Localizer{language: NormalizeLanguage(language)}
}

func (l *Localizer) Language() string {
	if l == nil {
		return English
	}
	return NormalizeLanguage(l.language)
}

func (l *Localizer) T(key string, data ...map[string]any) string {
	loadCatalogs()
	message, ok := lookup(l.Language(), key)
	if !ok {
		message, ok = lookup(English, key)
	}
	if !ok {
		log.Printf("missing translation key=%q language=%s", key, l.Language())
		if l.Language() == German {
			return "Übersetzung nicht verfügbar"
		}
		return "Translation unavailable"
	}
	if len(data) == 0 || len(data[0]) == 0 || !strings.Contains(message, "{{") {
		return message
	}
	tmpl, err := template.New(key).Option("missingkey=error").Parse(message)
	if err != nil {
		log.Printf("invalid translation template key=%q: %v", key, err)
		return message
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data[0]); err != nil {
		log.Printf("translation render failed key=%q: %v", key, err)
		return message
	}
	return out.String()
}

func (l *Localizer) Status(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	key := "status_code." + code
	if _, ok := lookup(l.Language(), key); ok {
		return l.T(key)
	}
	if _, ok := lookup(English, key); ok {
		return l.T(key)
	}
	return code
}

func (l *Localizer) UserError(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "reverse_dcc_port_forward_required") || strings.Contains(lower, "reverse_dcc_timeout"):
		port := ""
		if match := portPattern.FindStringSubmatch(raw); len(match) == 2 {
			port = match[1]
		}
		if port != "" {
			return l.T("error.reverse_dcc_port_forward_required_port", map[string]any{"Port": port})
		}
		return l.T("error.reverse_dcc_port_forward_required")
	case strings.Contains(lower, "reverse_dcc_disabled"):
		return l.T("error.reverse_dcc_disabled")
	case strings.Contains(lower, "client not paired") || strings.Contains(lower, "client_id") && strings.Contains(lower, "missing"):
		return l.T("error.client_not_paired")
	case strings.Contains(lower, "client revoked") || strings.Contains(lower, "client_revoke") || strings.Contains(lower, "client_revoked_or_missing"):
		return l.T("error.client_revoked")
	case strings.Contains(lower, "pairing expired"):
		return l.T("error.pairing_expired")
	case strings.Contains(lower, "pairing rejected"):
		return l.T("error.pairing_rejected")
	case strings.Contains(lower, "pairing approved but credentials missing"):
		return l.T("error.pairing_credentials_missing")
	case strings.Contains(lower, "irc_registered_nick_required"):
		return l.T("error.irc_registered_nick_required")
	case strings.Contains(lower, "sasl_auth_failed"):
		return l.T("error.sasl_auth_failed")
	case strings.Contains(lower, "nickserv identify failed"):
		return l.T("error.nickserv_identify_failed")
	case strings.Contains(lower, "nickserv register failed"):
		return l.T("error.nickserv_register_failed")
	case strings.Contains(lower, "nickserv_handshake_timeout"):
		return l.T("error.nickserv_handshake_timeout")
	case strings.Contains(lower, "irc_glined") || strings.Contains(lower, "irc_gline"):
		return l.T("error.irc_glined")
	case strings.Contains(lower, "irc_join_rejected") || strings.Contains(lower, "irc_error_during_channel_join") || strings.Contains(lower, "irc_join_send_failed"):
		return l.T("error.irc_join_failed")
	case strings.Contains(lower, "irc_connection_closed") || strings.Contains(lower, "irc_connection") && strings.Contains(lower, "failed"):
		return l.T("error.irc_connection_failed")
	case strings.Contains(lower, "xdcc_transfer_incomplete"):
		return l.T("error.xdcc_transfer_incomplete")
	case strings.Contains(lower, "xdcc_offer_timeout"):
		return l.T("error.xdcc_offer_timeout")
	case strings.Contains(lower, "filename_mismatch") || strings.Contains(lower, "filename mismatch"):
		return l.T("error.filename_mismatch")
	case strings.Contains(lower, "xdcc_missing_fields") || strings.Contains(lower, "missing instruction.") || strings.Contains(lower, "invalid irc.package"):
		return l.T("error.invalid_download_instruction")
	case strings.Contains(lower, "unsupported instruction.mode") || strings.Contains(lower, "unsupported_mode"):
		return l.T("error.unsupported_download_mode")
	case strings.Contains(lower, "empty_url"):
		return l.T("error.download_url_missing")
	case strings.Contains(lower, "empty_dest_path") || strings.Contains(lower, "missing output.filename_video"):
		return l.T("error.download_target_missing")
	case strings.Contains(lower, "http_404"):
		return l.T("error.http_404")
	case strings.Contains(lower, "http_") || strings.Contains(lower, "returned http"):
		if match := httpPattern.FindStringSubmatch(raw); len(match) == 2 {
			return l.T("error.http_download_failed_status", map[string]any{"Status": match[1]})
		}
		return l.T("error.download_failed")
	case strings.Contains(lower, "job_canceled"):
		return l.T("error.job_canceled")
	case strings.Contains(lower, "attempt_not_found") || strings.Contains(lower, "attempt_not_open"):
		return l.T("error.attempt_unavailable")
	case strings.Contains(lower, "client_paused"):
		return l.T("error.client_paused")
	case strings.Contains(lower, "client_shutdown"):
		return l.T("error.client_shutdown")
	case strings.Contains(lower, "server_base_url missing"):
		return l.T("error.server_url_missing")
	case strings.Contains(lower, "config path missing"):
		return l.T("error.config_path_missing")
	case strings.Contains(lower, "config missing") || strings.Contains(lower, "api client missing"):
		return l.T("error.config_missing")
	case strings.Contains(lower, "permission denied") || strings.Contains(lower, "access is denied"):
		return l.T("error.permission_denied")
	case strings.Contains(lower, "download path missing") || strings.Contains(lower, "download_dir missing"):
		return l.T("error.download_directory_missing")
	case strings.Contains(lower, "no public ip detection services configured") || strings.Contains(lower, "returned non-public ipv4"):
		return l.T("error.public_ip_detection_failed")
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "connection refused") || strings.Contains(lower, "network is unreachable"):
		return l.T("error.server_unreachable")
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "timeout"):
		return l.T("error.connection_timeout")
	default:
		return l.T("error.technical", map[string]any{"Detail": raw})
	}
}

func (l *Localizer) FormatTimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	if l.Language() == German {
		return t.Local().Format("02.01.2006 15:04:05")
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func (l *Localizer) FormatRemaining(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	d := time.Until(t)
	switch {
	case d <= 0:
		return l.T("time.expired")
	case d < time.Minute:
		return l.T("time.remaining_seconds", map[string]any{"Count": int(d.Seconds())})
	case d < time.Hour:
		return l.T("time.remaining_minutes", map[string]any{"Count": int(d.Minutes())})
	default:
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		if minutes == 0 {
			return l.T("time.remaining_hours", map[string]any{"Hours": hours})
		}
		return l.T("time.remaining_hours_minutes", map[string]any{"Hours": hours, "Minutes": minutes})
	}
}

func (l *Localizer) FormatDurationSeconds(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	d := time.Duration(seconds) * time.Second
	if d < time.Minute {
		return l.T("time.seconds_short", map[string]any{"Count": int(d.Seconds())})
	}
	if d < time.Hour {
		minutes := int(d.Minutes())
		secondsPart := int(d.Seconds()) % 60
		if secondsPart == 0 {
			return l.T("time.minutes_short", map[string]any{"Count": minutes})
		}
		return l.T("time.minutes_seconds_short", map[string]any{"Minutes": minutes, "Seconds": secondsPart})
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if minutes == 0 {
		return l.T("time.hours_short", map[string]any{"Count": hours})
	}
	return l.T("time.hours_minutes_short", map[string]any{"Hours": hours, "Minutes": minutes})
}

func loadCatalogs() {
	catalogsOnce.Do(func() {
		catalogs = make(map[string]catalog, 2)
		for _, language := range []string{German, English} {
			body, err := catalogFS.ReadFile("locales/" + language + ".json")
			if err != nil {
				catalogsErr = err
				return
			}
			var messages catalog
			if err := json.Unmarshal(body, &messages); err != nil {
				catalogsErr = fmt.Errorf("parse %s catalog: %w", language, err)
				return
			}
			catalogs[language] = messages
		}
	})
	if catalogsErr != nil {
		panic(catalogsErr)
	}
}

func lookup(language, key string) (string, bool) {
	loadCatalogs()
	messages, ok := catalogs[NormalizeLanguage(language)]
	if !ok {
		return "", false
	}
	message, ok := messages[key]
	return message, ok
}

// CatalogKeys returns a copy of the keys in a language catalog for tests and tooling.
func CatalogKeys(language string) map[string]struct{} {
	loadCatalogs()
	out := map[string]struct{}{}
	for key := range catalogs[NormalizeLanguage(language)] {
		out[key] = struct{}{}
	}
	return out
}
