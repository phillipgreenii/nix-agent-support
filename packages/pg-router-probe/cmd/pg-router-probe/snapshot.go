// snapshot.go: the persisted last-run snapshot checks 2 (queue/backlog
// drift) and 3 (binary hash sanity) compare their current reading
// against [design: "pg-router-probe run checks" items 2/3].
//
// Snapshot robustness [design: "Snapshot robustness" paragraph]: a
// missing, corrupted, or version-mismatched last-run snapshot are all
// treated IDENTICALLY by loadSnapshot below — logged by the caller, then
// treated as "no prior baseline" (this run is quiet on drift checks by
// construction, since there is nothing to diff against) — and
// overwritten with a fresh, version-stamped snapshot at the end of a
// successful run (run.go).
//
// File format/location are this packet's own implementation choice (no
// design citation): a small JSON file, defaulting under
// $HOME/.local/state/pg-router-probe/snapshot.json, overridable via
// --snapshot-path so a deployment (the out-of-scope sibling scheduling
// packet) can point every invocation at the same persistent path.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// snapshotVersion is bumped whenever this file's own field shape changes,
// so a snapshot written by an older/newer binary is recognized as
// version-mismatched rather than partially, incorrectly decoded.
const snapshotVersion = 1

// snapshot is the on-disk shape. QueueDepth/Backlog/BinaryHash are each
// independently optional in practice (a run invoked with only some of
// --queue-depth/--backlog/--binary-path set still needs to preserve the
// OTHER fields' last known values across runs — see run.go's own
// merge-not-clobber save logic), so zero values here are not
// distinguished from "genuinely observed zero/empty" -- run.go's own
// haveX bookkeeping is what tracks "was this sub-check configured this
// run", not this struct.
type snapshot struct {
	Version    int    `json:"version"`
	QueueDepth int    `json:"queue_depth"`
	Backlog    int    `json:"backlog"`
	BinaryHash string `json:"binary_hash"`
	// BinaryPath is the symlink-resolved path BinaryHash was computed from
	// (pg2-1jkai). Additive and omitempty: snapshotVersion is NOT bumped,
	// because a bump would discard the whole baseline; a snapshot without
	// it decodes to "" = unknown path (see checkBinaryHash).
	BinaryPath string `json:"binary_path,omitempty"`
	CheckedAt  string `json:"checked_at"`
	// Alerts is the per-Grafana-fingerprint episode state (pg2-3tt2e).
	// Additive and omitempty, like BinaryPath: snapshotVersion is NOT
	// bumped, because a bump would discard the whole baseline; a snapshot
	// without it decodes to a nil map = no alert has been observed yet.
	Alerts map[string]alertState `json:"alerts,omitempty"`
}

// alertEpisodeWindow is how far back episode start times are kept and
// counted ("episodes in last 7d").
const alertEpisodeWindow = 7 * 24 * time.Hour

// alertState is what this probe remembers about one Grafana alert
// fingerprint between ticks. It lives here, in the probe's own snapshot,
// rather than in bead metadata: pg-connector's metadata write bumps the
// bead's updated_at (see dedup.go's re-surface note), and the data is the
// probe's own bookkeeping, not tracker state.
type alertState struct {
	// LastStartsAt is the startsAt of the episode most recently recorded
	// (noted on a bead or seeded). A different startsAt on the next tick
	// is a new episode.
	LastStartsAt time.Time `json:"last_starts_at,omitzero"`
	// Episodes are the recorded episode start times within
	// alertEpisodeWindow (the current episode, LastStartsAt, is always
	// retained).
	Episodes []time.Time `json:"episodes,omitempty"`
	// LastNoted is when this probe last wrote about the fingerprint
	// (bead create, comment, or first-sight seed).
	LastNoted time.Time `json:"last_noted,omitzero"`
}

// pruneAlertStates returns a copy of states with episodes older than
// alertEpisodeWindow dropped (except each state's LastStartsAt) and
// entries with nothing left inside the window removed. It never mutates
// its input.
func pruneAlertStates(states map[string]alertState, now time.Time) map[string]alertState {
	if len(states) == 0 {
		return nil
	}
	cutoff := now.Add(-alertEpisodeWindow)
	out := make(map[string]alertState, len(states))
	for fp, st := range states {
		kept := make([]time.Time, 0, len(st.Episodes))
		inWindow := 0
		for _, e := range st.Episodes {
			if !e.Before(cutoff) {
				inWindow++
				kept = append(kept, e)
			} else if e.Equal(st.LastStartsAt) {
				kept = append(kept, e)
			}
		}
		if inWindow == 0 && st.LastNoted.Before(cutoff) {
			continue
		}
		st.Episodes = kept
		out[fp] = st
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// loadSnapshot returns (zero value, false) for EVERY failure mode the
// robustness rule above treats identically: the file does not exist, is
// unreadable, is not valid JSON, or was written by a different
// snapshotVersion. Only a successful decode AT the current version
// returns (snapshot, true).
func loadSnapshot(path string) (snapshot, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return snapshot{}, false
	}
	var s snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return snapshot{}, false
	}
	if s.Version != snapshotVersion {
		return snapshot{}, false
	}
	return s, true
}

// saveSnapshot always stamps the current snapshotVersion, regardless of
// what (if anything) the caller populated s.Version with. It creates the
// parent directory (0o700) when missing: the default location,
// $HOME/.local/state/pg-router-probe, does not exist on a fresh host, and
// without this every write failed (pg2-3gqtw), leaving every run without a
// baseline and the queue-growth check permanently inert.
func saveSnapshot(path string, s snapshot) error {
	s.Version = snapshotVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
