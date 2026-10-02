package contract

import "bytes"

// LimitedBuffer is an io.Writer that keeps at most Max bytes and silently
// discards the rest, so the process writing into it (a handler's stdout pipe
// copier) never blocks or fails on an oversized result. Truncated reports
// whether anything was discarded; pass it to Classify.
type LimitedBuffer struct {
	// Max is the number of bytes kept.
	Max       int
	buf       bytes.Buffer
	truncated bool
}

// NewResultBuffer returns a LimitedBuffer capped at MaxResultBytes.
func NewResultBuffer() *LimitedBuffer { return &LimitedBuffer{Max: MaxResultBytes} }

// Write keeps what fits and always reports the full length as written.
func (b *LimitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := max(b.Max-b.buf.Len(), 0)
	if n > room {
		b.truncated = true
		p = p[:room]
	}
	b.buf.Write(p)
	return n, nil
}

// Bytes returns the kept bytes.
func (b *LimitedBuffer) Bytes() []byte { return b.buf.Bytes() }

// Truncated reports whether more than Max bytes were written.
func (b *LimitedBuffer) Truncated() bool { return b.truncated }
