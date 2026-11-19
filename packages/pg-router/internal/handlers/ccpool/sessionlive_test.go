package ccpool

import (
	"context"
	"errors"
	"testing"
)

// fakeListRunner serves a fixed List result for SessionLive; every other method
// is the embedded nil Runner's, which a test must never reach.
type fakeListRunner struct {
	Runner
	sessions []Session
	err      error
}

func (f fakeListRunner) List(context.Context) ([]Session, error) { return f.sessions, f.err }

func TestSessionLive(t *testing.T) {
	cases := []struct {
		name string
		r    fakeListRunner
		want bool
	}{
		{"present and live", fakeListRunner{sessions: []Session{{ExternalID: "a", Live: false}, {ExternalID: "s", Live: true}}}, true},
		{"present and not live", fakeListRunner{sessions: []Session{{ExternalID: "s", Live: false, State: StateReady}}}, false},
		{"absent", fakeListRunner{sessions: []Session{{ExternalID: "other", Live: true}}}, false},
		{"empty pool", fakeListRunner{}, false},
		{"list unreadable counts as live (the send decides)", fakeListRunner{err: errors.New("boom")}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SessionLive(context.Background(), c.r, "s"); got != c.want {
				t.Fatalf("SessionLive = %v, want %v", got, c.want)
			}
		})
	}
}
