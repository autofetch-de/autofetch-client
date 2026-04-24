package main

import (
	"context"
	"errors"
	"log"

	"github.com/autofetch-de/autofetch-client/internal/app"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/desktop"
	"github.com/autofetch-de/autofetch-client/internal/instance"
)

// Version can be overridden at build time with -ldflags "-X main.Version=...".
var Version = "0.1.0"

func main() {
	lock, err := instance.Acquire("autofetch-gui")
	if err != nil {
		if errors.Is(err, instance.ErrAlreadyRunning) {
			log.Fatal("autofetch-gui läuft bereits")
		}
		log.Fatal(err)
	}
	defer func() { _ = lock.Release() }()

	cfg := config.Load()
	cfg.EnableWebUI = false
	cfg.OpenBrowser = false
	cfg.NoBrowser = true
	cfg.Headless = false

	svc, _, err := app.Bootstrap(&cfg, Version)
	if err != nil {
		log.Fatal(err)
	}
	if err := desktop.Run(context.Background(), svc); err != nil {
		log.Fatal(err)
	}
}
