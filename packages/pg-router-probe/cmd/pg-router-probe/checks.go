// checks.go: the three deterministic health checks run performs, each
// independently testable via fixtures [design: "pg-router-probe run
// checks"]:
//
//  1. checkGrafanaAlerts — Grafana currently-firing alerts filtered to the
//     4 registered rule UIDs (item 1).
//  2. checkQueueGrowth — queue/backlog drift vs. a persisted last-run
//     snapshot (item 2).
//  3. checkBinaryHash — daemon/handler binary hash sanity: an unexpected
//     change with no corresponding deploy record (item 3).
//
// Every function here is a pure function of its typed inputs: it makes no
// HTTP call, runs no subprocess, and touches no file — that plumbing
// lives in grafana.go/connector.go (network/subprocess) and snapshot.go
// (persistence); run.go wires the two together. This split is this
// packet's own implementation choice, made so each check's OWN decision
// logic (what counts as "a real finding") can be fixture-tested without
// a live Grafana server, a real pg-connector binary, or real snapshot
// I/O.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// findingKind identifies which of the three checks produced a finding —
// also the wire value each finding's rendered body implicitly carries via
// its own Kind-specific Summary/Evidence text.
type findingKind string

const (
	kindGrafanaAlert       findingKind = "grafana-alert"
	kindQueueGrowth        findingKind = "queue-growth"
	kindBinaryHashMismatch findingKind = "binary-hash-mismatch"
)

// finding is one real, new-or-changed detection surfaced by a check.
// State carries the value dedup.go's "nothing new" comparison keys off,
// specific to Kind:
//   - kindGrafanaAlert: the alert's own current state (e.g. "firing").
//   - kindQueueGrowth: the current severity band name.
//   - kindBinaryHashMismatch: the current binary hash.
type finding struct {
	Kind         findingKind
	Fingerprint  string
	Aliases      []string // non-empty only for kindGrafanaAlert
	Summary      string
	Evidence     string
	State        string
	EpisodeCount int // kindGrafanaAlert only; 0 for the other two kinds
}

// checkGrafanaAlerts builds one finding per currently-firing alert already
// filtered to the 4 registered rule UIDs (the filtering itself happens in
// grafana.go's FiringAlerts, since it is a property of the Grafana query,
// not of this decision function) [design: item 1]. A non-firing/no-op
// input yields zero findings, not an error.
func checkGrafanaAlerts(alerts []grafanaAlert) []finding {
	findings := make([]finding, 0, len(alerts))
	for _, a := range alerts {
		fp := grafanaAlertFingerprint(a.RuleUID, a.Labels)
		findings = append(findings, finding{
			Kind:        kindGrafanaAlert,
			Fingerprint: fp,
			// The alias carries the SAME fingerprint today (this probe has
			// no other identity for the same alert to alias yet) — dedup.go
			// still checks it, so a future rule-uid/label rename that
			// starts supplying a genuinely different alias here is honored
			// without a dedup.go change [design: "Fingerprint metadata key"
			// paragraph, first bullet].
			Aliases:      []string{fp},
			Summary:      fmt.Sprintf("Grafana alert %s is firing", a.RuleUID),
			Evidence:     grafanaAlertEvidence(a),
			State:        a.State,
			EpisodeCount: a.EpisodeCount,
		})
	}
	return findings
}

// severityBand classifies a raw queue-depth/backlog reading. Thresholds
// are this packet's own implementation choice (no design citation: exact
// severity-band thresholds are explicitly left undecided by the design) —
// chosen only to be deterministic and fixture-testable, not tuned against
// real traffic.
type severityBand string

const (
	bandNone   severityBand = "none"
	bandLow    severityBand = "low"
	bandMedium severityBand = "medium"
	bandHigh   severityBand = "high"
)

func classifyBand(value int) severityBand {
	switch {
	case value < 10:
		return bandNone
	case value < 50:
		return bandLow
	case value < 200:
		return bandMedium
	default:
		return bandHigh
	}
}

// checkQueueGrowth compares current against the previous snapshot's own
// reading for one kind ("queue-depth" or "backlog"). It returns nil
// (no finding) whenever there is no prior baseline to diff against, the
// value did not grow, or the current band is bandNone — a finding exists
// only once growth has pushed the value into a non-none band [design:
// item 2; Binding decisions' queue-growth-specific "'Nothing new' rule"
// text governs whether an ALREADY-OPEN bead needs a fresh comment, which
// is dedup.go's job, not this function's].
func checkQueueGrowth(kind string, hasPrevious bool, previous, current int) *finding {
	if !hasPrevious {
		return nil
	}
	if current <= previous {
		return nil
	}
	band := classifyBand(current)
	if band == bandNone {
		return nil
	}
	return &finding{
		Kind:        kindQueueGrowth,
		Fingerprint: queueGrowthFingerprint(kind),
		Summary:     fmt.Sprintf("%s is growing (%d -> %d, %s band)", kind, previous, current, band),
		Evidence:    fmt.Sprintf("kind=%s\nprevious=%d\ncurrent=%d\nband=%s", kind, previous, current, band),
		State:       string(band),
	}
}

// checkBinaryHash compares the current binary hash against the previous
// snapshot's own stored hash. deployExpected is the caller's own
// "corresponding deploy record" verdict (deployrecord.go) — this function
// itself makes no I/O decision about what counts as expected, only what
// to do given that verdict [design: item 3].
func checkBinaryHash(hasPrevious bool, previousHash, currentHash string, deployExpected bool) *finding {
	if !hasPrevious || previousHash == "" {
		return nil
	}
	if previousHash == currentHash {
		return nil
	}
	if deployExpected {
		return nil
	}
	return &finding{
		Kind:        kindBinaryHashMismatch,
		Fingerprint: binaryHashMismatchFingerprint,
		Summary:     fmt.Sprintf("handler binary hash changed unexpectedly (%s -> %s)", previousHash, currentHash),
		Evidence:    fmt.Sprintf("previous_hash=%s\ncurrent_hash=%s", previousHash, currentHash),
		State:       currentHash,
	}
}

// hashFile computes the sha256 hex digest of path's contents, streaming
// rather than reading the whole file into memory (the target is a real
// binary, potentially tens of MB).
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
