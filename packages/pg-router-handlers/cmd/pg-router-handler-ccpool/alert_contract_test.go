package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/failsig"
	"github.com/phillipgreenii/pg-router-handlers/internal/originprobe"
	"github.com/phillipgreenii/pg-router-handlers/internal/watchdog"
	"github.com/phillipgreenii/pg-router-handlers/internal/wire"
)

// The failure-reason strings, the decline kinds and the busy exit code are a
// handler-to-alert contract (design: "8. Consumer checklist"). This file pins,
// for every reason string this adapter can emit, which side of the
// pg-router-failure-rule the alert puts it on, and fails when the adapter can
// emit a reason that is not pinned (so a NEW reason makes someone decide whether
// the rule counts or excludes it), or when a pin goes stale.

// alertsPath is packages/pg-router/grafana/alerting/alerts.yaml and metricsPath
// the core's metrics.go, seen from this package's directory. The module's
// go-tests check already carries the packages/pg-router fileset.
const (
	alertsPath  = "../../../pg-router/grafana/alerting/alerts.yaml"
	metricsPath = "../../../pg-router/internal/metrics/metrics.go"
	precheckGo  = "../../internal/executor/precheck.go"
)

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

// skipReasonsInPrecheck lists every "skipped-..." string literal of precheck.go,
// so a skip reason added there is noticed even if nobody added it to the pins.
func skipReasonsInPrecheck(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(precheckGo)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`"(skipped-[a-z-]+)"`).FindAllStringSubmatch(string(b), -1) {
		seen[m[1]] = true
	}
	var out []string
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// coreSentinel reads a documented sentinel constant from the core's metrics.go.
func coreSentinel(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(metricsPath)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^const ` + name + ` = "([^"]+)"`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("metrics.go has no const %s", name)
	}
	return m[1]
}

// reasonPin is what the design pins for one reason: the decline kind (empty when
// the reason is not a decline) and whether the failure-rate rule counts it.
type reasonPin struct{ kind, disposition string }

const (
	counted  = "counted"
	excluded = "excluded"
)

func TestAlertContract_EveryEmittableReasonIsKnownToTheFailureRateRule(t *testing.T) {
	rule := failureRateExclusion(t)

	// Decline reasons, from the real sentinel-to-decline mapping.
	pins := map[string]reasonPin{
		"at-capacity":       {wire.DeclineKindBusy, excluded},
		"session-in-flight": {wire.DeclineKindBusy, excluded},
		"capacity-unknown":  {wire.DeclineKindFault, counted},
		"pool-unreadable":   {wire.DeclineKindFault, counted},
		"low-disk":          {wire.DeclineKindFault, counted},
		"launch-failed":     {wire.DeclineKindFault, excluded},
		// the origin gate and the precheck are neutral declines; a retryable
		// connector answer is busy but still counted, so a sustained outage pages.
		"origin-unavailable":    {wire.DeclineKindNeutral, excluded},
		"connector-unavailable": {wire.DeclineKindBusy, counted},
	}
	// A precheck skip is a neutral decline, except the MOOT outcomes (bead pg2-6t91v7),
	// which are completed as skipped: not declines at all (no kind), so they never
	// reach pg_router_failures_total. The rule's skipped-.+ matcher still covers
	// them (and the non-moot declines), so all of them stay excluded.
	for _, r := range skipReasonsInPrecheck(t) {
		if executor.Moot(r) {
			pins[r] = reasonPin{"", excluded}
			continue
		}
		pins[r] = reasonPin{wire.DeclineKindNeutral, excluded}
	}
	// Reasons the core derives from a handler ERROR's text when a session ends
	// badly: not declines, so no kind. The adapter emits them through the
	// sentinels its error text carries, pinned below against the core's own.
	pins["usage-limit"] = reasonPin{"", excluded}
	pins["budget-exceeded"] = reasonPin{"", excluded}
	// Failure signatures recorded on a failed dispatch (failure_signature): all
	// counted, except launch-failed, which is also the decline reason above.
	for _, s := range failsig.Signatures() {
		if _, isDecline := pins[string(s)]; isDecline {
			continue
		}
		pins[string(s)] = reasonPin{"", counted}
	}

	// What the adapter can emit, built from the code.
	emitted := map[string]string{} // reason -> decline kind ("" = not a decline)
	for _, err := range []error{
		executor.ErrPoolAtCapacity, executor.ErrSessionInFlight, executor.ErrPoolCapacityUnknown,
		executor.ErrPoolUnreadable, executor.ErrLowDisk, &executor.LaunchFailedError{Err: errors.New("x")},
	} {
		d, ok := wire.BusyDeclineFor(fmt.Errorf("wrapped: %w", err))
		if !ok {
			t.Fatalf("%v must map to a decline", err)
		}
		emitted[d.Reason] = d.Kind
	}
	emitted[originprobe.ReasonOriginUnavailable] = wire.DeclineKindNeutral
	emitted[executor.ReasonConnectorUnavailable] = wire.DeclineKindBusy
	for _, r := range skipReasonsInPrecheck(t) {
		if executor.Moot(r) {
			emitted[r] = "" // completed as skipped, not a decline
			continue
		}
		emitted[r] = wire.DeclineKindNeutral
	}
	emitted["usage-limit"] = ""
	emitted["budget-exceeded"] = ""
	for _, s := range failsig.Signatures() {
		if _, isDecline := emitted[string(s)]; !isDecline {
			emitted[string(s)] = ""
		}
	}
	// A command role's busy reason is not this adapter's: no ccpool sentinel maps to it.
	if d, ok := wire.BusyDeclineFor(executor.ErrPoolAtCapacity); !ok || d.Reason == wire.BusyReasonCommandBusy {
		t.Fatalf("a ccpool decline must not be command-busy: %+v", d)
	}

	for reason, kind := range emitted {
		pin, known := pins[reason]
		if !known {
			t.Errorf("reason %q is new: decide whether pg-router-failure-rate counts or excludes it, then pin it here", reason)
			continue
		}
		if pin.kind != kind {
			t.Errorf("reason %q is emitted with decline kind %q, the design pins %q", reason, kind, pin.kind)
		}
		got := counted
		if rule.MatchString(reason) {
			got = excluded
		}
		if got != pin.disposition {
			t.Errorf("reason %q is %s by the pg-router-failure-rate rule, the design pins it %s", reason, got, pin.disposition)
		}
	}
	for reason := range pins {
		if _, ok := emitted[reason]; !ok {
			t.Errorf("pinned reason %q is not emitted by the adapter any more", reason)
		}
	}
	// triager-failure is the core's per-role bulkhead for escalation-triage roles,
	// not an adapter reason; the rule still excludes it, which this checks so a
	// ccpool triager role keeps staying off the failure-rate series.
	if !rule.MatchString(coreSentinel(t, "ReasonTriagerFailure")) {
		t.Errorf("the failure-rate rule no longer excludes the triager-failure reason")
	}
}

// The reasons the core derives from error text are keyed on sentinel substrings;
// the adapter's errors must still carry exactly those, or the reasons silently
// stop being emitted.
func TestAlertContract_CoreSentinelsMatchTheAdapterErrors(t *testing.T) {
	if got, want := executor.UsageLimitEndSentinel, coreSentinel(t, "UsageLimitSentinel"); got != want {
		t.Errorf("executor.UsageLimitEndSentinel = %q, the core's UsageLimitSentinel is %q", got, want)
	}
	be := &watchdog.BudgetError{Role: "r"}
	if got, want := be.Error(), coreSentinel(t, "BudgetExceededSentinel"); !strings.Contains(got, want) {
		t.Errorf("a budget hard stop's text %q lacks the core's BudgetExceededSentinel %q", got, want)
	}
	if got := coreSentinel(t, "ReasonUsageLimit"); got != "usage-limit" {
		t.Errorf("core ReasonUsageLimit = %q", got)
	}
	if got := coreSentinel(t, "ReasonBudgetExceeded"); got != "budget-exceeded" {
		t.Errorf("core ReasonBudgetExceeded = %q", got)
	}
}
