package burnrate

import "time"

type sample struct {
	t      time.Time
	tokens int
}

type Buffer struct {
	window  time.Duration
	samples []sample
}

func New(window time.Duration) *Buffer {
	return &Buffer{window: window}
}

func (b *Buffer) Add(t time.Time, tokens int) {
	b.samples = append(b.samples, sample{t: t, tokens: tokens})
}

// Rate returns tokens per minute over the buffer's window as of `now`.
// Samples older than now-window are dropped lazily.
func (b *Buffer) Rate(now time.Time) float64 {
	cutoff := now.Add(-b.window)
	keep := b.samples[:0]
	var sum int
	for _, s := range b.samples {
		if s.t.Before(cutoff) {
			continue
		}
		keep = append(keep, s)
		sum += s.tokens
	}
	b.samples = keep
	if sum == 0 {
		return 0
	}
	minutes := b.window.Minutes()
	return float64(sum) / minutes
}
