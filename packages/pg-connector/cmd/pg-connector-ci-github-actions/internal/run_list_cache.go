// run_list_cache.go: this backend's own local, persistent cache of
// ListRuns' own last-known-good result per PR id — what lets
// ci.Provider.ListRuns (pkg/provider/ci/iface.go) carry the same AsOf/Stale
// contract pkg/provider/pr.Provider.Show already established (bead
// pg2-681xo), extended to the ci capability by bead pg2-4aoeg. When a live
// `gh run list` call fails in a way that is not itself a definitive
// not_found/unauthenticated answer — i.e. GitHub Actions is merely
// degraded/unreachable right now, the "everything else" bucket
// provider.go's own classifyGHError leaves for scriptout's codeForError
// fallback to classify as unavailable — ListRuns (provider.go) consults
// this cache and, if it holds a prior successful result for the same PR,
// serves that instead of erroring outright, with every returned
// schema.CIRun's own Stale forced true and AsOf rewritten to the CACHED
// read's own original as-of time (never "now" — "now" is the moment of
// this failed, not successful, attempt).
//
// Persisted the same way run_store.go's RunStore already is (a small JSON
// file, written via temp-file-and-rename, honouring XDG_STATE_HOME) since
// the scriptout wire protocol execs a new process per call ("one request,
// one response, one process per call" — pkg/scriptout's own doc comment) —
// an in-memory-only cache would lose every write the instant that process
// exits.
//
// Freedom-boundary decision, mirroring run_store.go's own: no TTL or
// eviction policy. A stale-but-present entry is exactly what this cache
// exists to serve; there is no notion of an entry becoming too old to
// serve, since the CALLER (a human or tool reading schema.CIRun.AsOf/Stale)
// is the one who decides whether a given staleness is acceptable, not this
// backend.
package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// RunListCache is this backend's persistent prID -> last-known-good
// ListRuns result cache, one file per invoking user
// (DefaultRunListCachePath honours XDG_STATE_HOME, mirroring run_store.go's
// own DefaultRunStorePath convention).
type RunListCache struct {
	mu   sync.Mutex
	path string
}

// cachedRunList is one PR's last-known-good ListRuns result: the runs
// themselves (as returned by the live gh call that produced them, each
// still carrying its own original Stale=false/AsOf) plus the wall-clock
// time this backend fetched them — Get re-stamps that AsOf onto every
// returned run on a later stale-serve, rather than the moment of the
// failed attempt that triggered the fallback (see provider.go's
// staleFallback).
type cachedRunList struct {
	Runs []schema.CIRun `json:"runs"`
	AsOf time.Time      `json:"as_of"`
}

// runListCacheFile is the on-disk JSON shape: one cachedRunList per PR id.
type runListCacheFile struct {
	ByPRID map[string]cachedRunList `json:"by_pr_id"`
}

// DefaultRunListCachePath returns the canonical cache file path for this
// backend, honouring XDG_STATE_HOME; fallback:
// ~/.local/state/pg-connector-ci-github-actions/run-list-cache.json.
func DefaultRunListCachePath() string {
	const name = "pg-connector-ci-github-actions"
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, name, "run-list-cache.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", name, "run-list-cache.json")
}

// NewRunListCache returns a RunListCache backed by the file at path. The
// file need not exist yet — it is created (along with its parent
// directory) on first write.
func NewRunListCache(path string) *RunListCache {
	return &RunListCache{path: path}
}

func (c *RunListCache) load() (runListCacheFile, error) {
	var data runListCacheFile
	raw, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			data.ByPRID = map[string]cachedRunList{}
			return data, nil
		}
		return data, fmt.Errorf("run list cache: read %s: %w", c.path, err)
	}
	if len(raw) == 0 {
		data.ByPRID = map[string]cachedRunList{}
		return data, nil
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, fmt.Errorf("run list cache: parse %s: %w", c.path, err)
	}
	if data.ByPRID == nil {
		data.ByPRID = map[string]cachedRunList{}
	}
	return data, nil
}

// save atomically writes data to c.path via a temp-file-and-rename in the
// same directory (so the rename is same-filesystem and therefore atomic) —
// the same shape run_store.go's own save uses.
func (c *RunListCache) save(data runListCacheFile) error {
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("run list cache: mkdir %s: %w", dir, err)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("run list cache: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".run-list-cache-*.json.tmp")
	if err != nil {
		return fmt.Errorf("run list cache: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename below succeeds
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("run list cache: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("run list cache: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, c.path); err != nil {
		return fmt.Errorf("run list cache: rename into place: %w", err)
	}
	return nil
}

// Set records prID's last-known-good ListRuns result (runs, as returned by
// a live gh call) as of asOf, overwriting any prior entry for prID.
func (c *RunListCache) Set(prID string, runs []schema.CIRun, asOf time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := c.load()
	if err != nil {
		return err
	}
	// Store a defensive copy so a later caller mutating its own slice
	// cannot corrupt the cached copy.
	stored := make([]schema.CIRun, len(runs))
	copy(stored, runs)
	data.ByPRID[prID] = cachedRunList{Runs: stored, AsOf: asOf}
	return c.save(data)
}

// Get returns prID's last cached ListRuns result. ok is false when this
// backend has never cached a successful ListRuns result for prID before —
// e.g. GitHub Actions has been unreachable since this environment's very
// first ListRuns call for this PR — which the caller (ListRuns, provider.go)
// turns into a plain propagated error rather than a fabricated stale
// answer with nothing to actually serve.
func (c *RunListCache) Get(prID string) (runs []schema.CIRun, asOf time.Time, ok bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := c.load()
	if err != nil {
		return nil, time.Time{}, false, err
	}
	entry, found := data.ByPRID[prID]
	if !found {
		return nil, time.Time{}, false, nil
	}
	out := make([]schema.CIRun, len(entry.Runs))
	copy(out, entry.Runs)
	return out, entry.AsOf, true, nil
}
