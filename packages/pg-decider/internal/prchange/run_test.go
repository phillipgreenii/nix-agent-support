package prchange

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/config"
)

// script is a Runner double that answers each call by the first matching rule
// (a prefix of "name arg arg...") and records every call it saw.
type script struct {
	mu    sync.Mutex
	rules []scriptRule
	calls []string
	// inputs[i] is the standard input of calls[i].
	inputs []string
}

type scriptRule struct {
	prefix string
	res    Result
	// later, when set, are the answers of the second and later matching calls
	// (the last one repeats).
	later []Result
	n     int
}

func (s *script) on(prefix string, res Result) *script {
	s.rules = append(s.rules, scriptRule{prefix: prefix, res: res})
	return s
}

// onSeq answers the first matching call with first and the following ones with
// the later results in order, the last repeating.
func (s *script) onSeq(prefix string, first Result, later ...Result) *script {
	s.rules = append(s.rules, scriptRule{prefix: prefix, res: first, later: later})
	return s
}

func (s *script) run(_ context.Context, c Call) Result {
	line := strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " "))
	s.mu.Lock()
	s.calls = append(s.calls, line)
	s.inputs = append(s.inputs, c.Stdin)
	s.mu.Unlock()
	for i := range s.rules {
		r := &s.rules[i]
		if strings.HasPrefix(line, r.prefix) {
			s.mu.Lock()
			n := r.n
			r.n++
			s.mu.Unlock()
			if n > 0 && len(r.later) > 0 {
				return r.later[min(n-1, len(r.later)-1)]
			}
			return r.res
		}
	}
	return Result{Code: 1, Stderr: "script: no rule for " + line}
}

func (s *script) called(prefix string) bool {
	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

const testID = "acme/api#7"

func hydratedView(version int, stale bool, anchorState string) string {
	links := ""
	if anchorState != "" {
		links = fmt.Sprintf(`{"type":"issue","id":"bd-a","relation":"work","state":%q,"title":"acme/api#7: a change","metadata":{"dedup_key":"pr:acme/api#7:anchor"}}`, anchorState)
	}
	return fmt.Sprintf(`{"contract":"pg-desk.view/v1","type":"pr","id":"acme/api#7","version":%d,"stale":%t,"snapshot":null,"links":[%s]}`, version, stale, links)
}

func ok(stdout string) Result { return Result{Stdout: stdout} }

func exit(code int, stdout, stderr string) Result {
	return Result{Code: code, Stdout: stdout, Stderr: stderr}
}

func opts(t *testing.T, mode string, s *script) (Options, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	// Every run is recorded through the lane write path; a test that is not about
	// it gets a write path that accepts everything (a test that is about it adds
	// its own rule before calling opts, and the first matching rule wins).
	s.on("pg-desk pr lane-record", ok("recorded\n"))
	var out, errb bytes.Buffer
	return Options{
		Mode: mode, RawID: testID, LockDir: t.TempDir(), LockWait: 200 * time.Millisecond,
		Config: &config.Config{}, Runner: s.run, Stdout: &out, Stderr: &errb,
	}, &out, &errb
}

func TestRefreshExitsNeverRunTheDecider(t *testing.T) {
	cases := []struct {
		name   string
		refr   Result
		code   int
		class  string
		retry  bool
		stderr string // a substring the failure line must carry
	}{
		{"exit 1", exit(1, "", "old-schema store"), ExitHydrateError, ClassHydrateError, false, "old-schema store"},
		{"exit 2 degraded", exit(2, "class=degraded\n", "files failed"), ExitHydrateDegraded, ClassDegraded, true, "files failed"},
		{"exit 2 no class", exit(2, "", "files failed"), ExitHydrateDegraded, ClassDegraded, true, "files failed"},
		{"exit 3 transient", exit(3, "class=transient\n", "502"), ExitHydrateTransient, ClassTransient, true, "502"},
		{"exit 3 unauthenticated", exit(3, "class=unauthenticated\n", "bad credential"), ExitHydrateUnauthenticated, ClassUnauthenticated, false, "pg-connector auth status"},
		{"exit 3 hydrate_error", exit(3, "class=hydrate_error\n", "undecodable"), ExitHydrateError, ClassHydrateError, false, "undecodable"},
		{"exit 3 no class is never not found", exit(3, "", "something"), ExitHydrateError, ClassHydrateError, false, "something"},
		{"exit 3 unknown class", exit(3, "class=bogus\n", "x"), ExitHydrateError, ClassHydrateError, false, "x"},
		{"exit 4", exit(4, "", ""), ExitHydrateError, ClassHydrateError, false, "exited 4"},
		{"killed", Result{Code: -1, Signaled: true}, ExitHydrateTransient, ClassTransient, true, "signal: killed"},
		{"cannot start", Result{Code: -1, StartErr: fmt.Errorf("executable file not found")}, ExitHydrateError, ClassHydrateError, false, "not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := (&script{}).on("pg-desk pr refresh", c.refr)
			o, _, errb := opts(t, ModeApply, s)
			if got := Run(context.Background(), o); got != c.code {
				t.Fatalf("exit %d, want %d; stderr: %s", got, c.code, errb)
			}
			if s.called("pg-decider") {
				t.Fatal("the decider ran after a failed refresh")
			}
			line := strings.TrimSpace(errb.String())
			if strings.Contains(line, "\n") {
				t.Fatalf("failure is not one line: %q", line)
			}
			if !strings.Contains(line, "class="+c.class+")") || !strings.Contains(line, "stage=hydrate") || !strings.Contains(line, c.stderr) {
				t.Fatalf("failure line %q lacks the stage, class %s or %q", line, c.class, c.stderr)
			}
			if got := hasRetryWord(line, "unavailable"); got != c.retry {
				t.Fatalf("line %q carries unavailable = %v, want %v", line, got, c.retry)
			}
			if !c.retry {
				for _, w := range retryWords {
					if hasRetryWord(line, w) {
						t.Fatalf("non-retryable line %q carries router retry word %q", line, w)
					}
				}
			}
			if !strings.Contains(line, ": run ") {
				t.Fatalf("line %q does not end with a next command", line)
			}
			if code := c.code; code == 1 || code == 9 {
				t.Fatalf("exit code %d is reserved", code)
			}
		})
	}
}

func hasRetryWord(s, w string) bool { return strings.Contains(strings.ToLower(s), w) }

func TestNonRetryableLineScrubsUpstreamRetryWords(t *testing.T) {
	up := "Service Unavailable; context Deadline Exceeded; signal: killed"
	for _, c := range []struct {
		name string
		refr Result
	}{
		{"hydrate_error", exit(3, "class=hydrate_error\n", up)},
		{"unauthenticated", exit(3, "class=unauthenticated\n", up)},
		{"exit 1", exit(1, "", up)},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := (&script{}).on("pg-desk pr refresh", c.refr)
			o, _, errb := opts(t, ModeApply, s)
			Run(context.Background(), o)
			for _, w := range retryWords {
				if hasRetryWord(errb.String(), w) {
					t.Fatalf("line %q still carries %q", errb, w)
				}
			}
		})
	}
}

func TestNoChangeRefreshStillRunsTheDecider(t *testing.T) {
	s := (&script{}).
		on("pg-desk pr refresh", ok("refreshed pr acme/api#7: no change (version 3)\n")).
		on("pg-desk pr show", ok(hydratedView(3, false, ""))).
		on("pg-decider apply pr acme/api#7", ok(""))
	o, out, errb := opts(t, ModeApply, s)
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	if !s.called("pg-decider apply pr acme/api#7") {
		t.Fatal("the decider did not run after a no-change refresh")
	}
	if !strings.Contains(out.String(), "no change") {
		t.Fatalf("stdout %q lost the refresh line", out)
	}
	// Order: refresh, then show, then the decider (its read-only plan for the
	// record, then the apply), and the lane write path last.
	if len(s.calls) < 4 || !strings.HasPrefix(s.calls[0], "pg-desk pr refresh") || !strings.HasPrefix(s.calls[1], "pg-desk pr show") ||
		!strings.HasPrefix(s.calls[2], "pg-decider") || !strings.HasPrefix(s.calls[len(s.calls)-1], "pg-desk pr lane-record") {
		t.Fatalf("call order = %v", s.calls)
	}
	for i, c := range s.calls {
		if strings.HasPrefix(c, "pg-decider") && i < 2 {
			t.Fatalf("the decider ran before the view check: %v", s.calls)
		}
	}
}

func TestPlanModeRunsPlan(t *testing.T) {
	s := (&script{}).
		on("pg-desk pr refresh", ok("refreshed\n")).
		on("pg-desk pr show", ok(hydratedView(1, false, ""))).
		on("pg-decider plan pr acme/api#7", ok("pr acme/api#7 plan\n"))
	o, out, _ := opts(t, ModePlan, s)
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d", got)
	}
	if !strings.Contains(out.String(), "pr acme/api#7 plan") {
		t.Fatalf("plan output lost: %q", out)
	}
}

func TestUnhydratedViewIsRefusedWithADistinctCode(t *testing.T) {
	for _, c := range []struct {
		name, view string
	}{
		{"version 0", hydratedView(0, false, "")},
		{"stale", hydratedView(4, true, "")},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := (&script{}).on("pg-desk pr refresh", ok("refreshed\n")).on("pg-desk pr show", ok(c.view))
			o, _, errb := opts(t, ModeApply, s)
			if got := Run(context.Background(), o); got != ExitUnhydrated {
				t.Fatalf("exit %d, want %d: %s", got, ExitUnhydrated, errb)
			}
			if s.called("pg-decider") {
				t.Fatal("the decider ran on an unhydrated view")
			}
			if !strings.Contains(errb.String(), "class=unhydrated") {
				t.Fatalf("line %q", errb)
			}
		})
	}
}

func TestViewReadFailures(t *testing.T) {
	cases := []struct {
		name  string
		show  Result
		code  int
		retry bool
	}{
		{"show fails", exit(1, "", "store busy"), ExitHydrateTransient, true},
		{"show killed", Result{Code: -1, Signaled: true}, ExitHydrateTransient, true},
		{"show unparseable", ok("not json"), ExitHydrateError, false},
		{"show wrong contract", ok(`{"contract":"other/v9"}`), ExitHydrateError, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := (&script{}).on("pg-desk pr refresh", ok("refreshed\n")).on("pg-desk pr show", c.show)
			o, _, errb := opts(t, ModeApply, s)
			if got := Run(context.Background(), o); got != c.code {
				t.Fatalf("exit %d, want %d: %s", got, c.code, errb)
			}
			if hasRetryWord(errb.String(), "unavailable") != c.retry {
				t.Fatalf("line %q: retry = %v, want %v", errb, !c.retry, c.retry)
			}
			if s.called("pg-decider") {
				t.Fatal("the decider ran")
			}
		})
	}
}

// lookupCounters is the failed-closed counters line of a run whose lookup
// failed for reason.
func lookupCounters(reason string) string {
	return `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/api#7","rules":{},"escalations":0,"failures":{"` + reason + `":1}}`
}

func TestDeciderExitMapping(t *testing.T) {
	counters := `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/api#7","rules":{},"escalations":0}`
	cases := []struct {
		name  string
		res   Result
		code  int
		retry bool
	}{
		{"exit 2 is partial and retryable", exit(2, "", "rule x failed: Service Unavailable\n"+counters+"\n"), ExitDecidePartial, true},
		{"exit 3 is failed", exit(3, "", "cannot read the view: signal: killed\n"), ExitDecideFailed, false},
		{"exit 1 is failed", exit(1, "", "bad config: deadline exceeded"), ExitDecideFailed, false},
		{"exit 1 no text", exit(1, "", ""), ExitDecideFailed, false},
		{"killed is partial", Result{Code: -1, Signaled: true}, ExitDecidePartial, true},
		{"exit 4 with an unavailable lookup is transient and retryable", exit(4, "", "pg-decider: cannot apply pr acme/api#7: work-item lookup is unavailable: down\n"+lookupCounters("lookup-unavailable")+"\n"), ExitDecideUnavailable, true},
		{"exit 4 above the ceiling is failed", exit(4, "", "pg-decider: cannot apply pr acme/api#7: work-item lookup: the listing holds 9 work items, above the ceiling\n"+lookupCounters("lookup-above-ceiling")+"\n"), ExitDecideFailed, false},
		{"exit 4 truncated is failed", exit(4, "", "pg-decider: work-item lookup: truncated\n"+lookupCounters("lookup-truncated")+"\n"), ExitDecideFailed, false},
		{"exit 4 with no counters line is failed", exit(4, "", "pg-decider: work-item lookup is unavailable: down\n"), ExitDecideFailed, false},
		{"exit 1 naming an unavailable lookup is failed", exit(1, "", "x\n"+lookupCounters("lookup-unavailable")+"\n"), ExitDecideFailed, false},
		{"cannot start", Result{Code: -1, StartErr: fmt.Errorf("not found in PATH")}, ExitDecideFailed, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := (&script{}).
				on("pg-desk pr refresh", ok("refreshed\n")).
				on("pg-desk pr show", ok(hydratedView(2, false, ""))).
				on("pg-decider apply", c.res)
			o, _, errb := opts(t, ModeApply, s)
			if got := Run(context.Background(), o); got != c.code {
				t.Fatalf("exit %d, want %d: %s", got, c.code, errb)
			}
			var failure string
			for _, l := range strings.Split(strings.TrimSpace(errb.String()), "\n") {
				if strings.Contains(l, "pg-pr-change:") {
					failure = l
				}
			}
			if failure == "" || !strings.Contains(failure, "stage=decide") || !strings.Contains(failure, ": run pg-decider plan pr "+testID) {
				t.Fatalf("failure line %q is not re-wrapped with a stage and a next command", failure)
			}
			if hasRetryWord(failure, "unavailable") != c.retry {
				t.Fatalf("failure line %q: retry = %v, want %v", failure, !c.retry, c.retry)
			}
			if !c.retry {
				for _, w := range retryWords {
					if hasRetryWord(failure, w) {
						t.Fatalf("non-retryable line %q carries %q", failure, w)
					}
				}
			}
			if strings.Contains(c.res.Stderr, countersContract) && !strings.Contains(errb.String(), countersContract) {
				t.Fatal("the run counters line was not passed through")
			}
		})
	}
}

func TestDeciderDiagnosticsOnSuccessArePassedWithAPrefix(t *testing.T) {
	s := (&script{}).
		on("pg-desk pr refresh", ok("refreshed\n")).
		on("pg-desk pr show", ok(hydratedView(2, false, ""))).
		on("pg-decider apply", exit(0, "", "claimed, left running\n"))
	o, _, errb := opts(t, ModeApply, s)
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d", got)
	}
	if !strings.Contains(errb.String(), "pg-pr-change: decider: claimed, left running") {
		t.Fatalf("stderr %q", errb)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, c := range []struct{ mode, id string }{
		{"sync", testID},
		{"", testID},
		{"apply", "7"},
		{"apply", "https://example.test/o/r/pull/7"},
		{"apply", "acme/api#7@xyz"},
		{"apply", "acme/api#7@0123456789abcdef"},
		{"apply", ""},
	} {
		s := &script{}
		o, _, errb := opts(t, c.mode, s)
		o.RawID = c.id
		if got := Run(context.Background(), o); got != ExitUsage {
			t.Errorf("mode %q id %q: exit %d, want %d: %s", c.mode, c.id, got, ExitUsage, errb)
		}
		if len(s.calls) != 0 {
			t.Errorf("mode %q id %q ran %v", c.mode, c.id, s.calls)
		}
	}
}

func TestRetryWindowIDGivesTheSameCommandsAsTheBareID(t *testing.T) {
	s := (&script{}).
		on("pg-desk pr refresh", ok("refreshed\n")).
		on("pg-desk pr show", ok(hydratedView(2, false, ""))).
		on("pg-decider", ok(""))
	o, _, errb := opts(t, ModeApply, s)
	o.RawID = testID + "@0123456789ab"
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	if !s.called("pg-desk pr refresh acme/api#7") || !s.called("pg-decider apply pr acme/api#7") {
		t.Fatalf("the suffix leaked into a call: %v", s.calls)
	}
}

func TestLockWaitTimesOutAsTransient(t *testing.T) {
	dir := t.TempDir()
	held, err := AcquireLock(context.Background(), dir, testID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	for _, raw := range []string{testID, testID + "@0123456789ab", "ACME/API#7"} {
		s := &script{}
		o, _, errb := opts(t, ModeApply, s)
		o.LockDir, o.RawID, o.LockWait = dir, raw, 100*time.Millisecond
		if got := Run(context.Background(), o); got != ExitLockTimeout {
			t.Fatalf("%s: exit %d, want %d: %s", raw, got, ExitLockTimeout, errb)
		}
		if len(s.calls) != 0 {
			t.Fatalf("%s: a call ran without the lock: %v", raw, s.calls)
		}
		if !hasRetryWord(errb.String(), "unavailable") || !strings.Contains(errb.String(), "stage=lock") {
			t.Fatalf("%s: line %q", raw, errb)
		}
	}
	held.Release()
	s := (&script{}).on("pg-desk pr refresh", exit(3, "class=transient\n", "x"))
	o, _, _ := opts(t, ModeApply, s)
	o.LockDir = dir
	if got := Run(context.Background(), o); got != ExitHydrateTransient {
		t.Fatalf("after release: exit %d", got)
	}
}

func TestLockDirThatCannotBeCreatedIsNotRetryable(t *testing.T) {
	s := &script{}
	o, _, errb := opts(t, ModeApply, s)
	o.LockDir = "/dev/null/locks"
	if got := Run(context.Background(), o); got != ExitLockError {
		t.Fatalf("exit %d: %s", got, errb)
	}
	if hasRetryWord(errb.String(), "unavailable") {
		t.Fatalf("a broken lock directory is retryable: %q", errb)
	}
}

func TestTableHasNoReservedCodesAndDistinctRows(t *testing.T) {
	seen := map[int]bool{}
	pairs := map[string]bool{}
	for _, r := range Table {
		if r.Code == 1 || r.Code == 9 || r.Code == 0 {
			t.Errorf("reserved code %d in the table", r.Code)
		}
		if seen[r.Code] {
			t.Errorf("code %d appears twice", r.Code)
		}
		seen[r.Code] = true
		k := r.Stage + "/" + r.Class
		if pairs[k] {
			t.Errorf("%s appears twice", k)
		}
		pairs[k] = true
		if r.Retryable != (r.Class == ClassTransient || r.Class == ClassDegraded || r.Class == ClassDecidePartial) {
			t.Errorf("%s retryable = %v", k, r.Retryable)
		}
	}
	if ExitUsage == 1 || ExitUsage == 9 {
		t.Error("usage code is reserved")
	}
}
