package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router/conformance"
)

func TestPoolFlag_setParsesNameDir(t *testing.T) {
	var f poolFlag
	if err := f.Set("review=/state/review"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Set("worker=/state/worker"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	want := []poolEntry{{name: "review", dir: "/state/review"}, {name: "worker", dir: "/state/worker"}}
	if len(f.entries) != len(want) || f.entries[0] != want[0] || f.entries[1] != want[1] {
		t.Errorf("entries = %+v, want %+v", f.entries, want)
	}
}

func TestPoolFlag_setRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"noequals", "=novalue", "noname="} {
		var f poolFlag
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%q) should have failed", bad)
		}
	}
}

// fakeCapacity returns a poolCapacityFn (writePoolCapacity's own seam) that
// answers from a fixed dir->Capacity map, or errs for any dir listed in errs
// — zero real `ccpool` processes.
func fakeCapacity(byDir map[string]ccpool.Capacity, errs map[string]error) poolCapacityFn {
	return func(_ context.Context, dir string) (ccpool.Capacity, error) {
		if err, ok := errs[dir]; ok {
			return ccpool.Capacity{}, err
		}
		return byDir[dir], nil
	}
}

// TestWritePoolCapacity_rendersEveryDimensionPerPool proves every ccpool.
// Capacity dimension (bead pg2-mr0sl's acceptance criterion: "reports the
// configured cap ... for each") is rendered as its own labeled sample, for
// every configured pool — not just max_sessions/free.
func TestWritePoolCapacity_rendersEveryDimensionPerPool(t *testing.T) {
	pools := []poolEntry{
		{name: "worker", dir: "/state/worker"},
		{name: "review", dir: "/state/review"},
	}
	byDir := map[string]ccpool.Capacity{
		"/state/review": {MaxSessions: 1, Live: 1, Preserved: 0, Counted: 1, Free: 0},
		"/state/worker": {MaxSessions: 3, Live: 1, Preserved: 0, Counted: 1, Free: 2},
	}
	var buf bytes.Buffer
	code := writePoolCapacity(&buf, pools, fakeCapacity(byDir, nil))
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}
	out := buf.String()

	// Sorted by name: review before worker, regardless of argv order above.
	if strings.Index(out, `role="review"`) > strings.Index(out, `role="worker"`) {
		t.Errorf("output not sorted by role name:\n%s", out)
	}
	for _, want := range []string{
		`pg_router_ccpool_handler_pool_capacity{role="review",pool="/state/review",dim="max_sessions"} 1`,
		`pg_router_ccpool_handler_pool_capacity{role="review",pool="/state/review",dim="free"} 0`,
		`pg_router_ccpool_handler_pool_capacity{role="worker",pool="/state/worker",dim="max_sessions"} 3`,
		`pg_router_ccpool_handler_pool_capacity{role="worker",pool="/state/worker",dim="free"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "# HELP pg_router_ccpool_handler_pool_capacity") {
		t.Errorf("output missing HELP line; got:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE pg_router_ccpool_handler_pool_capacity gauge") {
		t.Errorf("output missing TYPE line; got:\n%s", out)
	}
}

// TestWritePoolCapacity_onePoolFailureDoesNotBlankOthers proves a single
// pool's capacity-query failure is reported as an inert comment and does
// NOT suppress the other, healthy pools' real numbers — the exit code
// still flags that something needs attention.
func TestWritePoolCapacity_onePoolFailureDoesNotBlankOthers(t *testing.T) {
	pools := []poolEntry{
		{name: "feedback", dir: "/state/feedback"},
		{name: "worker", dir: "/state/worker"},
	}
	byDir := map[string]ccpool.Capacity{
		"/state/worker": {MaxSessions: 3, Free: 3},
	}
	errs := map[string]error{"/state/feedback": errors.New("pool dir unreadable")}
	var buf bytes.Buffer
	code := writePoolCapacity(&buf, pools, fakeCapacity(byDir, errs))
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want ExitError (one pool failed)", code)
	}
	out := buf.String()
	if !strings.Contains(out, `pg_router_ccpool_handler_pool_capacity{role="worker",pool="/state/worker",dim="max_sessions"} 3`) {
		t.Errorf("the healthy pool's metrics must still be written; got:\n%s", out)
	}
	if !strings.Contains(out, `# pool-capacity: role="feedback" pool="/state/feedback": pool dir unreadable`) {
		t.Errorf("the failed pool must be reported as a comment naming the failure; got:\n%s", out)
	}
	if strings.Contains(out, `role="feedback"`) && !strings.Contains(out, "# pool-capacity:") {
		t.Errorf("a failed pool must never emit a (fabricated) metric sample; got:\n%s", out)
	}
}

func TestRunPoolCapacity_requiresAtLeastOnePool(t *testing.T) {
	if code := runPoolCapacity(nil); code != conformance.ExitUsage {
		t.Errorf("runPoolCapacity(nil) = %d, want ExitUsage", code)
	}
}

func TestRunPoolCapacity_rejectsMalformedPoolFlag(t *testing.T) {
	if code := runPoolCapacity([]string{"--pool", "not-a-pair"}); code != conformance.ExitUsage {
		t.Errorf("runPoolCapacity(malformed --pool) = %d, want ExitUsage", code)
	}
}

func TestRunPoolCapacity_rejectsTrailingArgs(t *testing.T) {
	if code := runPoolCapacity([]string{"--pool", "review=/state/review", "extra"}); code != conformance.ExitUsage {
		t.Errorf("runPoolCapacity(trailing arg) = %d, want ExitUsage", code)
	}
}
