package update

import (
	"context"
	"fmt"
	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompareStable(t *testing.T) {
	for _, tt := range []struct {
		a, b     string
		expected int
		valid    bool
	}{
		{"1.1.0", "1.1.1", -1, true}, {"v1.2.0", "1.1.9", 1, true}, {"1.1.0", "1.1.0", 0, true},
		{"1.0.1-16-g736214a", "1.1.0", 0, false}, {"dev", "1.1.0", 0, false}, {"1.1.0-rc.1", "1.1.0", 0, false},
	} {
		got, ok := CompareStable(tt.a, tt.b)
		if got != tt.expected || ok != tt.valid {
			t.Fatalf("%q %q: %d %v", tt.a, tt.b, got, ok)
		}
	}
}
func TestCheckValidReleaseAndDevBuild(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != endpoint || r.URL.Query().Get("platform") != "linux" || r.URL.Query().Get("variant") != "headless" {
			t.Errorf("request %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"1.2.0","download_url":"/downloads/autofetch-headless-linux-amd64-de-v1.2.0"}`)
	}))
	defer server.Close()
	info := buildinfo.Info{Version: "1.1.0", Platform: "linux", Arch: "amd64", Variant: "headless", Language: "de"}
	got, err := Check(context.Background(), server.URL, info, server.Client())
	if err != nil || !got.Available {
		t.Fatalf("%+v: %v", got, err)
	}
	if got.DownloadURL != server.URL+"/downloads/autofetch-headless-linux-amd64-de-v1.2.0" {
		t.Fatal(got.DownloadURL)
	}
	info.Version = "1.0.1-16-g736214a"
	got, err = Check(context.Background(), server.URL, info, server.Client())
	if err != nil || got.Available || !got.DevelopmentBuild {
		t.Fatalf("%+v: %v", got, err)
	}
}
func TestCheckRejectsInvalidResponseAndRedirect(t *testing.T) {
	for _, tt := range []struct {
		code int
		body string
	}{
		{200, `{"version":"1.2.0","download_url":"https://evil.test/client"}`},
		{200, `{"version":"1.2.0","download_url":"/downloads/../evil"}`},
		{302, ""}, {503, ""},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tt.code == 302 {
				w.Header().Set("Location", "https://evil.test/")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tt.code)
			fmt.Fprint(w, tt.body)
		}))
		_, err := Check(context.Background(), server.URL, buildinfo.Info{Version: "1.0.0"}, server.Client())
		server.Close()
		if err == nil {
			t.Fatalf("expected error %+v", tt)
		}
	}
}

func TestMacAndArmPlatformMapping(t *testing.T) {
	for _, tc := range []struct{ platform, arch, expectedPlatform, expectedArch string }{
		{"darwin", "arm64", "macos", "arm64"}, {"linux", "arm", "linux", "armv7"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("platform") != tc.expectedPlatform || r.URL.Query().Get("arch") != tc.expectedArch {
				t.Errorf("mapping %s %s: %s", tc.platform, tc.arch, r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"version":"1.1.0","download_url":"/downloads/test-v1.1.0"}`)
		}))
		_, err := Check(context.Background(), server.URL, buildinfo.Info{Version: "1.0.0", Platform: tc.platform, Arch: tc.arch, Variant: "headless", Language: "de"}, nil)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
