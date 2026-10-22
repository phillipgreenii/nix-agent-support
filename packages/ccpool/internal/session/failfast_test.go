package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// launchWaiter is a Waiter that also provides WaitLaunch; the two record which
// one launchAndWait chose.
type launchWaiter struct {
	plain, launch int
	out           wait.Outcome
	err           error
}

func (w *launchWaiter) Wait(context.Context, string, int64) (wait.Outcome, error) {
	w.plain++
	return wait.Outcome{State: store.Ready}, nil
}

func (w *launchWaiter) WaitLaunch(context.Context, string, int64) (wait.Outcome, error) {
	w.launch++
	return w.out, w.err
}

func newFailFastService(t *testing.T, w Waiter) *Service {
	t.Helper()
	st := newMemStore(t)
	_ = st.Insert(context.Background(), store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Starting, TmuxSession: "cc-a"})
	return New(Deps{
		Tmux: &envCapTmux{}, Store: st, Prefix: "cc-", Wait: w,
		Now: func() time.Time { return time.Unix(1, 0) },
	})
}

// A launch (and so a resume, which reaches the same function) uses the
// launch-aware wait when the Waiter provides one, never the plain turn wait.
func TestLaunchAndWait_usesLaunchWaiter(t *testing.T) {
	w := &launchWaiter{out: wait.Outcome{State: store.Ready}}
	s := newFailFastService(t, w)
	if _, err := s.launchAndWait(context.Background(), "a", "cc-a", "csid", "a", "/cwd", 0, []string{"claude"}, nil, false, "resume"); err != nil {
		t.Fatalf("launchAndWait: %v", err)
	}
	if w.launch != 1 || w.plain != 0 {
		t.Errorf("WaitLaunch=%d Wait=%d, want 1 and 0", w.launch, w.plain)
	}
}

// The fail-fast error from the wait reaches the caller verbatim.
func TestLaunchAndWait_failFastErrorSurfaces(t *testing.T) {
	cause := errors.New("the launched process exited with status 1 before ready")
	s := newFailFastService(t, &launchWaiter{err: cause})
	_, err := s.launchAndWait(context.Background(), "a", "cc-a", "csid", "a", "/cwd", 0, []string{"claude"}, nil, false, "brand_new")
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want it to wrap %v", err, cause)
	}
}

// A timeout carries the pane tail the wait gathered, after the existing text.
func TestLaunchAndWait_timeoutCarriesTail(t *testing.T) {
	s := newFailFastService(t, &launchWaiter{out: wait.Outcome{State: store.Starting, TimedOut: true, Detail: "output tail:\nPlease log in"}})
	_, err := s.launchAndWait(context.Background(), "a", "cc-a", "csid", "a", "/cwd", 0, []string{"claude"}, nil, false, "brand_new")
	if err == nil {
		t.Fatal("want a timeout error")
	}
	for _, want := range []string{"did not reach ready before timeout", "state=starting", "Please log in"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// A Waiter without WaitLaunch keeps working through plain Wait.
func TestLaunchAndWait_plainWaiterStillWorks(t *testing.T) {
	s := newFailFastService(t, waitFunc(func(context.Context, string, int64) (wait.Outcome, error) {
		return wait.Outcome{State: store.Ready}, nil
	}))
	if _, err := s.launchAndWait(context.Background(), "a", "cc-a", "csid", "a", "/cwd", 0, []string{"claude"}, nil, false, "brand_new"); err != nil {
		t.Fatalf("launchAndWait: %v", err)
	}
}
