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
	"github.com/autofetch-de/autofetch-client/internal/localization"
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
	l := localization.New(info.Language)
	lock, err := instance.Acquire("autofetch-gui")
	if err != nil {
		if errors.Is(err, instance.ErrAlreadyRunning) {
			log.Fatal(l.T("cli.gui_already_running"))
		}
		log.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	cfg := config.Load(l)
	cfg.EnableWebUI = false
	cfg.OpenBrowser = false
	cfg.NoBrowser = true
	cfg.Headless = false
	svc, _, err := app.Bootstrap(&cfg, info)
	if err != nil {
		log.Printf("client bootstrap failed: %v", err)
		log.Fatal(l.UserError(err.Error()))
	}
	if err := desktop.Run(context.Background(), svc); err != nil {
		log.Printf("desktop client stopped with error: %v", err)
		log.Fatal(l.UserError(err.Error()))
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
