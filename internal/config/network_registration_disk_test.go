package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/nickservstate"
)

func TestNetworkRegistrationSavedToDiskAndRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	original := Config{ConfigPath: path, IRC: IRCConfig{Networks: []IRCNetwork{
		{Host: "irc.abjects.net", Port: 6697, TLS: true, Nick: "first", RegistrationEmail: "first@example.org", NickServ: IRCNickServ{Password: "private-a", RegistrationPending: true, RegistrationStatus: nickservstate.AwaitingConfirmation}},
		{Host: "irc.other.net", Port: 6697, TLS: true, Nick: "second", RegistrationEmail: "second@example.org", NickServ: IRCNickServ{Password: "private-b", RegistrationPending: true, RegistrationStatus: nickservstate.Failed}},
	}}}
	if err := Persist(original); err != nil {
		t.Fatal(err)
	}
	publicFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicFile), "@example.org") || strings.Contains(string(publicFile), "private-a") || strings.Contains(string(publicFile), "private-b") {
		t.Fatal("secrets leaked to public client.json")
	}
	privateFile, err := os.ReadFile(filepath.Join(filepath.Dir(path), "irc-secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(privateFile), "first@example.org") || !strings.Contains(string(privateFile), "second@example.org") {
		t.Fatal("network-specific registration emails not written to disk")
	}
	restored := Config{ConfigPath: path}
	if err := loadFromFile(&restored, path); err != nil {
		t.Fatal(err)
	}
	a, b := restored.IRC.Networks[0], restored.IRC.Networks[1]
	if a.RegistrationEmail != "first@example.org" || b.RegistrationEmail != "second@example.org" || a.NickServ.Password != "private-a" || b.NickServ.Password != "private-b" || !a.NickServ.RegistrationPending || !b.NickServ.RegistrationPending || a.NickServ.RegistrationStatus != nickservstate.AwaitingConfirmation || b.NickServ.RegistrationStatus != nickservstate.Failed {
		t.Fatal("persist/load did not restore per-network NickServ registration")
	}
}
