package main

// Regression guard for the pg-connector-thread-slack availability alert rule
// pg-conn-thread-slack-unavailable in
// grafana/alerting/thread-slack-alerts.yaml (bead pg2-3l5l9).
//
// The bug: the rule counted failures in a 5m window and required that to hold
// for 10m, but the thread-me source polls this backend about every 30 minutes,
// so a failing outage writes ONE log line per ~30m. One line keeps a 5m window
// non-empty for 5m, never 10 continuous minutes, so the rule could never leave
// Pending and never fired.
//
// No Loki or Grafana engine is a dependency of this module, so the two pieces
// of semantics that matter are modelled here and the YAML is read, not copied:
//
//   - LogQL `count_over_time(<selector> [W])` at an evaluation instant t counts
//     the matching log lines with timestamp in (t-W, t].
//   - Grafana's unified-alerting state machine for a rule with `for: F`,
//     `noDataState: OK` and a `gt N` threshold over the reduced count: a
//     condition that is false or has no data is Normal (and resets the pending
//     timer); a condition that turns true is Pending at that evaluation, and
//     Firing at the first evaluation at least F after it turned true.
//
// The rule's window, threshold, `for`, evaluation interval and the
// relativeTimeRange are all parsed out of the YAML, so editing any of them
// re-runs the cadence arithmetic against the new numbers.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// threadSlackPollCadence is how often the thread-me source calls
// pg-connector-thread-slack (one `claude -p` list call per poll); bead
// pg2-3l5l9 records "about every 30 minutes".
const threadSlackPollCadence = 30 * time.Minute

const threadSlackAvailabilityUID = "pg-conn-thread-slack-unavailable"

// alertRule is the slice of a provisioned Grafana rule this guard reads.
type alertRule struct {
	UID         string            `yaml:"uid"`
	For         string            `yaml:"for"`
	NoDataState string            `yaml:"noDataState"`
	Annotations map[string]string `yaml:"annotations"`
	Data        []alertQuery      `yaml:"data"`
}

type alertQuery struct {
	RefID             string `yaml:"refId"`
	RelativeTimeRange struct {
		From int `yaml:"from"`
	} `yaml:"relativeTimeRange"`
	Model struct {
		Expr       string `yaml:"expr"`
		Conditions []struct {
			Evaluator struct {
				Type   string `yaml:"type"`
				Params []int  `yaml:"params"`
			} `yaml:"evaluator"`
		} `yaml:"conditions"`
	} `yaml:"model"`
}

// ruleModel is the numeric shape of one rule: everything the replay needs.
type ruleModel struct {
	window    time.Duration // the LogQL range, count_over_time(... [window])
	threshold int           // fires when count > threshold
	forDur    time.Duration
	interval  time.Duration // evaluation interval of the rule group
}

func loadThreadSlackRule(t *testing.T) (alertRule, ruleModel) {
	t.Helper()
	path := filepath.Join(pgConnectorModuleRoot(t), "grafana", "alerting", "thread-slack-alerts.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Groups []struct {
			Interval string      `yaml:"interval"`
			Rules    []alertRule `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, g := range doc.Groups {
		for _, r := range g.Rules {
			if r.UID != threadSlackAvailabilityUID {
				continue
			}
			return r, buildRuleModel(t, r, g.Interval)
		}
	}
	t.Fatalf("rule %s not found in %s", threadSlackAvailabilityUID, path)
	return alertRule{}, ruleModel{}
}

var logqlRange = regexp.MustCompile(`\[(\d+)([smh])\]`)

func buildRuleModel(t *testing.T, r alertRule, groupInterval string) ruleModel {
	t.Helper()
	var a, c *alertQuery
	for i := range r.Data {
		switch r.Data[i].RefID {
		case "A":
			a = &r.Data[i]
		case "C":
			c = &r.Data[i]
		}
	}
	if a == nil || c == nil {
		t.Fatalf("rule %s needs refIds A and C", r.UID)
	}

	m := logqlRange.FindAllStringSubmatch(a.Model.Expr, -1)
	if len(m) != 1 {
		t.Fatalf("rule %s expr must carry exactly one [<n><unit>] range, got %q", r.UID, a.Model.Expr)
	}
	n, _ := strconv.Atoi(m[0][1])
	unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour}[m[0][2]]
	window := time.Duration(n) * unit

	// The query's relativeTimeRange MUST span the LogQL range, or Grafana
	// evaluates the instant query over less history than the rule claims.
	if got := time.Duration(a.RelativeTimeRange.From) * time.Second; got != window {
		t.Errorf("relativeTimeRange.from = %v, want it equal to the LogQL range %v", got, window)
	}

	if len(c.Model.Conditions) != 1 || c.Model.Conditions[0].Evaluator.Type != "gt" || len(c.Model.Conditions[0].Evaluator.Params) != 1 {
		t.Fatalf("rule %s: refId C must be a single gt <n> threshold", r.UID)
	}
	forDur, err := time.ParseDuration(r.For)
	if err != nil {
		t.Fatalf("rule %s: for %q: %v", r.UID, r.For, err)
	}
	interval, err := time.ParseDuration(groupInterval)
	if err != nil {
		t.Fatalf("group interval %q: %v", groupInterval, err)
	}
	if r.NoDataState != "OK" {
		t.Errorf("rule %s: noDataState = %q, want OK (a quiet log is the healthy case; the model treats no data as Normal)", r.UID, r.NoDataState)
	}
	return ruleModel{window: window, threshold: c.Model.Conditions[0].Evaluator.Params[0], forDur: forDur, interval: interval}
}

type alertState string

const (
	stateNormal  alertState = "Normal"
	statePending alertState = "Pending"
	stateFiring  alertState = "Firing"
)

// replay evaluates the rule every interval over [interval, horizon] against
// failures (log-line timestamps as offsets from t=0) and returns the state
// at every evaluation instant.
func (m ruleModel) replay(failures []time.Duration, horizon time.Duration) map[time.Duration]alertState {
	states := map[time.Duration]alertState{}
	var pendingSince time.Duration
	pending := false
	for at := m.interval; at <= horizon; at += m.interval {
		count := 0
		for _, f := range failures {
			if f > at-m.window && f <= at { // (at-window, at]
				count++
			}
		}
		if count > m.threshold {
			if !pending {
				pending, pendingSince = true, at
			}
			if at-pendingSince >= m.forDur {
				states[at] = stateFiring
			} else {
				states[at] = statePending
			}
		} else { // false, or no series at all (NoData -> OK): back to Normal
			pending = false
			states[at] = stateNormal
		}
	}
	return states
}

// failingPolls returns n failure timestamps `every` apart starting at start.
func failingPolls(start time.Duration, n int, every time.Duration) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = start + time.Duration(i)*every
	}
	return out
}

func firstAt(states map[time.Duration]alertState, want alertState, from time.Duration, interval time.Duration, horizon time.Duration) (time.Duration, bool) {
	for at := from; at <= horizon; at += interval {
		if states[at] == want {
			return at, true
		}
	}
	return 0, false
}

// TestThreadSlackUnavailableFiresOnThreeFailingPollsAt30m is the acceptance
// case: 3 consecutive failing polls at the poll cadence reach Firing, and a
// persistent outage stays Firing for as long as polls keep failing.
func TestThreadSlackUnavailableFiresOnThreeFailingPollsAt30m(t *testing.T) {
	_, m := loadThreadSlackRule(t)
	start := 10 * time.Minute
	failures := failingPolls(start, 3, threadSlackPollCadence) // 10m, 40m, 70m
	third := failures[2]

	horizon := 24 * time.Hour
	states := m.replay(failures, horizon)
	firing, ok := firstAt(states, stateFiring, m.interval, m.interval, horizon)
	if !ok {
		t.Fatalf("3 failing polls at %v never reached Firing (window %v, count > %d, for %v)",
			threadSlackPollCadence, m.window, m.threshold, m.forDur)
	}
	if firing < third || firing > third+m.forDur+m.interval {
		t.Errorf("fired at %v, want within for+1 tick of the third failure at %v", firing, third)
	}

	// A persistent outage (a failing poll every 30m for 12h) is Firing at
	// every evaluation from the first fire to the last failure.
	outage := failingPolls(start, 24, threadSlackPollCadence)
	last := outage[len(outage)-1]
	states = m.replay(outage, last+m.window+time.Hour)
	first, ok := firstAt(states, stateFiring, m.interval, m.interval, last)
	if !ok {
		t.Fatal("a 12h outage at the 30m cadence never reached Firing")
	}
	for at := first; at <= last; at += m.interval {
		if states[at] != stateFiring {
			t.Fatalf("outage at %v: state %s, want Firing for the whole outage", at, states[at])
		}
	}
}

// TestThreadSlackUnavailableToleratesPollJitter pins that "about every 30
// minutes" has slack: polls up to just under 45m apart (the window is open at its left edge, so exactly 45m apart is out) still reach the threshold.
func TestThreadSlackUnavailableToleratesPollJitter(t *testing.T) {
	_, m := loadThreadSlackRule(t)
	for _, every := range []time.Duration{25 * time.Minute, 35 * time.Minute, 40 * time.Minute, 44 * time.Minute} {
		failures := failingPolls(5*time.Minute, 3, every)
		states := m.replay(failures, 6*time.Hour)
		if _, ok := firstAt(states, stateFiring, m.interval, m.interval, 6*time.Hour); !ok {
			t.Errorf("3 failing polls %v apart never reached Firing (window %v)", every, m.window)
		}
	}
}

// TestThreadSlackUnavailableIsolatedFailureNeverPends pins that one failure
// (or two) followed by healthy polls leaves the rule Normal throughout: it is
// never Pending or Firing, so nothing can be stuck beyond the stated window.
func TestThreadSlackUnavailableIsolatedFailureNeverPends(t *testing.T) {
	_, m := loadThreadSlackRule(t)
	for n := 1; n <= m.threshold; n++ {
		failures := failingPolls(10*time.Minute, n, threadSlackPollCadence)
		states := m.replay(failures, 6*time.Hour)
		for at, s := range states {
			if s != stateNormal {
				t.Errorf("%d failure(s) then healthy polls: state %s at %v, want Normal at every evaluation", n, s, at)
			}
		}
	}
}

// TestThreadSlackUnavailableClearsWithinOneWindowOfLastFailure pins the
// "clears within <window> of the last failure" claim the description makes.
func TestThreadSlackUnavailableClearsWithinOneWindowOfLastFailure(t *testing.T) {
	_, m := loadThreadSlackRule(t)
	failures := failingPolls(10*time.Minute, 6, threadSlackPollCadence)
	last := failures[len(failures)-1]
	horizon := last + 3*m.window
	states := m.replay(failures, horizon)
	if _, ok := firstAt(states, stateFiring, m.interval, m.interval, horizon); !ok {
		t.Fatal("outage never fired")
	}
	for at := last + m.window; at <= horizon; at += m.interval {
		if states[at] != stateNormal {
			t.Errorf("state %s at %v, want Normal one window (%v) after the last failure at %v", states[at], at, m.window, last)
		}
	}
}

// TestThreadSlackUnavailableOldShapeCouldNeverFire keeps the model honest: the
// shape this bead replaced (5m window, for 10m, count > 0) must NOT fire under
// the same replay, so a model that cannot tell the two apart fails here.
func TestThreadSlackUnavailableOldShapeCouldNeverFire(t *testing.T) {
	old := ruleModel{window: 5 * time.Minute, threshold: 0, forDur: 10 * time.Minute, interval: time.Minute}
	failures := failingPolls(10*time.Minute, 24, threadSlackPollCadence)
	states := old.replay(failures, 14*time.Hour)
	if at, ok := firstAt(states, stateFiring, old.interval, old.interval, 14*time.Hour); ok {
		t.Fatalf("old 5m/for-10m shape fired at %v; the model no longer reproduces the bug", at)
	}
	if _, ok := firstAt(states, statePending, old.interval, old.interval, 14*time.Hour); !ok {
		t.Fatal("old shape never went Pending; the model no longer reproduces the observed Pending-then-Normal behaviour")
	}
}

// TestThreadSlackUnavailableRuleTextStatesWindowAndCount pins that the
// summary and description say what window and failure count the rule means,
// derived from the YAML numbers (count = threshold+1).
func TestThreadSlackUnavailableRuleTextStatesWindowAndCount(t *testing.T) {
	r, m := loadThreadSlackRule(t)
	wantCount := fmt.Sprintf("%d or more", m.threshold+1)
	wantWindow := fmt.Sprintf("%d minutes", int(m.window/time.Minute))
	for _, key := range []string{"summary", "description"} {
		text := r.Annotations[key]
		if !strings.Contains(text, wantCount) {
			t.Errorf("annotation %s = %q, want it to state the failure count %q", key, text, wantCount)
		}
		if !strings.Contains(text, wantWindow) {
			t.Errorf("annotation %s = %q, want it to state the window %q", key, text, wantWindow)
		}
	}
}

// TestThreadSlackUnavailableWindowSizedToPollCadence states the sizing rule
// directly: the window must hold threshold+1 polls at the nominal cadence with
// room to spare, or a persistent outage cannot reach the threshold.
func TestThreadSlackUnavailableWindowSizedToPollCadence(t *testing.T) {
	_, m := loadThreadSlackRule(t)
	needed := time.Duration(m.threshold) * threadSlackPollCadence // span of threshold+1 polls
	if m.window <= needed {
		t.Errorf("window %v cannot hold %d polls %v apart (span %v)", m.window, m.threshold+1, threadSlackPollCadence, needed)
	}
	if m.window < 2*threadSlackPollCadence {
		t.Errorf("window %v is shorter than two poll intervals (%v)", m.window, 2*threadSlackPollCadence)
	}
}
