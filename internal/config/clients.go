package config

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
)

// ClientsConfig is an optional config file living next to client.json.
// It is meant for user-editable settings that are not credentials.
//
// Default path: ~/.config/autofetch/clients.json
// Example:
//
//	{"irc_nick": "myNick"}
//
// Environment override:
//
//	IRC_NICK
type ClientsConfig struct {
	IRCNick string `json:"irc_nick"`
}

func defaultClientsConfigPath() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return "./clients.json"
	}
	return filepath.Join(base, "autofetch", "clients.json")
}

func LoadClientsConfig() ClientsConfig {
	cc := ClientsConfig{}
	path := defaultClientsConfigPath()

	if bs, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(bs, &cc)
	} else if err != nil {
		// Ignore missing file.
		_ = errorsIs(err, fs.ErrNotExist)
	}

	if v := os.Getenv("IRC_NICK"); v != "" {
		cc.IRCNick = v
	}

	return cc
}
