package event_test

import (
	"regexp"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

type completeOrder1 struct {
	TaskID      string `json:"task_id"`
	EffectiveAt string `json:"effective_at,omitempty"`
	Minutes     int    `json:"minutes,omitempty"`
}

// completeOrder2 holds the same fields in another declaration order.
type completeOrder2 struct {
	Minutes     int    `json:"minutes,omitempty"`
	EffectiveAt string `json:"effective_at,omitempty"`
	TaskID      string `json:"task_id"`
}

// completeWithRequestFields carries the request-level fields that must never
// be hashed, tagged the way a command type tags them.
type completeWithRequestFields struct {
	ID              string `json:"id"`
	DryRun          bool   `json:"dry_run"`
	ExpectedVersion int    `json:"expected_version"`
	TaskID          string `json:"task_id"`
}

func mustHash(t *testing.T, command string, fields any) string {
	t.Helper()
	h, err := event.ReqHash(command, fields)
	if err != nil {
		t.Fatalf("ReqHash(%q, %v): %v", command, fields, err)
	}
	return h
}

func TestReqHashStable(t *testing.T) {
	h := mustHash(t, "task.complete", completeOrder1{TaskID: "day:2026-10-07:post-plan", Minutes: 5})

	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(h) {
		t.Errorf("hash %q is not 64 lower-case hex digits", h)
	}
	if again := mustHash(t, "task.complete", completeOrder1{TaskID: "day:2026-10-07:post-plan", Minutes: 5}); again != h {
		t.Errorf("the same request hashed to %q and %q", h, again)
	}

	t.Run("struct field order does not matter", func(t *testing.T) {
		if got := mustHash(t, "task.complete", completeOrder2{TaskID: "day:2026-10-07:post-plan", Minutes: 5}); got != h {
			t.Errorf("hash = %q, want %q", got, h)
		}
	})
	t.Run("map key order and map versus struct do not matter", func(t *testing.T) {
		m1 := map[string]any{"task_id": "day:2026-10-07:post-plan", "minutes": 5}
		m2 := map[string]any{"minutes": 5, "task_id": "day:2026-10-07:post-plan"}
		if got := mustHash(t, "task.complete", m1); got != h {
			t.Errorf("map hash = %q, want %q", got, h)
		}
		if got := mustHash(t, "task.complete", m2); got != h {
			t.Errorf("reordered map hash = %q, want %q", got, h)
		}
	})
	t.Run("nested keys are sorted too", func(t *testing.T) {
		a := mustHash(t, "x", map[string]any{"outer": map[string]any{"a": 1, "b": 2}})
		b := mustHash(t, "x", map[string]any{"outer": map[string]any{"b": 2, "a": 1}})
		if a != b {
			t.Errorf("nested order changed the hash: %q vs %q", a, b)
		}
	})
	t.Run("a supplied effective_at differs from an omitted one", func(t *testing.T) {
		with := mustHash(t, "task.complete", completeOrder1{TaskID: "day:2026-10-07:post-plan", Minutes: 5, EffectiveAt: "2026-10-07T13:28:00.000Z"})
		if with == h {
			t.Error("supplying effective_at left the hash unchanged")
		}
	})
	t.Run("a changed minutes value changes it", func(t *testing.T) {
		if got := mustHash(t, "task.complete", completeOrder1{TaskID: "day:2026-10-07:post-plan", Minutes: 6}); got == h {
			t.Error("changing minutes left the hash unchanged")
		}
	})
	t.Run("the command name enters it", func(t *testing.T) {
		if got := mustHash(t, "task.skip", completeOrder1{TaskID: "day:2026-10-07:post-plan", Minutes: 5}); got == h {
			t.Error("a different command name left the hash unchanged")
		}
	})
	t.Run("large integers keep every digit", func(t *testing.T) {
		a := mustHash(t, "x", map[string]any{"n": int64(9007199254740993)})
		b := mustHash(t, "x", map[string]any{"n": int64(9007199254740992)})
		if a == b {
			t.Error("two integers above 2^53 hash equal")
		}
	})
}

func TestReqHashExcludesRequestFields(t *testing.T) {
	base := mustHash(t, "task.complete", completeWithRequestFields{TaskID: "day:2026-10-07:post-plan"})

	if got := mustHash(t, "task.complete", completeWithRequestFields{ID: "01J9Z3K8M20000000000000001", TaskID: "day:2026-10-07:post-plan"}); got != base {
		t.Errorf("id entered the hash: %q vs %q", got, base)
	}
	if got := mustHash(t, "task.complete", completeWithRequestFields{DryRun: true, ExpectedVersion: 9, TaskID: "day:2026-10-07:post-plan"}); got != base {
		t.Errorf("dry_run or expected_version entered the hash: %q vs %q", got, base)
	}
	asMap := map[string]any{"id": "01J9Z3K8M20000000000000001", "dry_run": true, "expected_version": 3, "task_id": "day:2026-10-07:post-plan"}
	if got := mustHash(t, "task.complete", asMap); got != base {
		t.Errorf("a map carrying the request fields hashed to %q, want %q", got, base)
	}
	if got := mustHash(t, "task.complete", map[string]any{"task_id": "day:2026-10-07:post-plan"}); got != base {
		t.Errorf("hash without the request fields = %q, want %q", got, base)
	}
}

func TestReqHashRejectsFieldsThatCannotBeEncoded(t *testing.T) {
	if _, err := event.ReqHash("x", func() {}); err == nil {
		t.Error("ReqHash of a func succeeded, want an error")
	}
}
