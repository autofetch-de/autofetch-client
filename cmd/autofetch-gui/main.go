package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/autofetch-de/autofetch-client/internal/app"
	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
	"github.com/autofetch-de/autofetch-client/internal/desktop"
	"github.com/autofetch-de/autofetch-client/internal/instance"
)

func main() {
	info := buildinfo.Current()
	if hasVersionFlag(os.Args[1:]) {
		fmt.Println(info.VersionText())
		return
	}
	for _, line := range info.StartLogLines() {
		log.Print(line)
	}
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
	svc, _, err := app.Bootstrap(&cfg, info)
	if err != nil {
		log.Fatal(err)
	}
	if err := desktop.Run(context.Background(), svc); err != nil {
		log.Fatal(err)
	}
}
func hasVersionFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--version" || arg == "-version" {
			return true
		}
	}
	return false
}
