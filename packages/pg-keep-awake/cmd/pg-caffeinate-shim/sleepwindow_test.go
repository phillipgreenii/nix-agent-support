package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/anchor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// The real /usr/bin/caffeinate -t counts awake time only (operator Check 3 of
// pg2-8bgxg.3, 2026-10-22: a 120 s -t with about 134 s asleep ended after 120 s
// awake). The shim MUST match, so a sleep window MUST NOT consume the -t
// budget. A Mac cannot be slept from a test, so the window is simulated on the
// shim's injectable clock (newClock): the wall clock jumps while the awake
// clock stands still, exactly what a sleep does to the two readings. Everything
// else is real: the real runPure, the real lease directory and files, the real
// process identity.

// sleepClock is an anchor.Clock whose Sleep returns at once and advances awake
// time and wall time together, except at the sleep numbers in jumps, where the
// wall clock also steps by the given amount (the Mac slept that long).
type sleepClock struct {
	mu       sync.Mutex
	awake    time.Duration
	wall     time.Time
	sleeps   int
	jumps    map[int]time.Duration
	onSleep  func(c *sleepClock, n int)
	maxSleep int
}

var _ anchor.Clock = (*sleepClock)(nil)

func (c *sleepClock) Awake() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.awake
}

func (c *sleepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *sleepClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps++
	n := c.sleeps
	if n > c.maxSleep {
		c.mu.Unlock()
		return errors.New("sleepClock: runaway loop")
	}
	c.awake += d
	c.wall = c.wall.Add(d + c.jumps[n])
	hook := c.onSleep
	c.mu.Unlock()
	if hook != nil {
		hook(c, n)
	}
	return nil
}

func readOnlyLease(t *testing.T, xdg string) *lease.Lease {
	t.Helper()
	files := leaseFiles(xdg)
	if len(files) != 1 {
		t.Fatalf("want exactly one lease file, got %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	l := new(lease.Lease)
	if err := json.Unmarshal(data, l); err != nil {
		t.Fatal(err)
	}
	return l
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// TestSleepWindowDoesNotConsumeTimeoutBudget runs `caffeinate -t 120` through
// the real runPure with a simulated sleep window (or a wall-clock step) in the
// middle, and requires that exactly 120 s of AWAKE time pass before the shim
// ends, and that the lease's expires_at is kept equal to "now + awake time
// left" so the keeper does not reap the lease while budget remains.
func TestSleepWindowDoesNotConsumeTimeoutBudget(t *testing.T) {
	requireDarwin(t)
	const budget = 120 * time.Second
	cases := []struct {
		name  string
		jumps map[int]time.Duration // sleep number -> extra wall time
	}{
		{"no sleep", nil},
		{"asleep 134 s after 30 s awake (the Check 3 shape)", map[int]time.Duration{30: 134 * time.Second}},
		{"asleep 3 hours", map[int]time.Duration{60: 3 * time.Hour}},
		{"two sleep windows", map[int]time.Duration{20: 10 * time.Minute, 90: 50 * time.Hour}},
		{"wall clock stepped back an hour", map[int]time.Duration{40: -time.Hour}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xdg := t.TempDir()
			t.Setenv("XDG_STATE_HOME", xdg)
			t.Setenv("HOME", xdg)

			clk := &sleepClock{
				wall:     time.Date(2026, 10, 22, 23, 55, 42, 0, time.UTC),
				jumps:    tc.jumps,
				maxSleep: 10000,
			}
			// Invariant checked on every poll after the first: the lease's
			// expires_at is the wall time at which the remaining awake budget
			// runs out. (The poll in which a jump happens is allowed to lag;
			// the refresh runs right after it.)
			var lastJump, checked int
			clk.onSleep = func(c *sleepClock, n int) {
				if _, ok := tc.jumps[n]; ok {
					lastJump = n
				}
				if n < 2 || n == lastJump {
					return
				}
				l := readOnlyLease(t, xdg)
				if l.ExpiresAt == nil {
					t.Errorf("sleep %d: lease has no expires_at", n)
					return
				}
				c.mu.Lock()
				want := c.wall.Add(budget - c.awake)
				c.mu.Unlock()
				if d := abs(l.ExpiresAt.Sub(want)); d > expirySlack {
					t.Errorf("sleep %d: expires_at %v is %v off now+remaining (%v)", n, l.ExpiresAt, d, want)
				}
				checked++
			}

			old := newClock
			newClock = func() anchor.Clock { return clk }
			t.Cleanup(func() { newClock = old })

			req, out := parseArgs([]string{"-t", "120"})
			if out != nil {
				t.Fatalf("parse: %+v", out)
			}
			res := runPure(req, []string{"-t", "120"}, io.Discard)

			if res.code != 0 || res.sig != 0 {
				t.Errorf("result = %+v, want a clean exit 0", res)
			}
			if got := clk.Awake(); got != budget {
				t.Errorf("shim ended after %v awake, want exactly %v: the sleep window must not consume the -t budget", got, budget)
			}
			if checked == 0 {
				t.Error("the lease was never inspected")
			}
			noLeases(t, xdg)
		})
	}
}
