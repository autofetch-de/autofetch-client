package main

import (
	"fmt"
	"log"
	"os"

	"github.com/autofetch-de/autofetch-client/internal/app"
	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
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
	cfg := config.Load()
	if err := app.Run(cfg, info); err != nil {
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
