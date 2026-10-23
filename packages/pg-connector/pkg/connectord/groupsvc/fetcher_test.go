package groupsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// scriptCall is one Fetch call the scripted fetcher saw.
type scriptCall struct {
	Call    CallMeta
	Targets []Target
}

// scriptFetcher is a counting, scriptable Fetcher. By default it answers every
// target with a small JSON document that changes on every call, carrying the
// target's own stamps. It can block inside Fetch, fail the whole call, say an
// entity is gone, and answer through a function.
type scriptFetcher struct {
	name string

	mu      sync.Mutex
	calls   []scriptCall
	started int
	seq     int

	gate    chan struct{}
	ctxErr  error // the fetch context's error once a blocked call resumed
	err     error
	gone    map[string]bool
	lastErr string
	outcome func(t Target, n int) Outcome
}

func newScriptFetcher(group string) *scriptFetcher {
	return &scriptFetcher{name: group, gone: map[string]bool{}}
}

func (f *scriptFetcher) Group() string { return f.name }
func (f *scriptFetcher) MaxBatch() int { return 10 }

func (f *scriptFetcher) Spend(class governor.Class, _ int) governor.Request {
	return governor.Request{Class: class, Cost: 1}
}

func (f *scriptFetcher) Fetch(ctx context.Context, call CallMeta, targets []Target) []Outcome {
	f.mu.Lock()
	f.calls = append(f.calls, scriptCall{Call: call, Targets: slices.Clone(targets)})
	f.started++
	n := f.started
	gate, ferr, lastErr, outcome := f.gate, f.err, f.lastErr, f.outcome
	f.mu.Unlock()

	if gate != nil {
		<-gate
		f.mu.Lock()
		f.ctxErr = ctx.Err()
		f.mu.Unlock()
	}
	out := make([]Outcome, len(targets))
	if ferr != nil {
		for i := range out {
			out[i] = Outcome{Err: ferr}
		}
		return out
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, t := range targets {
		switch {
		case f.gone[t.ID]:
			out[i] = Outcome{Err: ErrGone}
		case outcome != nil:
			out[i] = outcome(t, n)
		default:
			f.seq++
			content, _ := json.Marshal(map[string]any{
				"group": f.name, "id": t.ID, "stamp": t.Stamp, "stamp2": t.Stamp2, "fetch": f.seq,
			})
			out[i] = Outcome{Content: content, Stamp: t.Stamp, Stamp2: t.Stamp2, Cost: 1, LastError: lastErr}
		}
	}
	return out
}

func (f *scriptFetcher) callList() []scriptCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *scriptFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *scriptFetcher) targetCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += len(c.Targets)
	}
	return n
}

// block makes every later Fetch wait inside the call until the returned release
// function runs (safe to call more than once).
func (f *scriptFetcher) block() (release func()) {
	gate := make(chan struct{})
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// waitStarted blocks until at least n calls have started or the timeout passes.
func (f *scriptFetcher) waitStarted(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		f.mu.Lock()
		ok := f.started >= n
		f.mu.Unlock()
		if ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (f *scriptFetcher) failWith(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *scriptFetcher) setGone(id string, gone bool) {
	f.mu.Lock()
	f.gone[id] = gone
	f.mu.Unlock()
}

func (f *scriptFetcher) setOutcome(fn func(t Target, n int) Outcome) {
	f.mu.Lock()
	f.outcome = fn
	f.mu.Unlock()
}

// The errors of the invented origin: a rate-limit answer and a refusal of the
// credentials. fakeErrors turns an error whose text says "quota" into the first
// (dropping any wrap of the second, as a real policy must).
var (
	errTestRateLimited = errors.New("test: rate limited")
	errTestAuth        = errors.New("test: credentials refused")
)

// classifyCall is what one Classify call was given.
type classifyCall struct {
	gov   *governor.Governor
	pause time.Duration
	err   error
}

// fakeErrors is the invented connector's ErrorPolicy.
type fakeErrors struct {
	mu      sync.Mutex
	classed []classifyCall
}

func (e *fakeErrors) Classify(g *governor.Governor, pause time.Duration, err error) error {
	e.mu.Lock()
	e.classed = append(e.classed, classifyCall{g, pause, err})
	e.mu.Unlock()
	if err != nil && !errors.Is(err, governor.ErrRefused) && strings.Contains(err.Error(), "quota") {
		return fmt.Errorf("%w: %s", errTestRateLimited, err)
	}
	return err
}

func (e *fakeErrors) IsRateLimit(err error) bool { return errors.Is(err, errTestRateLimited) }

func (e *fakeErrors) IsUnauthenticated(err error) bool { return errors.Is(err, errTestAuth) }

func (e *fakeErrors) calls() []classifyCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.classed)
}

// authMarker records the authentication markers.
type authMarker struct {
	mu     sync.Mutex
	marks  int
	clears int
}

func (m *authMarker) MarkUnauthenticated(error) {
	m.mu.Lock()
	m.marks++
	m.mu.Unlock()
}

func (m *authMarker) Clear() {
	m.mu.Lock()
	m.clears++
	m.mu.Unlock()
}

func (m *authMarker) counts() (marks, clears int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.marks, m.clears
}

// contextErr is the error of the context a blocked Fetch saw when it resumed.
func (f *scriptFetcher) contextErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxErr
}
