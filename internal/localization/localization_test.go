package localization

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"text/template"
)

func TestCatalogsHaveSameKeys(t *testing.T) {
	de := CatalogKeys(German)
	en := CatalogKeys(English)
	if !reflect.DeepEqual(de, en) {
		for key := range de {
			if _, ok := en[key]; !ok {
				t.Errorf("missing English translation: %s", key)
			}
		}
		for key := range en {
			if _, ok := de[key]; !ok {
				t.Errorf("missing German translation: %s", key)
			}
		}
	}
}

func TestMissingKeyDoesNotLeakKey(t *testing.T) {
	key := "missing.secret.translation.key"
	got := New(German).T(key)
	if strings.Contains(got, key) {
		t.Fatalf("translation key leaked to user: %q", got)
	}
}

func TestTemplateRendering(t *testing.T) {
	got := New(English).T("pairing.valid_until_no_remaining", map[string]any{"Expires": "2026-07-21"})
	if got != "Valid until 2026-07-21" {
		t.Fatalf("unexpected translation: %q", got)
	}
}

func TestUserErrorKnownCode(t *testing.T) {
	got := New(English).UserError("reverse_dcc_port_forward_required: port=36080")
	if !strings.Contains(got, "36080") || strings.Contains(got, "reverse_dcc") {
		t.Fatalf("unexpected user error: %q", got)
	}
}

func TestCatalogKeysMatchSourceUsage(t *testing.T) {
	root := repositoryRoot(t)
	used := map[string]struct{}{}
	translationCall := regexp.MustCompile(`\.T\(\s*"([^"]+)"`)
	templateCall := regexp.MustCompile(`call \.T "([^"]+)"`)
	statusValue := regexp.MustCompile(`Status[A-Za-z0-9_]+\s*=\s*"([a-z0-9_]+)"`)

	for _, subtree := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, subtree), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, match := range translationCall.FindAllSubmatch(body, -1) {
				used[string(match[1])] = struct{}{}
			}
			for _, match := range templateCall.FindAllSubmatch(body, -1) {
				used[string(match[1])] = struct{}{}
			}
			if filepath.Base(path) == "state.go" && strings.Contains(filepath.ToSlash(path), "/internal/observe/") {
				for _, match := range statusValue.FindAllSubmatch(body, -1) {
					used["status_code."+string(match[1])] = struct{}{}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", subtree, err)
		}
	}

	catalog := CatalogKeys(English)
	for key := range used {
		if _, ok := catalog[key]; !ok {
			t.Errorf("translation key used in source but missing from catalogs: %s", key)
		}
	}
	for key := range catalog {
		if _, ok := used[key]; !ok {
			t.Errorf("translation key present but unused: %s", key)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func TestCatalogTemplateParametersMatch(t *testing.T) {
	loadCatalogs()
	parameter := regexp.MustCompile(`{{\s*\.([A-Za-z0-9_]+)\s*}}`)
	for key, german := range catalogs[German] {
		english := catalogs[English][key]
		germanParameters := parameterSet(parameter, german)
		englishParameters := parameterSet(parameter, english)
		if !reflect.DeepEqual(germanParameters, englishParameters) {
			t.Errorf("template parameters differ for %s: de=%v en=%v", key, germanParameters, englishParameters)
		}
		for language, message := range map[string]string{German: german, English: english} {
			if _, err := template.New(key).Option("missingkey=error").Parse(message); err != nil {
				t.Errorf("invalid %s template %s: %v", language, key, err)
			}
		}
	}
}

func parameterSet(pattern *regexp.Regexp, message string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, match := range pattern.FindAllStringSubmatch(message, -1) {
		out[match[1]] = struct{}{}
	}
	return out
}

func TestUserErrorPreservesTechnicalCodeOnlyInLogs(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		contains string
	}{
		{name: "IRC connection", raw: "irc_connection_closed_during_handshake: EOF", contains: "IRC network"},
		{name: "Incomplete transfer", raw: "xdcc_transfer_incomplete", contains: "complete file"},
		{name: "HTTP status", raw: "http_503", contains: "503"},
		{name: "Invalid instruction", raw: "missing instruction.video_url", contains: "invalid information"},
	}
	localizer := New(English)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := localizer.UserError(tt.raw)
			if !strings.Contains(got, tt.contains) {
				t.Fatalf("expected %q in %q", tt.contains, got)
			}
			if strings.Contains(got, tt.raw) {
				t.Fatalf("technical error leaked unchanged: %q", got)
			}
		})
	}
}
