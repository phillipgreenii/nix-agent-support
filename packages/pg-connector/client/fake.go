package client

import (
	"context"
	"slices"
	"sync"

	"github.com/phillipgreenii/x/cmdrun"
)

// Fake is an in-process stand-in for the pg-connector binary: it records
// every call and answers from scripted responses. Use Fake.Runner as
// Client.Run. It is safe for concurrent use.
type Fake struct {
	mu      sync.Mutex
	calls   []cmdrun.Call
	scripts []fakeScript
	// Default answers a call no script matches; the zero value is an empty
	// stdout with exit 0.
	Default cmdrun.Result
}

type fakeScript struct {
	prefix []string
	res    cmdrun.Result
}

// NewFake returns an empty Fake.
func NewFake() *Fake { return &Fake{} }

// Script makes every call whose args start with prefix answer res. The
// longest matching prefix wins; among equals the latest script wins.
func (f *Fake) Script(res cmdrun.Result, prefix ...string) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, fakeScript{prefix: slices.Clone(prefix), res: res})
	return f
}

// Runner is the cmdrun.Runner to hand to Client.Run.
func (f *Fake) Runner() cmdrun.Runner {
	return func(_ context.Context, c cmdrun.Call) cmdrun.Result {
		f.mu.Lock()
		defer f.mu.Unlock()
		c.Args = slices.Clone(c.Args)
		f.calls = append(f.calls, c)
		best, bestLen := f.Default, -1
		for _, s := range f.scripts {
			if len(s.prefix) >= bestLen && len(s.prefix) <= len(c.Args) && slices.Equal(s.prefix, c.Args[:len(s.prefix)]) {
				best, bestLen = s.res, len(s.prefix)
			}
		}
		return best
	}
}

// Calls returns the recorded calls in order.
func (f *Fake) Calls() []cmdrun.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Argv returns the recorded calls' argument lists in order.
func (f *Fake) Argv() [][]string {
	calls := f.Calls()
	out := make([][]string, len(calls))
	for i, c := range calls {
		out[i] = c.Args
	}
	return out
}
