package originguard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: t0} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeAgent maps a socket path to the certificates its agent holds. A socket
// not in the map is unreachable.
type fakeAgent map[string][]Cert

func (f fakeAgent) ListCerts(_ context.Context, socket string) ([]Cert, error) {
	certs, ok := f[socket]
	if !ok {
		return nil, errors.New("dial: no such socket")
	}
	return certs, nil
}

type captureSink struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (s *captureSink) Emit(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, e)
	return nil
}

func (s *captureSink) list() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func testEnv(t *testing.T) Env {
	t.Helper()
	home := t.TempDir()
	return Env{Home: home, Getenv: func(string) string { return "" }}
}

func mustParse(t *testing.T, text string, env Env) Config {
	t.Helper()
	cfg, err := Parse(text, env)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}
