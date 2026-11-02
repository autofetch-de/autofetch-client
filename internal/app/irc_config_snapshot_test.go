package app

import (
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/buildinfo"
	"github.com/autofetch-de/autofetch-client/internal/config"
)

func TestIRCConfigReturnsIndependentNetworkAndChannelSlices(t *testing.T) {
	cfg := config.Config{IRC: config.IRCConfig{Networks: []config.IRCNetwork{{
		Host: "irc.example.net", Nick: "saved-nick", Channels: []string{"#server-job"},
		RegistrationEmail: "private@example.org", NickServ: config.IRCNickServ{Password: "saved-password"},
	}}}}
	svc := NewService(&cfg, buildinfo.Info{}, nil, nil, nil)
	snapshot := svc.IRCConfig()
	snapshot.Networks[0].Nick = "changed-nick"
	snapshot.Networks[0].Channels[0] = "#changed"
	snapshot.Networks[0].RegistrationEmail = "changed@example.org"
	snapshot.Networks[0].NickServ.Password = "changed-password"
	original := svc.IRCConfig().Networks[0]
	if original.Nick != "saved-nick" || original.Channels[0] != "#server-job" || original.RegistrationEmail != "private@example.org" || original.NickServ.Password != "saved-password" {
		t.Fatalf("reading/editing settings mutated active configuration without saving: %+v", original)
	}
}
