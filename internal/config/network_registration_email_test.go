package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistrationEmailIsSecretAndPerNetwork(t *testing.T) {
	input := IRCConfig{Networks: []IRCNetwork{
		{Host: "irc.abjects.net", Port: 6697, TLS: true, Nick: "nickA", RegistrationEmail: "first@example.org", NickServ: IRCNickServ{Password: "pwA"}, Channels: []string{"#a"}},
		{Host: "irc.other.net", Port: 6697, TLS: true, Nick: "nickB", RegistrationEmail: "second@example.org", NickServ: IRCNickServ{Password: "pwB"}, Channels: []string{"#b"}},
	}}
	clean, secrets := splitIRCSecrets(input)
	if clean.Networks[0].RegistrationEmail != "" || clean.Networks[1].RegistrationEmail != "" {
		t.Fatalf("registration emails leaked into regular config: %+v", clean.Networks)
	}
	encoded, err := json.Marshal(clean)
	if err != nil || strings.Contains(string(encoded), "example.org") || strings.Contains(string(encoded), "pwA") {
		t.Fatalf("secrets in config: %s, %v", encoded, err)
	}
	restored := mergeIRCSecrets(clean, secrets)
	if restored.Networks[0].RegistrationEmail != "first@example.org" || restored.Networks[1].RegistrationEmail != "second@example.org" || restored.Networks[0].NickServ.Password != "pwA" || restored.Networks[1].NickServ.Password != "pwB" {
		t.Fatalf("wrong network-specific data after reload: %+v", restored.Networks)
	}
	// Clearing one address must not touch the other network or its password.
	restored.Networks[0].RegistrationEmail = ""
	clean, secrets = splitIRCSecrets(restored)
	restored = mergeIRCSecrets(clean, secrets)
	if restored.Networks[0].RegistrationEmail != "" || restored.Networks[1].RegistrationEmail != "second@example.org" || restored.Networks[0].NickServ.Password != "pwA" {
		t.Fatalf("clearing one address touched another setting: %+v", restored.Networks)
	}
}
