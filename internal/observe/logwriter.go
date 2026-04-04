package observe

import "bytes"

type LogWriter struct {
	State *State
}

func (w *LogWriter) Write(p []byte) (int, error) {
	if w == nil || w.State == nil {
		return len(p), nil
	}
	for _, line := range bytes.Split(p, []byte{'\n'}) {
		w.State.AppendLog(string(line))
	}
	return len(p), nil
}
