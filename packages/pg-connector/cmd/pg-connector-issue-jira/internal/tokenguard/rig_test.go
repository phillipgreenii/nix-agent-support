package tokenguard

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/keychain"
)

const fixtureToken = "FIXTURE-TOKEN-do-not-use-0001"

// memStore is a Store that keeps the token in memory, like a file pjira reads.
type memStore struct {
	mu       sync.Mutex
	startErr error
	writeErr error
	cur      string
	writes   int
}

func (s *memStore) Start() error { return s.startErr }

func (s *memStore) Write(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	s.cur = string(b)
	s.writes++
	return nil
}

func (s *memStore) token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

func (s *memStore) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// fakePjira stands in for the inner runner: it "reads the token file" at
// launch and answers 401 unless the token is in the accepted set.
type fakePjira struct {
	mu       sync.Mutex
	store    *memStore
	accepted map[string]bool
	calls    [][]string
	hook     func(args []string) (string, error, bool) // optional override
	gate     func()                                    // runs after the launch is counted
}

func (f *fakePjira) Binary() string { return "/abs/pjira" }

func (f *fakePjira) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	hook, gate := f.hook, f.gate
	f.mu.Unlock()
	tok := f.store.token()
	if gate != nil {
		gate()
	}
	if hook != nil {
		if out, err, ok := hook(args); ok {
			return out, err
		}
	}
	f.mu.Lock()
	ok := f.accepted[tok]
	f.mu.Unlock()
	if !ok {
		return "", errors.New("pjira issue PROJ-1: status 401 Unauthorized")
	}
	return "{}", nil
}

func (f *fakePjira) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakePjira) accept(tokens ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepted = map[string]bool{}
	for _, t := range tokens {
		f.accepted[t] = true
	}
}

type obs struct {
	mu      sync.Mutex
	results []string
}

func (o *obs) Request(r string, _ time.Duration) {
	o.mu.Lock()
	o.results = append(o.results, r)
	o.mu.Unlock()
}

func (o *obs) got() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.results...)
}

type evRec struct {
	mu     sync.Mutex
	events []Event
	ch     chan Event
}

func newEvRec() *evRec { return &evRec{ch: make(chan Event, 64)} }

func (e *evRec) Record(ev Event) {
	e.mu.Lock()
	e.events = append(e.events, ev)
	e.mu.Unlock()
	select {
	case e.ch <- ev:
	default:
	}
}

func (e *evRec) all() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.events...)
}

// await blocks until an event with the name arrives.
func (e *evRec) await(t *testing.T, name string) Event {
	t.Helper()
	for {
		select {
		case ev := <-e.ch:
			if ev.Name == name {
				return ev
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no %s event", name)
		}
	}
}

type rig struct {
	clock *governor.ManualClock
	timer *ManualTimer
	kc    *keychain.Fake
	store *memStore
	pjira *fakePjira
	gov   *governor.Governor
	run   *origin.Runner
	guard *Guard
	obs   *obs
	rec   *evRec
	auth  *origin.AuthState
	mu    sync.Mutex
	spent []governor.SpendEvent
}

func newRig(t *testing.T, script ...keychain.Result) *rig {
	t.Helper()
	r := &rig{
		clock: governor.NewManualClock(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)),
		timer: NewManualTimer(),
		kc:    keychain.NewFake(script...),
		store: &memStore{},
		obs:   &obs{},
		rec:   newEvRec(),
		auth:  origin.NewAuthState(),
	}
	r.pjira = &fakePjira{store: r.store}
	r.pjira.accept(fixtureToken)
	cfg := config.Defaults()
	cfg.RequestRatePerSecond = 1000
	g, err := governor.New(origin.GovernorConfig(cfg), governor.Options{
		Clock: r.clock,
		OnSpend: func(e governor.SpendEvent) {
			r.mu.Lock()
			r.spent = append(r.spent, e)
			r.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.gov = g
	r.guard = New(Config{
		Keychain: r.kc, Store: r.store, Service: "svc", Account: "acct",
		Clock: r.clock, Timer: r.timer, Recorder: r.rec,
	})
	t.Cleanup(r.guard.Close)
	run, err := origin.New(origin.Options{
		Inner: r.pjira, Governor: g, Concurrency: 64, DefaultPause: time.Minute,
		Clock: r.clock, Observer: r.obs, Auth: r.auth, Refresher: r.guard,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.run = run
	return r
}

func (r *rig) spends() []governor.SpendEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]governor.SpendEvent(nil), r.spent...)
}

func (r *rig) call() (string, error) {
	return r.run.Run(context.Background(), "issue", "--", "PROJ-1")
}

func (r *rig) start() { r.guard.Start(context.Background()) }

func newToken(t *testing.T) string { t.Helper(); return "FIXTURE-ROTATED-" + strings.ToUpper(t.Name()) }
