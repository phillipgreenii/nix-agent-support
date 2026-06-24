package session

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// envCapTmux records the env passed to NewSession.
type envCapTmux struct {
	env map[string]string
}

func (c *envCapTmux) HasSession(string) bool { return false }
func (c *envCapTmux) NewSession(_, _ string, env map[string]string, _ []string) error {
	c.env = env
	return nil
}
func (c *envCapTmux) SendKeys(string, ...string) error   { return nil }
func (c *envCapTmux) Paste(string, string) error         { return nil }
func (c *envCapTmux) KillSession(string) error           { return nil }
func (c *envCapTmux) CapturePane(string) (string, error) { return "", nil }

func TestLaunchAndWait_injectsPool(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Starting, TmuxSession: "cc-a"})
	tm := &envCapTmux{}
	s := New(Deps{
		Tmux: tm, Store: st, Prefix: "cc-", PoolPath: "/pools/alpha",
		Wait: waitFunc(func(context.Context, string, int64) (wait.Outcome, error) {
			return wait.Outcome{State: store.Ready}, nil
		}),
		Now: func() time.Time { return time.Unix(1, 0) },
	})
	if _, err := s.launchAndWait(ctx, "a", "cc-a", "csid", "name-a", "/cwd", 0, []string{"claude"}, nil, false); err != nil {
		t.Fatalf("launchAndWait: %v", err)
	}
	if tm.env["CCPOOL_POOL"] != "/pools/alpha" {
		t.Errorf("CCPOOL_POOL = %q, want /pools/alpha", tm.env["CCPOOL_POOL"])
	}
}

func TestLaunchAndWait_defaultModeNoPool(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Starting, TmuxSession: "cc-a"})
	tm := &envCapTmux{}
	s := New(Deps{
		Tmux: tm, Store: st, Prefix: "cc-", PoolPath: "", // default mode
		Wait: waitFunc(func(context.Context, string, int64) (wait.Outcome, error) {
			return wait.Outcome{State: store.Ready}, nil
		}),
		Now: func() time.Time { return time.Unix(1, 0) },
	})
	_, _ = s.launchAndWait(ctx, "a", "cc-a", "csid", "name-a", "/cwd", 0, []string{"claude"}, nil, false)
	if _, ok := tm.env["CCPOOL_POOL"]; ok {
		t.Error("default mode must NOT set CCPOOL_POOL")
	}
}
