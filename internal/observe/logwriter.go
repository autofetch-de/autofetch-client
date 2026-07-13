package observe

import (
	"bytes"
	"strings"
)

type LogWriter struct {
	State *State
	Debug bool
}

func (w *LogWriter) Write(p []byte) (int, error) {
	if w == nil || w.State == nil {
		return len(p), nil
	}
	for _, line := range bytes.Split(p, []byte{'\n'}) {
		text := string(line)
		if !w.Debug && isNoisyLogLine(text) {
			continue
		}
		w.State.AppendLog(text)
	}
	return len(p), nil
}

func isNoisyLogLine(line string) bool {
	l := strings.ToLower(strings.TrimSpace(line))
	if l == "" {
		return false
	}
	noisy := []string{
		"irc job:",
		"joining channel:",
		"attempting nickserv",
		"initial nickserv identify succeeded",
		"nickserv identify acknowledged",
		"nickserv marked nick as",
		"nickserv register acknowledged",
		"nickserv register succeeded",
		"retrying nickserv",
		"retrying xdcc request after successful identification",
		"complete retry",
		"runtime config refresh failed",
		"persist selected irc nick failed",
		"persist irc network config failed",
		"persist irc registration credentials failed",
	}
	for _, n := range noisy {
		if strings.Contains(l, n) {
			return true
		}
	}
	return false
}
