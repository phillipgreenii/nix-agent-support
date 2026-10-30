package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/failsig"
	"github.com/phillipgreenii/pg-router-handlers/internal/wire"
)

// alertsPath is packages/pg-router/grafana/alerting/alerts.yaml seen from this
// package's directory. The module's go-tests check already carries the
// packages/pg-router fileset.
const alertsPath = "../../../pg-router/grafana/alerting/alerts.yaml"

// failureRateExclusion reads the reason exclusion pattern of the
// pg-router-failure-rate rule: the `reason!~"..."` matcher of its refId A query.
// A Prometheus regex matcher is fully anchored, so the pattern is returned
// anchored the same way.
func failureRateExclusion(t *testing.T) *regexp.Regexp {
	t.Helper()
	b, err := os.ReadFile(alertsPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	i := strings.Index(text, "- uid: pg-router-failure-rate\n")
	if i < 0 {
		t.Fatal("alerts.yaml has no pg-router-failure-rate rule")
	}
	rest := text[i+1:]
	// The rule ends where the next rule's uid starts.
	if j := strings.Index(rest, "- uid: "); j >= 0 {
		rest = rest[:j]
	}
	m := regexp.MustCompile(`(?m)^\s+expr: sum by \(class, role\) \(rate\(pg_router_failures_total\{reason!~"([^"]+)"\}`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatal("the pg-router-failure-rate rule has no `reason!~\"...\"` matcher in its refId A expression")
	}
	re := regexp.MustCompile("^(?:" + m[1] + ")$")
	// Proof the right matcher was read: the rule's long-standing exclusions.
	for _, r := range []string{"at-capacity", "usage-limit", "skipped-bead-closed"} {
		if !re.MatchString(r) {
			t.Fatalf("parsed exclusion %q does not exclude %q: wrong matcher", m[1], r)
		}
	}
	return re
}

// commandReasons are the reason strings this adapter can emit, each the string
// the alert rules would see, and the disposition the design pins for it:
//
//   - command-busy is the decline reason of an exit 9 (kind busy). It is
//     EXCLUDED by the failure-rate rule: a busy decline is backpressure, not a
//     failure (operator ruling 2026-10-29 on pg2-6j0lr5, precedent pg2-v3qn6),
//     the same routine-backpressure exclusion that at-capacity has.
//   - the failure signatures of a failed command (failsig.Classify of its
//     stderr) are recorded as failure_signature on the dispatch_result record.
//     Each is COUNTED too: a failing command is a real delivery failure.
//
// A signature the adapter cannot produce for a command is listed in
// notFromCommand with the reason, so a NEW signature in failsig fails this test
// until someone decides what the alert rule does with it.
var (
	commandReasonDisposition = map[string]string{
		wire.BusyReasonCommandBusy:   "excluded",
		string(failsig.GitAuth):      "counted",
		string(failsig.GitNetwork):   "counted",
		string(failsig.MountOrPath):  "counted",
		string(failsig.Budget):       "counted",
		string(failsig.IndexLock):    "counted",
		string(failsig.APITransient): "counted",
		string(failsig.APIRateLimit): "counted",
		string(failsig.APIAuth):      "counted",
		string(failsig.ContextLimit): "counted",
		string(failsig.Unknown):      "counted",
	}
	notFromCommand = map[string]string{
		// Assigned by the ccpool executor directly for a pre-ready session launch;
		// a command has no session. (The rule excludes it: role-progress pages it.)
		string(failsig.LaunchFailed): "ccpool launch failure",
		// The session-exit family comes from failsig.ClassifyExit over ccpool session
		// facts; a command failure is classified from its stderr text alone.
		string(failsig.SessionErrored): "ccpool session exit",
		string(failsig.SessionIdle):    "ccpool session exit",
		string(failsig.SessionGone):    "ccpool session exit",
	}
)

func TestAlertContract_EveryEmittableReasonIsKnownToTheFailureRateRule(t *testing.T) {
	excluded := failureRateExclusion(t)

	// The decline this adapter emits for exit 9 is exactly command-busy, kind busy.
	d, ok := wire.BusyDeclineFor(fmt.Errorf("command role: %w", executor.ErrBusy))
	if !ok || d.Reason != "command-busy" || d.Kind != wire.DeclineKindBusy {
		t.Fatalf("exit 9 maps to %+v (busy=%v), want reason command-busy kind busy", d, ok)
	}

	emitted := []string{d.Reason}
	for _, s := range failsig.Signatures() {
		if _, skip := notFromCommand[string(s)]; skip {
			continue
		}
		emitted = append(emitted, string(s))
	}

	for _, reason := range emitted {
		want, known := commandReasonDisposition[reason]
		if !known {
			t.Errorf("reason %q is new: decide whether pg-router-failure-rate counts or excludes it, then pin it here", reason)
			continue
		}
		got := "counted"
		if excluded.MatchString(reason) {
			got = "excluded"
		}
		if got != want {
			t.Errorf("reason %q is %s by the pg-router-failure-rate rule, the design pins it %s", reason, got, want)
		}
	}
	// The pins must not name a reason the adapter cannot emit.
	for reason := range commandReasonDisposition {
		found := false
		for _, e := range emitted {
			found = found || e == reason
		}
		if !found {
			t.Errorf("pinned reason %q is not emitted by the adapter any more", reason)
		}
	}
}
