package main

import (
	"fmt"
	"log"
	"os"

	"github.com/autofetch-de/autofetch-client/internal/app"
	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
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
	cfg := config.Load(l)
	if err := app.Run(cfg, info); err != nil {
		log.Printf("client stopped with error: %v", err)
		fmt.Fprintln(os.Stderr, l.UserError(err.Error()))
		os.Exit(1)
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
