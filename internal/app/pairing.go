package app

import (
	"context"
	"fmt"
	gort "runtime"
	"strings"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
	"github.com/autofetch-de/autofetch-client/internal/config"
)

func normalizeArch(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "amd64", "x86_64":
		return "amd64"
	case "386", "i386", "i686", "x86":
		return "386"
	case "arm64", "aarch64":
		return "arm64"
	case "arm":
		return "arm"
	default:
		return arch
	}
}

func runPairingFlow(cfg *config.Config, version string) error {
	apiClient := api.New(cfg.ServerBaseURL, "", "")
	apiClient.HTTP.Timeout = 60 * time.Second

	start, err := apiClient.RegisterStart(context.Background(), api.RegisterStartRequest{
		ClientName: cfg.ClientName,
		Platform:   gort.GOOS,
		Arch:       normalizeArch(gort.GOARCH),
		Version:    version,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Pairing code: %s\n", start.PairingCode)
	fmt.Printf("Open: %s/clients/new\n", strings.TrimRight(cfg.ServerBaseURL, "/"))

	pollEvery := time.Duration(start.PollAfterSeconds) * time.Second
	if pollEvery <= 0 {
		pollEvery = 3 * time.Second
	}

	for {
		res, err := apiClient.RegisterPoll(context.Background(), api.RegisterPollRequest{
			PairingID: start.PairingID,
		})
		if err != nil {
			return err
		}

		switch res.Status {
		case "PENDING":
			if res.PollAfterSeconds > 0 {
				pollEvery = time.Duration(res.PollAfterSeconds) * time.Second
			}
			time.Sleep(pollEvery)

		case "APPROVED":
			if strings.TrimSpace(res.ClientID) == "" || strings.TrimSpace(res.ClientToken) == "" {
				return fmt.Errorf("pairing approved but credentials missing")
			}

			now := time.Now().UTC()
			cfg.ClientID = res.ClientID
			cfg.ClientToken = res.ClientToken
			cfg.PairedAt = &now
			cfg.RevokedAt = nil

			return config.Persist(*cfg)

		case "EXPIRED":
			return fmt.Errorf("pairing expired")

		case "REJECTED":
			return fmt.Errorf("pairing rejected")

		default:
			return fmt.Errorf("unknown pairing status: %s", res.Status)
		}
	}
}
