package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/autofetch-de/autofetch-client/internal/api"
	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/localization"
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

func runPairingFlow(cfg *config.Config, info buildinfo.Info, l *localization.Localizer) error {
	if l == nil {
		l = localization.New(info.Language)
	}
	apiClient := api.New(cfg.ServerBaseURL, "", "")
	apiClient.HTTP.Timeout = 60 * time.Second

	start, err := apiClient.RegisterStart(context.Background(), api.RegisterStartRequest{
		ClientName:  cfg.ClientName,
		Platform:    info.Platform,
		Arch:        info.Arch,
		Version:     info.Version,
		Variant:     info.Variant,
		BuildCommit: info.BuildCommit,
		BuildDate:   info.BuildDate,
	})
	if err != nil {
		return err
	}

	if cfg.PrintCodeOnly {
		fmt.Println(start.PairingCode)
	} else {
		fmt.Println(l.T("cli.pairing_code", map[string]any{"Code": start.PairingCode}))
		fmt.Println(l.T("cli.open", map[string]any{"URL": strings.TrimRight(cfg.ServerBaseURL, "/") + "/clients/new"}))
	}

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
