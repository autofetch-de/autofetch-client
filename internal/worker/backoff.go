package worker

import (
	"math/rand"
	"time"
)

type Backoff struct {
	Min time.Duration
	Max time.Duration
	cur time.Duration
}

func (b *Backoff) Next() time.Duration {
	if b.Min == 0 {
		b.Min = 500 * time.Millisecond
	}
	if b.Max == 0 {
		b.Max = 30 * time.Second
	}

	if b.cur == 0 {
		b.cur = b.Min
	} else {
		b.cur *= 2
		if b.cur > b.Max {
			b.cur = b.Max
		}
	}

	j := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(b.cur) * j)
}

func (b *Backoff) Reset() { b.cur = 0 }
