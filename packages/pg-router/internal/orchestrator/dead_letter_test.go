package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

type fakeFailedLog struct {
	mu   sync.Mutex
	recs []eventqueue.DeadLetter
}

func (f *fakeFailedLog) RecordFailed(dl eventqueue.DeadLetter) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, dl)
	return nil
}

func (f *fakeFailedLog) snapshot() []eventqueue.DeadLetter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]eventqueue.DeadLetter(nil), f.recs...)
}

var deterministicErr = &wireclient.ExitError{Role: "desk-pr", Code: 1, Detail: "invalid_argument: bad PR id"}

func newDeadLetterListener(t *testing.T, h wireclient.HandlerClient, maxRetries int, now time.Time) (eventqueue.Listener, *fakeFailedLog) {
	t.Helper()
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = h
	fl := &fakeFailedLog{}
	o.FailedLog = fl
	o.failedNow = func() time.Time { return now }
	role := roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}, MaxDispatchRetries: maxRetries}
	return o.NewListener(context.Background(), role), fl
}

func deadEvent(id string, expires time.Time) eventqueue.Event {
	return eventqueue.Event{ID: id, Type: "pr.changed", Payload: map[string]any{"id": "pr-1"}, ExpiresAt: expires}
}

func wantOne(t *testing.T, fl *fakeFailedLog, want eventqueue.DeadLetter) {
	t.Helper()
	got := fl.snapshot()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("failed records = %+v, want exactly %+v", got, want)
	}
}

// A deterministic failure ends the dispatch at once: one record, reason
// deterministic, even for a role with retries and an unexpired event.
func TestDeadLetter_Deterministic(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 5, 700, time.UTC)
	h := &seqHandler{errs: []error{deterministicErr}}
	l, fl := newDeadLetterListener(t, h, 2, now)
	l.Offer(eventqueue.Offering{ID: "d", Event: deadEvent("e1", now.Add(time.Hour))})
	wantOne(t, fl, eventqueue.DeadLetter{
		EventID: "e1", Role: "desk-pr", Class: "deterministic", Attempts: 1,
		Reason: "deterministic", At: time.Date(2026, 11, 20, 10, 0, 5, 0, time.UTC),
	})
}

// A transient failure with its retries spent: records only on the last attempt
// (attempt 3 after 2 re-runs), reason retries-spent; the re-runs write nothing.
func TestDeadLetter_RetriesSpent(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := &seqHandler{errs: []error{killedErr, killedErr, killedErr}}
	l, fl := newDeadLetterListener(t, h, 2, now)
	evt := deadEvent("e1", now.Add(time.Hour))
	for i := 0; i < 2; i++ {
		l.Offer(eventqueue.Offering{ID: "d", Event: evt})
		if n := len(fl.snapshot()); n != 0 {
			t.Fatalf("a scheduled re-run wrote %d failed records, want 0", n)
		}
	}
	l.Offer(eventqueue.Offering{ID: "d", Event: evt})
	wantOne(t, fl, eventqueue.DeadLetter{EventID: "e1", Role: "desk-pr", Class: "killed", Attempts: 3, Reason: "retries-spent", At: now})
}

// A role that did not opt in (zero retries allowed) ends a transient failure as
// retries-spent on attempt 1.
func TestDeadLetter_NoOptInIsRetriesSpent(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := &seqHandler{errs: []error{killedErr}}
	l, fl := newDeadLetterListener(t, h, 0, now)
	l.Offer(eventqueue.Offering{ID: "d", Event: deadEvent("e1", now.Add(time.Hour))})
	wantOne(t, fl, eventqueue.DeadLetter{EventID: "e1", Role: "desk-pr", Class: "killed", Attempts: 1, Reason: "retries-spent", At: now})
}

// A transient failure of an event already expired at the failure instant is
// window-closed, and so is one whose expiresAt equals that instant; this also
// holds when retries were left (no re-run was allowed) and when they were spent.
func TestDeadLetter_WindowClosed(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for name, expires := range map[string]time.Time{"past": now.Add(-time.Minute), "equal": now} {
		t.Run(name, func(t *testing.T) {
			h := &seqHandler{errs: []error{killedErr}}
			l, fl := newDeadLetterListener(t, h, 2, now)
			l.Offer(eventqueue.Offering{ID: "d", Event: deadEvent("e1", expires)})
			wantOne(t, fl, eventqueue.DeadLetter{EventID: "e1", Role: "desk-pr", Class: "killed", Attempts: 1, Reason: "window-closed", At: now})
		})
	}
	t.Run("retries spent and window closed", func(t *testing.T) {
		h := &seqHandler{errs: []error{killedErr}}
		l, fl := newDeadLetterListener(t, h, 1, now)
		rl := l.(*roleListener)
		rl.retries.take("e1", 1) // the one allowed re-run is already used
		l.Offer(eventqueue.Offering{ID: "d", Event: deadEvent("e1", now)})
		wantOne(t, fl, eventqueue.DeadLetter{EventID: "e1", Role: "desk-pr", Class: "killed", Attempts: 2, Reason: "window-closed", At: now})
	})
}

// A deterministic failure of an expired event stays deterministic.
func TestDeadLetter_DeterministicBeatsWindowClosed(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := &seqHandler{errs: []error{deterministicErr}}
	l, fl := newDeadLetterListener(t, h, 2, now)
	l.Offer(eventqueue.Offering{ID: "d", Event: deadEvent("e1", now.Add(-time.Hour))})
	wantOne(t, fl, eventqueue.DeadLetter{EventID: "e1", Role: "desk-pr", Class: "deterministic", Attempts: 1, Reason: "deterministic", At: now})
}

// Each transient class and the not-ingested class is carried by the record.
func TestDeadLetter_ClassVocabulary(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		class string
		err   error
	}{
		{"killed", killedErr},
		{"deadline", context.DeadlineExceeded},
		{"unavailable", &wireclient.ExitError{Role: "desk-pr", Code: 1, Detail: "connector: unavailable"}},
	} {
		t.Run(c.class, func(t *testing.T) {
			h := &seqHandler{errs: []error{c.err}}
			l, fl := newDeadLetterListener(t, h, 0, now)
			l.Offer(eventqueue.Offering{ID: "d", Event: deadEvent("e1", now.Add(time.Hour))})
			got := fl.snapshot()
			if len(got) != 1 || got[0].Class != c.class {
				t.Fatalf("records = %+v, want one of class %q", got, c.class)
			}
		})
	}
	dl := DeadLetterFor(deadEvent("e1", now.Add(time.Hour)), "r", errors.New("prompt not ingested"), 1, now)
	if dl.Class != "not-ingested" {
		t.Fatalf("class = %q, want not-ingested", dl.Class)
	}
}

// A success writes no record.
func TestDeadLetter_SuccessWritesNothing(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	l, fl := newDeadLetterListener(t, &seqHandler{}, 2, now)
	l.Offer(eventqueue.Offering{ID: "d", Event: deadEvent("e1", now.Add(time.Hour))})
	if n := len(fl.snapshot()); n != 0 {
		t.Fatalf("records = %d, want 0", n)
	}
}
