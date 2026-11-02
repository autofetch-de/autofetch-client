package irc

import (
	"reflect"
	"testing"

	"github.com/autofetch-de/autofetch-client/internal/config"
)

func TestRememberJobChannelsIsInformationalAndDeduplicates(t *testing.T) {
	n := &config.IRCNetwork{Channels: []string{"#moviegods"}}
	if !RememberJobChannels(n, []string{"#mg-chat", "#moviegods", "#MG-CHAT", "", "other"}) {
		t.Fatal("new server-provided prerequisite channels not recorded")
	}
	if want := []string{"#moviegods", "#mg-chat", "#other"}; !reflect.DeepEqual(n.Channels, want) {
		t.Fatalf("channels=%v, want=%v", n.Channels, want)
	}
	if RememberJobChannels(n, []string{"#MG-CHAT", "#OTHER"}) {
		t.Fatal("duplicate channels should not trigger a settings write")
	}
}
