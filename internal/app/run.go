package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"syscall"

	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/localization"
	"github.com/autofetch-de/autofetch-client/internal/webui"
	"github.com/autofetch-de/autofetch-client/internal/worker"
)

func Run(cfg config.Config, info buildinfo.Info) error {
	l := localization.New(info.Language)
	service, _, err := Bootstrap(&cfg, info)
	if err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go service.RunUpdateChecker(ctx)

	if cfg.EnableWebUI {
		ui, err := webui.NewWithOptions(cfg.WebUIListenAddr, service.state, service, l, webui.AccessOptions{
			PasswordFile: cfg.WebUIPasswordFile, AllowUnauthenticatedLAN: cfg.WebUILANNoPassword,
		})
		if err != nil {
			return fmt.Errorf("local ui configuration failed: %w", err)
		}
		if err := ui.Start(); err != nil {
			return fmt.Errorf("local ui start failed: %w", err)
		}
		log.Printf("local status UI listening on %s", ui.URL())
		if cfg.OpenBrowser && !cfg.Headless {
			if err := webui.OpenBrowser(ui.URL()); err != nil {
				log.Printf("could not open browser: %v", err)
			}
		}
		if cfg.AuthPausedAt != nil {
			log.Printf("client paused due to repeated credential rejection; use re-pair to resume")
		} else if err := service.Start(); err != nil {
			return fmt.Errorf("service start failed: %w", err)
		}
		<-ctx.Done()
		_ = service.Shutdown()
		// SIGINT should not terminate the process before the runner has
		// released its lease with a fresh, bounded completion request.
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 8*time.Second)
		_ = service.WaitStopped(waitCtx)
		waitCancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ui.Shutdown(shutdownCtx)
		return nil
	}

	if cfg.ClientID == "" || cfg.ClientToken == "" {
		if err := runPairingFlow(&cfg, info, l); err != nil {
			return fmt.Errorf("pairing failed: %w", err)
		}
		service.api.ClientID = cfg.ClientID
		service.api.ClientToken = cfg.ClientToken
	}

	if cfg.AuthPausedAt != nil {
		log.Printf("client paused due to repeated credential rejection; restart with --re-pair to resume")
		<-ctx.Done() // Keep systemd from immediately restarting a stopped client.
		return nil
	}
	r := service.factory(service.state)
	if err := r.Run(ctx); errors.Is(err, worker.ErrClientAuthSuspended) {
		now := time.Now().UTC()
		cfg.AuthPausedAt = &now
		if persistErr := config.Persist(cfg); persistErr != nil {
			log.Printf("[error] could not persist authentication pause: %v", persistErr)
		}
		log.Printf("client paused: invalid credentials repeatedly rejected; restart with --re-pair")
		<-ctx.Done() // Never flood the server after an automatic service restart.
		return nil
	} else if errors.Is(err, worker.ErrClientRevoked) {
		now := time.Now().UTC()
		config.MarkRevoked(&cfg, now)
		cfg.AuthPausedAt = &now
		if persistErr := config.Persist(cfg); persistErr != nil {
			log.Printf("[error] could not persist revoked-client pause: %v", persistErr)
		}
		log.Printf("client paused: credentials revoked by server; restart with --re-pair")
		<-ctx.Done()
		return nil
	} else if err != nil && err != context.Canceled {
		return fmt.Errorf("runner exited: %w", err)
	}
	return nil
}
