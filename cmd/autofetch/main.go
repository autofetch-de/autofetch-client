package main

import (
	"log"

	"github.com/autofetch-de/autofetch-client/internal/app"
	"github.com/autofetch-de/autofetch-client/internal/config"
)

// Version can be overridden at build time with -ldflags "-X main.Version=...".
var Version = "0.1.0"

func main() {
	cfg := config.Load()
	if err := app.Run(cfg, Version); err != nil {
		log.Fatal(err)
	}
}
