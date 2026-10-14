package power

import "sync"

// FakeReader is a scriptable Reader for tests: set the reading (or error) with
// Set / SetErr between ticks. The zero value reads as "on AC".
type FakeReader struct {
	mu      sync.Mutex
	reading Reading
	err     error
	reads   int
}

// NewFakeReader returns a FakeReader reporting r.
func NewFakeReader(r Reading) *FakeReader { return &FakeReader{reading: r} }

// Set replaces the reading and clears any error.
func (f *FakeReader) Set(r Reading) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reading, f.err = r, nil
}

// SetErr makes every following Read fail with err (nil clears it).
func (f *FakeReader) SetErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Reads returns how many times Read was called.
func (f *FakeReader) Reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// Read implements Reader.
func (f *FakeReader) Read() (Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return Reading{Percent: NoPercent}, f.err
	}
	return f.reading, nil
}
