// checks.go: the deterministic health checks run performs, each
// independently testable via fixtures [design: "ccpool-probe run
// checks"]:
//
//  1. checkNeedsInput — LIVE ccpool sessions in needs_input state that
//     pg-router-ccpool-handler dispatched (item 1).
//  2. checkZombieDrift — "zombie" session count (state working AND not
//     live) vs. a persisted baseline (item 2).
//  3. checkNeverPrompted — live sessions parked in ccpool's "ready" state
//     (launched, no turn ever started) across two consecutive runs
//     (bead pg2-bkzrc).
//
// Checks 1 and 3 run once PER ccpool pool (pools.go) and name the pool in
// the finding; check 2 compares one count aggregated across every scanned
// pool.
//
// Every function here is a pure function of its typed inputs: it makes no
// subprocess call and touches no file — that plumbing lives in
// ccpoolexec.go/connector.go (subprocess) and snapshot.go (persistence);
// run.go wires the two together. This split is this packet's own
// implementation choice (mirroring packages/pg-router-probe/cmd/pg-router-probe/checks.go's
// own rationale), made so each check's OWN decision logic (what counts as
// a real finding) can be fixture-tested without a real ccpool/pg-connector
// binary or real snapshot I/O.
package main

import "fmt"

// findingKind identifies which of the checks produced a finding.
type findingKind string

const (
	kindNeedsInput  findingKind = "needs-input"
	kindZombieDrift findingKind = "zombie-drift"
	// kindNeverPrompted: a live session sitting in "ready" — launched but
	// never given a prompt (bead pg2-bkzrc).
	kindNeverPrompted findingKind = "never-prompted"
)

// finding is one real, new-or-changed detection surfaced by a check.
// State carries the value dedup.go's "nothing new" comparison keys off,
// specific to Kind:
//   - kindNeedsInput: the fixed literal "needs_input" (this probe has no
//     visibility, via `ccpool list --json`, into a session's actual
//     pending-question TEXT, so "a materially different question is now
//     pending" [Binding decisions: "'Nothing new' rule" bullet 2] cannot
//     be distinguished from "still the same question" — this is this
//     packet's own documented simplification, not re-derived by the
//     design. In practice this means: file once per stuck session, then
//     stay quiet on every subsequent run while it remains needs_input,
//     which is exactly the desired "nothing new" behavior for the common
//     case).
//   - kindZombieDrift: the current severity band name (checkZombieDrift);
//     see fingerprint.go's zombieDriftFingerprint for how a band CHANGE
//     alone (not this State field) is what actually drives a fresh bead.
type finding struct {
	Kind        findingKind
	Fingerprint string
	Summary     string
	Evidence    string
	State       string
}

// checkNeedsInput builds one finding per row already filtered to
// needs_input state and pg-router's own session pool (ccpoolexec.go's
// listCcpoolSessions with state="needs_input") [design: item 1]. An empty
// input yields zero findings, not an error.
//
// Only LIVE rows are reported (pg2-d845f): a needs_input row whose
// process is gone (live=false) is not "stuck waiting on an answer", it is
// a dead record nobody can answer, and ccpool's list does not prune it.
// Reporting it would file a bead for history, not for an actionable
// session.
func checkNeedsInput(pool poolRef, rows []ccpoolSessionRow) []finding {
	findings := make([]finding, 0, len(rows))
	for _, r := range rows {
		if !r.Live {
			continue
		}
		findings = append(findings, finding{
			Kind:        kindNeedsInput,
			Fingerprint: needsInputFingerprint(pool.fingerprintScope(), r.ExternalID),
			Summary:     fmt.Sprintf("ccpool session %s in pool %s is stuck in needs_input", r.ExternalID, pool.Label),
			Evidence:    fmt.Sprintf("pool=%s\nexternal_id=%s\nname=%s\nstate=%s\ncwd=%s", pool.Label, r.ExternalID, r.Name, r.State, r.CWD),
			State:       "needs_input",
		})
	}
	return findings
}

// readyKey identifies one session across runs and pools in the persisted
// snapshot (snapshot.ReadySeen).
func readyKey(pool poolRef, externalID string) string {
	return pool.Label + "\x00" + externalID
}

// checkNeverPrompted flags every LIVE session in ccpool's "ready" state
// that was ALSO live-and-ready in the previous run (prevReady, keyed by
// readyKey) — i.e. it has sat launched-but-unprompted for at least one full
// probe interval. A single observation is never a finding: the handler's
// Ensure->Send window legitimately leaves a session briefly ready, and
// ccpool's list --json carries no state-entry timestamp to age it by. It
// also returns this run's complete ready key set for run.go to persist
// (unconditionally — including on the first run, which has no baseline and
// therefore reports nothing). Pure: no I/O.
func checkNeverPrompted(pool poolRef, rows []ccpoolSessionRow, prevReady map[string]bool) ([]finding, []string) {
	var findings []finding
	var current []string
	for _, r := range rows {
		if r.State != "ready" || !r.Live {
			continue
		}
		key := readyKey(pool, r.ExternalID)
		current = append(current, key)
		if !prevReady[key] {
			continue
		}
		findings = append(findings, finding{
			Kind:        kindNeverPrompted,
			Fingerprint: neverPromptedFingerprint(pool.Label, r.ExternalID),
			Summary:     fmt.Sprintf("ccpool session %s in pool %s is live but has never been prompted (stuck in ready)", r.ExternalID, pool.Label),
			Evidence:    fmt.Sprintf("pool=%s\nexternal_id=%s\nname=%s\nstate=%s\nlive=true\ncwd=%s", pool.Label, r.ExternalID, r.Name, r.State, r.CWD),
			State:       "ready",
		})
	}
	return findings, current
}

// severityZombieBand classifies how much the working-and-dead "zombie"
// session count has drifted from the persisted baseline. Thresholds
// (below, in classifyZombieBand) are this packet's own implementation
// choice — no design citation: exact severity-band thresholds are
// explicitly left undecided by the design [Binding decisions: "'Nothing
// new' rule" bullet, "exact thresholds are an implementation detail, not
// re-derived here"] — chosen only to be deterministic and
// fixture-testable, not tuned against real traffic.
type severityZombieBand string

const (
	bandBaseline   severityZombieBand = "baseline"
	band50Percent  severityZombieBand = "+50%"
	band100Percent severityZombieBand = "+100%"
	bandSustained  severityZombieBand = "sustained-growth"
)

// classifyZombieBand compares current against previous, folding in
// consecutiveGrowthBefore (how many PRIOR consecutive runs in a row
// already observed growth) so a string of small, individually
// sub-threshold increases is still eventually caught by the
// sustained-growth band, not just a single large jump:
//   - current <= previous: no growth this run -> bandBaseline, and the
//     consecutive-growth streak resets to 0.
//   - otherwise the streak increments; 3+ consecutive growing runs is
//     always sustained-growth, regardless of this run's own ratio;
//     below that, a zero baseline (any positive count is a full jump) or
//     a >=100% increase is band100Percent, a >=50% increase is
//     band50Percent, and anything smaller is bandBaseline (no finding
//     yet — but the streak still counts toward sustained-growth).
func classifyZombieBand(previous, current, consecutiveGrowthBefore int) (severityZombieBand, int) {
	if current <= previous {
		return bandBaseline, 0
	}
	streak := consecutiveGrowthBefore + 1
	if streak >= 3 {
		return bandSustained, streak
	}
	if previous == 0 {
		return band100Percent, streak
	}
	ratio := float64(current-previous) / float64(previous)
	switch {
	case ratio >= 1.0:
		return band100Percent, streak
	case ratio >= 0.5:
		return band50Percent, streak
	default:
		return bandBaseline, streak
	}
}

// checkZombieDrift compares the current zombie count (countZombieSessions)
// against the previous snapshot's own reading [design: item 2]. It
// returns (nil, 0) whenever there is no prior baseline to diff against —
// this run is quiet on drift by construction, matching checkQueueGrowth's
// own no-baseline behavior in pg-router-probe. Otherwise it always
// returns the fresh consecutive-growth streak (for snapshot.go to
// persist, regardless of whether THIS run's band was alert-worthy), and a
// non-nil finding only when the band is not bandBaseline [Binding
// decisions: "'Nothing new' rule" bullet, zombie-count half; dedup.go's
// zombieDriftFingerprint is what actually realizes "skip unless moved
// into a new band" — this function only decides whether the CURRENT
// band is alert-worthy at all].
func checkZombieDrift(hasPrevious bool, previous, current, consecutiveGrowthBefore int) (*finding, int) {
	if !hasPrevious {
		return nil, 0
	}
	band, streak := classifyZombieBand(previous, current, consecutiveGrowthBefore)
	if band == bandBaseline {
		return nil, streak
	}
	return &finding{
		Kind:        kindZombieDrift,
		Fingerprint: zombieDriftFingerprint(band),
		Summary:     fmt.Sprintf("working-and-dead zombie session count is growing (%d -> %d, %s band)", previous, current, band),
		Evidence:    fmt.Sprintf("previous=%d\ncurrent=%d\nband=%s\nconsecutive_growth_runs=%d", previous, current, band, streak),
		State:       string(band),
	}, streak
}

// countZombieSessions counts rows in ccpool's "working" state whose
// process is NOT live (state == working && !live): a session ccpool
// believes is mid-turn but whose process is gone — the zombie signal
// ccpool's own metric documents (packages/ccpool/internal/telemetry/
// metrics.go: live=false, state=working) [design: Contract's "ccpool's
// own list CLI" bullet; "ccpool-probe run checks" item 2].
//
// Deliberately NOT counted (pg2-d845f):
//   - errored (live or dead): errored history only ever grows (ccpool
//     keeps the rows), so counting it would trip drift on ordinary churn;
//     an errored session is a recorded failure, not a hung one.
//   - starting/ready && !live: a dead ready/starting row never reached a
//     turn, so it is not a hung working session; live ready sessions are
//     the never-prompted check's concern (checkNeverPrompted), and dead
//     ones are inert history.
//   - any live row: a live working session is healthy, and a live errored
//     one is a recorded failure of a running process, not a vanished one.
//   - idle/needs_input/done: reached their expected resting state.
func countZombieSessions(rows []ccpoolSessionRow) int {
	n := 0
	for _, r := range rows {
		if r.State == "working" && !r.Live {
			n++
		}
	}
	return n
}
