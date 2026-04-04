package download

import (
	"io"
	"time"
)

// RateLimitedReader throttles reads to roughly bytesPerSec.
// A value <= 0 means unlimited.
type RateLimitedReader struct {
	r           io.Reader
	bytesPerSec int64
	allowance   float64
	last        time.Time
}

func NewRateLimitedReader(r io.Reader, bytesPerSec int64) io.Reader {
	if bytesPerSec <= 0 {
		return r
	}
	return &RateLimitedReader{
		r:           r,
		bytesPerSec: bytesPerSec,
		allowance:   float64(bytesPerSec),
		last:        time.Now(),
	}
}

func (r *RateLimitedReader) Read(p []byte) (int, error) {
	for {
		now := time.Now()
		elapsed := now.Sub(r.last).Seconds()
		r.last = now
		r.allowance += elapsed * float64(r.bytesPerSec)
		maxAllowance := float64(r.bytesPerSec)
		if r.allowance > maxAllowance {
			r.allowance = maxAllowance
		}

		if r.allowance >= 1 {
			maxRead := int(r.allowance)
			if maxRead <= 0 {
				maxRead = 1
			}
			if maxRead < len(p) {
				p = p[:maxRead]
			}
			n, err := r.r.Read(p)
			r.allowance -= float64(n)
			if r.allowance < 0 {
				r.allowance = 0
			}
			return n, err
		}

		sleepFor := time.Duration((1 - r.allowance) / float64(r.bytesPerSec) * float64(time.Second))
		if sleepFor < time.Millisecond {
			sleepFor = time.Millisecond
		}
		time.Sleep(sleepFor)
	}
}
