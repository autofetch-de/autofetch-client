package download

import (
	"io"
	"sync/atomic"
	"time"
)

type Progress struct {
	Downloaded atomic.Int64
	Expected   int64
	Start      time.Time
}

func NewProgress(expected int64) *Progress {
	return &Progress{Expected: expected, Start: time.Now()}
}

type countingWriter struct{ p *Progress }

func (w *countingWriter) Write(b []byte) (int, error) {
	n := len(b)
	w.p.Downloaded.Add(int64(n))
	return n, nil
}

func TeeReader(r io.Reader, p *Progress) io.Reader {
	return io.TeeReader(r, &countingWriter{p: p})
}
