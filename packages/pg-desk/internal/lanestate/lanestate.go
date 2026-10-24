package lanestate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// RunEvent is one counted event of a run: the stage it happened in and its
// class. A clean run has no event.
type RunEvent struct {
	Stage string `json:"stage"`
	Class string `json:"class"`
}

// DecisionCount adds Count (1 when 0) to one decisions-counter series.
type DecisionCount struct {
	Rule    string `json:"rule"`
	Action  string `json:"action"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	Count   int64  `json:"count,omitempty"`
}

// RunInput is what one run of the composed command reports.
type RunInput struct {
	// PR is the normalized PR id (the canonical OWNER/REPO#N the store keys
	// the entity under).
	PR string `json:"pr"`
	// Mode is the mode the run executed in: off, plan or apply.
	Mode string `json:"mode"`
	// Events are the run's counted events (failures and degraded outcomes).
	Events []RunEvent `json:"events,omitempty"`
	// Residual is the number of actions a read-only plan still lists for the PR
	// after the run; nil means the run did not learn it (the PR's residual is
	// left as it was).
	Residual *int `json:"residual,omitempty"`
	// LastOutcome is a short label for the run's outcome.
	LastOutcome string `json:"last_outcome,omitempty"`
	// Decisions are the decider's per-rule results of the run.
	Decisions []DecisionCount `json:"decisions,omitempty"`
	// LookupItems and LookupCeiling are the decider's work-item lookup item
	// count and its configured ceiling; nil leaves the stored value alone.
	LookupItems   *int64 `json:"lookup_items,omitempty"`
	LookupCeiling *int64 `json:"lookup_ceiling,omitempty"`
}

// PRState is the persisted state of one PR, written only by the recorded run
// for that PR (which holds the per-PR lock), so it needs no atomic form.
type PRState struct {
	Mode          string `json:"mode"`
	Residual      int    `json:"residual"`
	ResidualSince string `json:"residual_since,omitempty"`
	LastOutcome   string `json:"last_outcome,omitempty"`
	UpdatedAt     string `json:"updated_at"`
}

// Validate rejects an input that names a value outside the closed vocabularies,
// so nothing is written for it and series cardinality stays bounded.
func (in RunInput) Validate() error {
	if strings.TrimSpace(in.PR) == "" {
		return fmt.Errorf("lanestate: run input has no pr")
	}
	if !contains(Modes, in.Mode) {
		return fmt.Errorf("lanestate: mode %q is not one of %v", in.Mode, Modes)
	}
	for _, e := range in.Events {
		if !contains(Stages, e.Stage) {
			return fmt.Errorf("lanestate: stage %q is not one of %v", e.Stage, Stages)
		}
		if !contains(Classes, e.Class) {
			return fmt.Errorf("lanestate: class %q is not one of %v", e.Class, Classes)
		}
	}
	for _, d := range in.Decisions {
		switch {
		case !contains(Rules, d.Rule):
			return fmt.Errorf("lanestate: decision rule %q is not one of %v", d.Rule, Rules)
		case !contains(Actions, d.Action):
			return fmt.Errorf("lanestate: decision action %q is not one of %v", d.Action, Actions)
		case !contains(Outcomes, d.Outcome):
			return fmt.Errorf("lanestate: decision outcome %q is not one of %v", d.Outcome, Outcomes)
		case d.Reason != "" && !contains(Reasons, d.Reason):
			return fmt.Errorf("lanestate: decision reason %q is not one of %v", d.Reason, Reasons)
		case d.Reason != "" && d.Action != ActionSkip:
			return fmt.Errorf("lanestate: decision reason %q on action %q: only a skip carries a reason", d.Reason, d.Action)
		case d.Count < 0:
			return fmt.Errorf("lanestate: decision count %d is negative", d.Count)
		}
	}
	if in.Residual != nil && *in.Residual < 0 {
		return fmt.Errorf("lanestate: residual %d is negative", *in.Residual)
	}
	return nil
}

func decisionKey(d Decision) string {
	return KeyDecisionsPrefix + d.Rule + "|" + d.Action + "|" + d.Outcome + "|" + d.Reason
}

func runKey(stage, class string) string { return KeyRunsPrefix + stage + "." + class }

// Record persists one run. configuredMode is the configured pr_lane.mode (the
// value the mode gauge reads). Counters, the decider's lookup figures and the
// last-run time are written for every run. The lane-level mode, the flip time
// and the PR's own state are written ONLY when the run's mode equals
// configuredMode: a run in another mode (a by-hand plan run after the flip)
// leaves them, and so the PR's persisted apply-mode residual, untouched. The
// read-only residual plan an apply run makes afterwards is not a recorded run;
// it is reported as the Residual of the apply run.
func Record(st *store.Store, configuredMode string, now time.Time, in RunInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	stamp := now.UTC().Format(time.RFC3339)

	for _, e := range in.Events {
		if err := st.IncrMeta(runKey(e.Stage, e.Class), 1); err != nil {
			return err
		}
	}
	for _, d := range in.Decisions {
		n := d.Count
		if n == 0 {
			n = 1
		}
		if err := st.IncrMeta(decisionKey(Decision{d.Rule, d.Action, d.Outcome, d.Reason}), n); err != nil {
			return err
		}
	}
	if in.LookupItems != nil {
		if err := st.SetMeta(KeyStateLookupItem, strconv.FormatInt(*in.LookupItems, 10)); err != nil {
			return err
		}
	}
	if in.LookupCeiling != nil {
		if err := st.SetMeta(KeyStateLookupCeil, strconv.FormatInt(*in.LookupCeiling, 10)); err != nil {
			return err
		}
	}

	if in.Mode == configuredMode {
		if err := recordLaneLevel(st, in.Mode, stamp); err != nil {
			return err
		}
		if err := recordPR(st, in, stamp); err != nil {
			return err
		}
	}
	return st.SetMeta(KeyStateLastRunAt, stamp)
}

// recordLaneLevel writes the lane-level mode and flip time for a run whose mode
// is the configured one. The flip time is set once: only by the first run in
// apply that finds the persisted lane mode not yet apply, through an insert
// that exactly one of several concurrent first runs wins. It is cleared when a
// recorded run moves the lane out of apply, so a later flip starts a new one.
func recordLaneLevel(st *store.Store, mode, stamp string) error {
	prev, found, err := st.GetMeta(KeyStateMode)
	if err != nil {
		return err
	}
	if !found || !contains(Modes, prev) {
		prev = config.PRLaneModeOff
	}
	switch {
	case mode == config.PRLaneModeApply && prev != config.PRLaneModeApply:
		if _, err := st.SetMetaIfAbsent(KeyStateFlipAt, stamp); err != nil {
			return err
		}
	case mode != config.PRLaneModeApply && prev == config.PRLaneModeApply:
		if err := st.DeleteMeta(KeyStateFlipAt); err != nil {
			return err
		}
	}
	return st.SetMeta(KeyStateMode, mode)
}

func recordPR(st *store.Store, in RunInput, stamp string) error {
	key := KeyPRPrefix + in.PR
	var prev PRState
	hadPrev := false
	if raw, found, err := st.GetMeta(key); err != nil {
		return err
	} else if found {
		hadPrev = json.Unmarshal([]byte(raw), &prev) == nil
	}
	next := PRState{Mode: in.Mode, LastOutcome: in.LastOutcome, UpdatedAt: stamp}
	switch {
	case in.Residual != nil:
		next.Residual = *in.Residual
		if next.Residual > 0 {
			// The first-seen time survives while the residual stays non-empty
			// in the same mode; a new mode starts its own clock.
			if hadPrev && prev.Residual > 0 && prev.Mode == in.Mode && prev.ResidualSince != "" {
				next.ResidualSince = prev.ResidualSince
			} else {
				next.ResidualSince = stamp
			}
		}
	case hadPrev && prev.Mode == in.Mode:
		// The run did not learn the residual: keep what this mode recorded.
		next.Residual, next.ResidualSince = prev.Residual, prev.ResidualSince
	}
	b, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("lanestate: encode pr state: %w", err)
	}
	return st.SetMeta(key, string(b))
}

// MarkInactive marks the PR inactive and counts it, in a single transaction.
// It is a store write, not a tracker write, so a run in either mode may call
// it. It reports whether the entity changed (an absent or already inactive PR
// changes nothing and counts nothing).
func MarkInactive(st *store.Store, repo, id string) (bool, error) {
	return st.MarkEntityInactive(repo, "pr", id, KeyMarkedInactive)
}

// RunKey identifies one lane-run counter series.
type RunKey struct{ Stage, Class string }

// Snapshot is the persisted lane state at one scrape.
type Snapshot struct {
	Runs           map[RunKey]int64
	Decisions      map[Decision]int64
	MarkedInactive int64
	// LaneMode is the lane-level mode, off when none has been recorded.
	LaneMode string
	// FlipAt and LastRunAt are RFC 3339 times, or "" when unset.
	FlipAt, LastRunAt string
	// LookupItems and LookupCeiling are the decider's last reported lookup
	// figures, 0 before any report.
	LookupItems, LookupCeiling int64
	// ResidualPRs and ResidualOldestAgeSeconds describe the PRs whose
	// persisted state is in the lane-level mode and has a non-empty residual
	// (a residual left by another mode is not counted).
	ResidualPRs, ResidualOldestAgeSeconds int
}

// Read reads the whole lane state with one prefix scan and aggregates it as of
// now. A corrupt value reads as zero (or is skipped, for a PR record) rather
// than failing the scrape.
func Read(st *store.Store, now time.Time) (Snapshot, error) {
	rows, err := st.ListMetaPrefix(KeyPrefix)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		Runs:      map[RunKey]int64{},
		Decisions: map[Decision]int64{},
		LaneMode:  config.PRLaneModeOff,
		FlipAt:    rows[KeyStateFlipAt],
		LastRunAt: rows[KeyStateLastRunAt],
	}
	if m := rows[KeyStateMode]; contains(Modes, m) {
		snap.LaneMode = m
	}
	snap.MarkedInactive = atoi(rows[KeyMarkedInactive])
	snap.LookupItems = atoi(rows[KeyStateLookupItem])
	snap.LookupCeiling = atoi(rows[KeyStateLookupCeil])

	var prs []PRState
	for k, v := range rows {
		switch {
		case strings.HasPrefix(k, KeyRunsPrefix):
			rest := strings.TrimPrefix(k, KeyRunsPrefix)
			stage, class, ok := strings.Cut(rest, ".")
			if ok {
				snap.Runs[RunKey{stage, class}] = atoi(v)
			}
		case strings.HasPrefix(k, KeyDecisionsPrefix):
			parts := strings.Split(strings.TrimPrefix(k, KeyDecisionsPrefix), "|")
			if len(parts) == 4 {
				snap.Decisions[Decision{parts[0], parts[1], parts[2], parts[3]}] = atoi(v)
			}
		case strings.HasPrefix(k, KeyPRPrefix):
			var ps PRState
			if json.Unmarshal([]byte(v), &ps) == nil {
				prs = append(prs, ps)
			}
		}
	}
	for _, ps := range prs {
		if ps.Mode != snap.LaneMode || ps.Residual <= 0 {
			continue
		}
		snap.ResidualPRs++
		if t, err := time.Parse(time.RFC3339, ps.ResidualSince); err == nil {
			if age := int(now.Sub(t).Seconds()); age > snap.ResidualOldestAgeSeconds {
				snap.ResidualOldestAgeSeconds = age
			}
		}
	}
	return snap, nil
}

func atoi(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
