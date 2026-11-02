package download

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestIRCLogLevelRoutineNoticesStayDebugOnly(t *testing.T) {
	previous := log.Writer()
	defer log.SetOutput(previous)
	var output bytes.Buffer
	log.SetOutput(&output)
	t.Setenv("AUTOFETCH_IRC_DEBUG", "0")
	logRelevantIRCLine("channel_join", ":server NOTICE nick :*** Checking ident...", "bot")
	logRelevantIRCLine("channel_join", ":server 353 nick = #channel :a b c", "bot")
	logRelevantIRCLine("waiting_for_offer", ":bot!user@host PRIVMSG nick :File list", "bot")
	if output.Len() != 0 {
		t.Fatalf("routine IRC traffic logged without debug: %q", output.String())
	}
	logRelevantIRCLine("channel_join", "ERROR :Closing Link: banned", "bot")
	if !strings.Contains(output.String(), "IRC ERROR") {
		t.Fatal("IRC ERROR must stay visible")
	}
	output.Reset()
	t.Setenv("AUTOFETCH_IRC_DEBUG", "1")
	logRelevantIRCLine("channel_join", ":server NOTICE nick :*** Checking ident...", "bot")
	logRelevantIRCLine("channel_join", ":server 353 nick = #channel :a b c", "bot")
	if !strings.Contains(output.String(), "Checking ident") || !strings.Contains(output.String(), "353") {
		t.Fatalf("debug must include IRC traffic: %q", output.String())
	}
}
