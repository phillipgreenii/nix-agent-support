// Package cache persists Tier 2 (classify) verdicts keyed by candidate id,
// classifier identity and prompt version (bead pg2-ohvpk).
//
// # Why this exists
//
// A `pg-ccaudit report --classifier cli` pass makes one paid model call per
// batch and can run for many minutes over a real corpus. Before this package,
// a killed pass had NOTHING to show for whatever it already paid for: the
// verdicts lived only in memory until the whole pass returned. This cache is
// what makes a completed batch's work durable the moment it completes, so a
// later `classify status` over the same window can report those candidates as
// already answered instead of pending, and a later `classify`/`report` run
// does not pay to ask the model the same question twice.
//
// # Why the key includes the classifier name and prompt version
//
// A cached verdict is only valid for the classifier and rubric that produced
// it. `classify.PromptVersion` is bumped whenever the rubric's MEANING
// changes, and a verdict cached under an old prompt version answering a
// different question would silently contaminate a run made under a new one.
// Scoping the key by classifier name additionally means the baseline rule
// (which does not use this package — see the callers in cmd/pg-ccaudit) can
// never collide with the semantic CLI classifier's entries even if it were
// ever wired in.
//
// # Why the file is append-only
//
// Load keeps the LAST entry it reads for a given key, so appending a fresh
// verdict for an already-cached id is a correct update without ever
// rewriting or truncating the file. That is what lets Append be called once
// per completed batch, mid-run, with no read-modify-write race against a
// concurrent reader: a reader only ever sees a prefix of true, complete
// lines, and a writer that dies mid-line leaves at most one trailing partial
// line for the next Load to skip (see the truncation guard below).
package cache

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvPath overrides the cache location.
const EnvPath = "PG_CCAUDIT_CLASSIFY_CACHE"

// Entry is one cached verdict.
type Entry struct {
	ID            string `json:"id"`
	Classifier    string `json:"classifier"`
	PromptVersion int    `json:"prompt_version"`
	Class         string `json:"class"`
	Confidence    string `json:"confidence"`
	What          string `json:"what"`
	Prevention    string `json:"prevention"`
	RouteHint     string `json:"route_hint"`
	// RunID identifies which classification pass produced this entry, so a
	// cache hit can be traced back to the cost-ledger run that paid for it.
	RunID        string    `json:"run_id"`
	ClassifiedAt time.Time `json:"classified_at"`
}

// Key identifies one cache slot. Two runs of the SAME classifier at the SAME
// prompt version answering the SAME candidate id share a slot; anything else
// does not.
type Key struct {
	ID            string
	Classifier    string
	PromptVersion int
}

// Key is this entry's cache slot.
func (e Entry) Key() Key {
	return Key{ID: e.ID, Classifier: e.Classifier, PromptVersion: e.PromptVersion}
}

// DefaultPath resolves the cache location, beside the index by default —
// the same convention gold.DefaultPath uses, and for the same reason: this
// data is derived from a corpus that lives outside the repository.
func DefaultPath() (string, error) {
	if p := os.Getenv(EnvPath); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "pg-ccaudit", "classify-cache.jsonl"), nil
}

// Load reads every cached entry into a map keyed by Key. A malformed LAST
// line is tolerated as a truncated write-in-progress (or the tail of a
// process killed mid-write) and dropped rather than failing the whole load;
// a malformed line that is NOT last is a genuine corruption and fails loudly,
// because tolerating it silently would make every "was this cached" answer
// downstream unprovable.
//
// The caller MUST check os.IsNotExist on the returned error: an absent cache
// is the normal state before anything has ever been classified, exactly as
// gold.Load's callers already check for the gold set.
func Load(path string) (map[Key]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open classify cache %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out := map[Key]Entry{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read classify cache %s: %w", path, err)
	}
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			if i == len(lines)-1 {
				// The tail of a write that was interrupted mid-line — exactly
				// the case this cache exists to survive. Everything before it
				// is still a complete, valid entry.
				break
			}
			return nil, fmt.Errorf("classify cache %s line %d: %w", path, i+1, err)
		}
		out[e.Key()] = e
	}
	return out, nil
}

// Append persists new entries durably: each call opens, writes and closes
// the file, so a write that returns nil has already reached the OS — a
// process killed immediately afterward (SIGTERM, SIGKILL) cannot take it
// back. This is what lets a caller call Append once per completed batch and
// trust that batch is safe before starting the next one.
func Append(path string, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create classify cache directory %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open classify cache %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("encode cache entry %s: %w", e.ID, err)
		}
		if _, err := w.Write(b); err != nil {
			return fmt.Errorf("write classify cache %s: %w", path, err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return fmt.Errorf("write classify cache %s: %w", path, err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush classify cache %s: %w", path, err)
	}
	return f.Sync()
}
