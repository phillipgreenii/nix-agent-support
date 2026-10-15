// checks.go: the two deterministic health checks run performs, each
// independently testable via fixtures [design: "pg-router-probe run
// checks"]:
//
//  1. checkGrafanaAlerts — Grafana currently-firing alerts filtered to the
//     registered rule UIDs (item 1).
//  2. checkBinaryHash — daemon/handler binary hash sanity: an unexpected
//     change with no corresponding deploy record (item 3).
//
// The design's item 2, queue/backlog drift against the persisted last-run
// snapshot, was deleted by operator ruling (pg2-po4nj option A, applied by
// pg2-7729r): it fired 15 or more times in 2026-10-06..14 and every
// occurrence self-cleared by the next reading. A backlog that is actually
// stuck stays covered by the Grafana pg-router-queue-stalled and
// pg-router-queue-slow-growth rules, which this probe forwards. The probe
// still reads and persists the backlog (run.go, snapshot.go) but never
// escalates on it.
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
	"path/filepath"
	"strings"
	"time"
)

// findingKind identifies which of the checks produced a finding —
// also the wire value each finding's rendered body implicitly carries via
// its own Kind-specific Summary/Evidence text.
type findingKind string

const (
	kindGrafanaAlert       findingKind = "grafana-alert"
	kindBinaryHashMismatch findingKind = "binary-hash-mismatch"
)

// finding is one real, new-or-changed detection surfaced by a check.
// State carries the value dedup.go's "nothing new" comparison keys off,
// specific to Kind:
//   - kindGrafanaAlert: the alert's own current state (e.g. "active"); the
//     episode identity is StartsAt, not State (dedup.go).
//   - kindBinaryHashMismatch: the current binary hash.
type finding struct {
	Kind        findingKind
	Fingerprint string
	Aliases     []string // non-empty only for kindGrafanaAlert
	Summary     string
	Evidence    string
	State       string
	// StartsAt/Values are kindGrafanaAlert only (zero/"" otherwise): the
	// alert instance's startsAt (identifies the episode) and its rendered
	// __values__ annotation (the "current value"). They are NOT part of
	// the fingerprint (pg2-3tt2e).
	StartsAt time.Time
	Values   string
	// Escalation is the alert's "escalation" label value (kindGrafanaAlert
	// only; escalationHuman files the bead straight to the operator), and
	// AlertSummary/AlertDescription its static annotations, rendered under
	// the body's Remediation: section for escalationHuman beads
	// (pg2-x7ie2). None of the three is part of the fingerprint.
	Escalation       string
	AlertSummary     string
	AlertDescription string
}

// checkGrafanaAlerts builds one finding per currently-firing alert already
// filtered to the registered rule UIDs (the filtering itself happens in
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
			Aliases:  []string{fp},
			Summary:  fmt.Sprintf("Grafana alert %s is firing", a.RuleUID),
			Evidence: grafanaAlertEvidence(a),
			State:    a.State,
			StartsAt: a.StartsAt,
			Values:   a.Values,

			Escalation:       a.Labels[escalationLabelKey],
			AlertSummary:     a.AnnotationSummary,
			AlertDescription: a.AnnotationDescription,
		})
	}
	return findings
}

// nixStorePrefix is the immutable store root. A binary resolved into it is
// content-addressed by its store path: any change to its inputs yields a
// DIFFERENT store path, so a different path with a different hash is an
// ordinary deploy, not tampering (pg2-1jkai).
const nixStorePrefix = "/nix/store/"

// storeObject returns the top-level store object of a resolved path
// (e.g. "/nix/store/abc-name" for "/nix/store/abc-name/bin/x"), or ""
// when path is not under the nix store.
func storeObject(path string) string {
	rest, ok := strings.CutPrefix(path, nixStorePrefix)
	if !ok || rest == "" {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	if name == "" {
		return ""
	}
	return nixStorePrefix + name
}

// binaryIdentity is a binary's content hash plus the symlink-resolved path
// it was hashed from. Path is empty when unknown (a snapshot written
// before pg2-1jkai persisted only the hash).
type binaryIdentity struct {
	Hash string
	Path string
}

// checkBinaryHash compares the current binary against the previous
// snapshot's own stored identity. deployExpected is the caller's own
// "corresponding deploy record" verdict (deployrecord.go) — this function
// itself makes no I/O decision about what counts as expected, only what
// to do given that verdict [design: item 3].
//
// Store-path rule (pg2-1jkai, option b): when the CURRENT path is a
// nix-store path, a hash change is an expected update if the store object
// differs from the previous one (an apply replaced the handler), and a
// tamper signal only when the SAME store object now has a different hash
// (the store is immutable, so that cannot be a deploy). A non-store
// current path keeps the pre-existing behaviour: any hash change alerts
// unless the deploy record allows it.
//
// Unknown previous path (old snapshot with no path field): treated as an
// expected update when the current path is a store path, and otherwise
// falls back to the pre-existing behaviour. Rationale: the first tick
// after upgrading the probe frequently coincides with an apply that also
// replaced the handler, so alerting there would reproduce the very false
// positive this rule removes; and a tampered store path is not reachable
// to an unprivileged writer, so the one-tick blind spot is small. The
// snapshot is re-baselined with the path on that tick, so the strict
// same-store-path check applies from the next tick on.
func checkBinaryHash(hasPrevious bool, previous, current binaryIdentity, deployExpected bool) *finding {
	if !hasPrevious || previous.Hash == "" {
		return nil
	}
	if previous.Hash == current.Hash {
		return nil
	}
	if deployExpected {
		return nil
	}
	if curObj := storeObject(current.Path); curObj != "" && curObj != storeObject(previous.Path) {
		// Includes previous.Path == "" (unknown) and a previous non-store
		// path: either way the store object is new to us.
		return nil
	}
	return &finding{
		Kind:        kindBinaryHashMismatch,
		Fingerprint: binaryHashMismatchFingerprint,
		Summary:     fmt.Sprintf("handler binary hash changed unexpectedly (%s -> %s)", previous.Hash, current.Hash),
		Evidence:    fmt.Sprintf("previous_hash=%s\ncurrent_hash=%s\nprevious_path=%s\ncurrent_path=%s", previous.Hash, current.Hash, previous.Path, current.Path),
		State:       current.Hash,
	}
}

// resolveBinaryPath returns path with symlinks evaluated, falling back to
// the cleaned input when evaluation fails (the file was just hashed, so
// failure here is an unlikely race, not a reason to skip the check).
func resolveBinaryPath(path string) string {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	return filepath.Clean(path)
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
