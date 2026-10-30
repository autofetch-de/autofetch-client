package download

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The timeout belongs to the *request*, not to the number of messages received.
// Unrelated pack announcements must never keep an XDCC request alive forever.
// With the default 120-second window, a confirmed request has 180 seconds and
// an explicitly queued request has at most 15 minutes from initial submission.
// Shorter caller-supplied windows (e.g. for fixture tests) scale proportionally.
type ircOfferWait struct {
	started  time.Time
	deadline time.Time
	accepted bool
	queued   bool
	base     time.Duration
}

func newIRCOfferWait(started time.Time, base time.Duration) ircOfferWait {
	if base <= 0 {
		base = 120 * time.Second
	}
	return ircOfferWait{started: started, deadline: started.Add(base), base: base}
}

func (w *ircOfferWait) state() string {
	if w.queued {
		return "queued"
	}
	if w.accepted {
		return "accepted"
	}
	return "unconfirmed"
}

var sendingPackNumber = regexp.MustCompile(`(?i)\bsending\s+you\s+pack\s+#(\d+)\b`)

// Only direct NOTICE/PRIVMSG messages from our selected bot and addressed to
// our negotiated nick can change the wait policy. This excludes announcements
// to the channel and messages from another bot.
func selectedBotDirectMessage(line, bot, nick string) (string, bool) {
	fields := strings.SplitN(line, " ", 4)
	if len(fields) != 4 || !strings.HasPrefix(fields[0], ":") ||
		!(strings.EqualFold(fields[1], "NOTICE") || strings.EqualFold(fields[1], "PRIVMSG")) ||
		!strings.EqualFold(ircPrefixNick(fields[0][1:]), strings.TrimSpace(bot)) ||
		!strings.EqualFold(fields[2], strings.TrimSpace(nick)) {
		return "", false
	}
	_, _, trailing := parseIRCLine(line)
	return strings.TrimSpace(stripIRCFormatting(trailing)), true
}

// observe may advance a deadline once per state transition; duplicate queue
// or acceptance messages never renew it. It returns the new state if changed.
func (w *ircOfferWait) observe(line string, opt XDCCOptions, nick string) string {
	message, ok := selectedBotDirectMessage(line, opt.Bot, nick)
	if !ok {
		return ""
	}
	lower := strings.ToLower(strings.Join(strings.Fields(message), " "))
	queued := strings.Contains(lower, "you are queued") ||
		strings.Contains(lower, "you have been queued") ||
		strings.Contains(lower, "you are in queue") ||
		strings.Contains(lower, "your pack is queued") ||
		strings.Contains(lower, "added to queue") ||
		strings.Contains(lower, "queue position") ||
		strings.Contains(lower, "all slots are full")
	if queued && !w.queued {
		w.queued = true
		limit := w.started.Add(w.base * 15 / 2)
		if limit.After(w.deadline) {
			w.deadline = limit
		}
		return "queued"
	}
	if w.accepted || w.queued {
		return ""
	}

	accepted := false
	if pack := sendingPackNumber.FindStringSubmatch(message); len(pack) == 2 {
		n, err := strconv.Atoi(pack[1])
		accepted = err == nil && opt.DownloadMethod != "irc_command" && n == opt.Package
	} else if opt.DownloadMethod == "irc_command" {
		// A command-based bot might not use numeric packs. Do not accept a
		// generic announcement as an acknowledgement: require its selected
		// filename together with an explicit "sending you" response.
		accepted = strings.Contains(lower, "sending you") &&
			strings.Contains(message, opt.ExpectedFilename) && opt.ExpectedFilename != ""
	}
	if accepted {
		w.accepted = true
		limit := w.started.Add(w.base * 3 / 2)
		if limit.After(w.deadline) {
			w.deadline = limit
		}
		return "accepted"
	}
	return ""
}
