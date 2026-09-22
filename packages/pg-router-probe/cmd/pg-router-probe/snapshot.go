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
	CheckedAt  string `json:"checked_at"`
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
// what (if anything) the caller populated s.Version with.
func saveSnapshot(path string, s snapshot) error {
	s.Version = snapshotVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
