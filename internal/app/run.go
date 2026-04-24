package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"syscall"

	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/webui"
	"github.com/autofetch-de/autofetch-client/internal/worker"
)

func Run(cfg config.Config, version string) error {
	service, _, err := Bootstrap(&cfg, version)
	if err != nil {
		log.Print(err)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.EnableWebUI && !cfg.Headless {
		ui := webui.New(cfg.WebUIListenAddr, service.state, service)
		if err := ui.Start(); err != nil {
			return fmt.Errorf("local ui start failed: %w", err)
		}
		log.Printf("local status UI listening on %s", ui.URL())
		if cfg.OpenBrowser {
			if err := webui.OpenBrowser(ui.URL()); err != nil {
				log.Printf("could not open browser: %v", err)
			}
		}
		if err := service.Start(); err != nil {
			return fmt.Errorf("service start failed: %w", err)
		}
		<-ctx.Done()
		_ = service.Shutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ui.Shutdown(shutdownCtx)
		return nil
	}

	if cfg.ClientID == "" || cfg.ClientToken == "" {
		if err := runPairingFlow(&cfg, version); err != nil {
			return fmt.Errorf("pairing failed: %w", err)
		}
		service.api.ClientID = cfg.ClientID
		service.api.ClientToken = cfg.ClientToken
	}

	r := service.factory(service.state)
	if err := r.Run(ctx); err != nil && err != context.Canceled {
		if err == worker.ErrClientRevoked {
			now := time.Now().UTC()
			config.MarkRevoked(&cfg, now)
			_ = config.Persist(cfg)
			log.Print("[fatal] client revoked or deleted by server – exiting")
			return nil
		}
		return fmt.Errorf("runner exited: %w", err)
	}
	return nil
}
