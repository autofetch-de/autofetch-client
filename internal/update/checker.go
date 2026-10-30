// Package update checks the autofetch server's published release information.
// It never downloads or installs executables.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
)

const CheckInterval = 6 * time.Hour
const endpoint = "/api/client-release/latest"

var stableVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

type Status struct {
	Available        bool      `json:"available"`
	Installed        string    `json:"installed_version"`
	Latest           string    `json:"latest_version,omitempty"`
	DownloadURL      string    `json:"download_url,omitempty"`
	CheckedAt        time.Time `json:"checked_at,omitempty"`
	DevelopmentBuild bool      `json:"development_build,omitempty"`
}

type releaseResponse struct {
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
}

func CompareStable(installed, latest string) (int, bool) {
	a, b := stableVersion.FindStringSubmatch(strings.TrimSpace(installed)), stableVersion.FindStringSubmatch(strings.TrimSpace(latest))
	if a == nil || b == nil {
		return 0, false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.ParseUint(a[i], 10, 32)
		y, _ := strconv.ParseUint(b[i], 10, 32)
		if x < y {
			return -1, true
		}
		if x > y {
			return 1, true
		}
	}
	return 0, true
}

// Check consults the user's configured autofetch server, never a third-party
// endpoint. Insecure HTTP is accepted only for local loopback development.
func Check(ctx context.Context, baseURL string, info buildinfo.Info, client *http.Client) (Status, error) {
	result := Status{Installed: info.Version}
	if !stableVersion.MatchString(strings.TrimSpace(info.Version)) {
		result.DevelopmentBuild = true
	}
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return result, errors.New("invalid update server URL")
	}
	if base.Scheme != "https" {
		host := base.Hostname()
		ip := net.ParseIP(host)
		if base.Scheme != "http" || !(host == "localhost" || (ip != nil && ip.IsLoopback())) {
			return result, errors.New("update server must use HTTPS")
		}
	}
	u := *base
	u.Path = endpoint
	q := url.Values{}
	platform := strings.ToLower(strings.TrimSpace(info.Platform))
	if platform == "darwin" {
		platform = "macos"
	}
	arch := strings.ToLower(strings.TrimSpace(info.Arch))
	if arch == "arm" {
		arch = "armv7"
	}
	q.Set("platform", platform)
	q.Set("arch", arch)
	q.Set("variant", info.Variant)
	q.Set("lang", info.Language)
	u.RawQuery = q.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return result, err
	}
	request.Header.Set("Accept", "application/json")
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	safeClient := *client
	safeClient.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safeClient.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("update check status %d", response.StatusCode)
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		return result, errors.New("invalid update response content type")
	}
	var release releaseResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
	if err := decoder.Decode(&release); err != nil {
		return result, err
	}
	if !stableVersion.MatchString(release.Version) {
		return result, errors.New("invalid published version")
	}
	link, err := url.Parse(release.DownloadURL)
	if err != nil || link.IsAbs() || link.Host != "" || !strings.HasPrefix(link.Path, "/downloads/") || link.RawQuery != "" || link.Fragment != "" || strings.Contains(link.EscapedPath(), "%") || strings.Contains(link.Path, "..") {
		return result, errors.New("invalid published artifact path")
	}
	result.Latest = strings.TrimPrefix(release.Version, "v")
	result.DownloadURL = base.Scheme + "://" + base.Host + link.Path
	result.CheckedAt = time.Now().UTC()
	if cmp, ok := CompareStable(info.Version, release.Version); ok && cmp < 0 {
		result.Available = true
	}
	return result, nil
}
