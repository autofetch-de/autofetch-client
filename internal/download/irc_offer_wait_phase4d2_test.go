package download

import (
	"strconv"
	"testing"
	"time"
)

func TestOfferWaitAnnouncementsNeverExtendDeadline(t *testing.T) {
	start := time.Date(2026, 10, 29, 20, 54, 10, 0, time.UTC)
	wait := newIRCOfferWait(start, 120*time.Second)
	opt := XDCCOptions{Bot: "Oday-YEA02", Package: 845, DownloadMethod: "xdcc_numeric"}
	initial := wait.deadline
	for i := 4381; i <= 4385; i++ {
		line := ":oday-yea02!u@h PRIVMSG testnick :#" + strconv.Itoa(i) + " 1x [37M] [Electronic]_file.tar"
		if state := wait.observe(line, opt, "testnick"); state != "" {
			t.Fatalf("unrelated announcement changed state to %s", state)
		}
	}
	if wait.deadline != initial || wait.state() != "unconfirmed" {
		t.Fatalf("announcement extended unconfirmed deadline: %+v", wait)
	}
}

func TestOfferWaitAcceptOnlySelectedMatchingPack(t *testing.T) {
	start := time.Date(2026, 10, 29, 20, 54, 10, 0, time.UTC)
	opt := XDCCOptions{Bot: "Oday-YEA02", Package: 845, DownloadMethod: "xdcc_numeric"}
	wait := newIRCOfferWait(start, 120*time.Second)
	for _, line := range []string{
		":OtherBot!u@h NOTICE testnick :** Sending you pack #845 (\"file.tar\")",
		":Oday-YEA02!u@h NOTICE #channel :** Sending you pack #845 (\"file.tar\")",
		":Oday-YEA02!u@h NOTICE someoneelse :** Sending you pack #845 (\"file.tar\")",
		":Oday-YEA02!u@h NOTICE testnick :** Sending you pack #4381 (\"other.tar\")",
		":Oday-YEA02!u@h PRIVMSG testnick :#845 1x [35M] ourfile.tar",
	} {
		if got := wait.observe(line, opt, "testnick"); got != "" {
			t.Errorf("unrelated message treated as %s: %s", got, line)
		}
	}
	if wait.deadline != start.Add(120*time.Second) {
		t.Fatalf("unexpected extension: %s", wait.deadline)
	}
	line := ":Oday-YEA02!u@h NOTICE testnick :** Sending you pack #845 (\"file.tar\"), which is 137MB. (resume supported)"
	if got := wait.observe(line, opt, "testnick"); got != "accepted" {
		t.Fatalf("expected accepted, got %s", got)
	}
	if want := start.Add(180 * time.Second); wait.deadline != want {
		t.Fatalf("deadline %s, want %s", wait.deadline, want)
	}
	// Repeated acceptance does not create an unbounded sliding deadline.
	if got := wait.observe(line, opt, "testnick"); got != "" {
		t.Fatalf("repeated acceptance changed state: %s", got)
	}
	if wait.deadline != start.Add(180*time.Second) {
		t.Fatal("repeated acceptance extended deadline")
	}
}

func TestOfferWaitQueueIsBounded(t *testing.T) {
	start := time.Date(2026, 10, 29, 20, 54, 10, 0, time.UTC)
	opt := XDCCOptions{Bot: "Zombie-Ferel", Package: 191, DownloadMethod: "xdcc_numeric"}
	wait := newIRCOfferWait(start, 120*time.Second)
	if got := wait.observe(":Zombie-Ferel!u@h NOTICE testnick :You are queued, position 5", opt, "testnick"); got != "queued" {
		t.Fatalf("queue announcement not detected: %q", got)
	}
	if want := start.Add(15 * time.Minute); wait.deadline != want {
		t.Fatalf("unexpected queue deadline %s want %s", wait.deadline, want)
	}
	for _, line := range []string{
		":Zombie-Ferel!u@h NOTICE testnick :You are queued, position 4",
		":OtherBot!u@h NOTICE testnick :You are queued, position 3",
		":Zombie-Ferel!u@h NOTICE #zombie-warez :All slots are full",
		":Zombie-Ferel!u@h NOTICE testnick :Sending you pack #191 (\"file.tar\")",
	} {
		if got := wait.observe(line, opt, "testnick"); got != "" {
			t.Fatalf("existing queue extended by %q: %s", line, got)
		}
	}
	if want := start.Add(15 * time.Minute); wait.deadline != want {
		t.Fatal("queue deadline extended by repeated announcements")
	}
}

func TestOfferWaitCommandRequiresExactSelectedFilename(t *testing.T) {
	start := time.Date(2026, 10, 29, 20, 54, 10, 0, time.UTC)
	opt := XDCCOptions{Bot: "Wench", DownloadMethod: "irc_command", ExpectedFilename: "name with spaces.epub"}
	wait := newIRCOfferWait(start, 120*time.Second)
	if got := wait.observe(":Wench!u@h NOTICE nick :Sending you pack #123 (\"unrelated.epub\")", opt, "nick"); got != "" {
		t.Fatal("numeric announcement must not confirm unrelated command")
	}
	if got := wait.observe(":Wench!u@h NOTICE nick :Sending you name with spaces.epub", opt, "nick"); got != "accepted" {
		t.Fatalf("matching command acknowledgement: %s", got)
	}
}

func TestOfferWaitShortFixtureTimeoutScales(t *testing.T) {
	start := time.Now()
	wait := newIRCOfferWait(start, 2*time.Second)
	opt := XDCCOptions{Bot: "BotA", Package: 7, DownloadMethod: "xdcc_numeric"}
	wait.observe(":BotA!u@h NOTICE nick :Sending you pack #7", opt, "nick")
	if want := start.Add(3 * time.Second); wait.deadline != want {
		t.Fatalf("short fixture timeout not respected: %s", wait.deadline.Sub(start))
	}
}
