// Package usage reads a session's cumulative consumption (tokens and estimated
// cost) behind a Reader interface so the watchdog never couples to how it is
// measured. The measurement and the price table live in ccpool (`ccpool
// consumption`); this package only adapts that reading.
package usage

import "context"

// Snapshot is cumulative consumption for one session at one instant.
type Snapshot struct {
	Model               string
	InputTokens         int
	CacheCreationTokens int
	CacheReadTokens     int
	OutputTokens        int
	// CostCents is ccpool's estimated cost in integer USD cents.
	CostCents int64
}

// Total is the cumulative-tokens meter (all components summed).
func (s Snapshot) Total() int {
	return s.InputTokens + s.CacheCreationTokens + s.CacheReadTokens + s.OutputTokens
}

// Reader reads a session's cumulative consumption, addressed by external_id.
type Reader interface {
	Read(ctx context.Context, externalID string) (Snapshot, error)
}

// Source reports a session's cumulative consumption; ccpool.CLIRunner
// implements it by running `ccpool consumption`.
type Source interface {
	Consumption(ctx context.Context, externalID string) (Snapshot, error)
}

type ccpoolReader struct{ src Source }

// NewCcpoolReader returns a Reader backed by `ccpool consumption`.
func NewCcpoolReader(src Source) Reader { return ccpoolReader{src: src} }

func (r ccpoolReader) Read(ctx context.Context, externalID string) (Snapshot, error) {
	s, err := r.src.Consumption(ctx, externalID)
	if err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// zeroReader reads nothing: the Reader for a Runner that cannot report
// consumption (the watchdog then meters only time).
type zeroReader struct{}

func (zeroReader) Read(context.Context, string) (Snapshot, error) { return Snapshot{}, nil }

// ReaderFor returns the ccpool-backed Reader when run can report consumption,
// else a zero reader.
func ReaderFor(run any) Reader {
	if src, ok := run.(Source); ok {
		return NewCcpoolReader(src)
	}
	return zeroReader{}
}
