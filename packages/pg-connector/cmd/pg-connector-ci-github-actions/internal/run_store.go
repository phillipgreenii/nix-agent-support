// run_store.go: this backend's own fresh, backend-local persistent store,
// keyed by CI run ID, recording which repo owns that run. GetLogs needs the
// repo to pass gh's own `run view <id> --log --repo <repo>` (`gh` has no
// repo-agnostic "look up a run by id" call — every run endpoint GitHub's
// REST API exposes is scoped under /repos/{owner}/{repo}/…), but
// ci.Provider.GetLogs is deliberately kept id-only (no repo/prID parameter)
// [operator ruling, Phillip, 2026-09-06, on pg2-f327j: GetLogs must resolve
// repo internally rather than widen its signature]. ListRuns/
// listRunsByBranch (provider.go) already know the repo for every run they
// return — via PRResolver — so they populate this store as a side effect;
// GetLogs then looks the run up here before calling gh.
//
// The scriptout wire protocol execs a NEW PROCESS per call ("one request,
// one response, one process per call" — pkg/scriptout's own doc comment), so
// an in-memory-only store would lose every write the instant that process
// exits. This store persists to a small JSON file on disk instead, written
// via a temp-file-and-rename so a crash mid-write cannot corrupt the copy a
// later read sees — the same shape as the sibling pg-connector-pr-github
// backend's own store.go, adapted to this backend's single run_id->repo
// mapping (no nested per-PR fields, since a run belongs to exactly one
// repo).
//
// Freedom-boundary decision, recorded per the operator ruling above: no TTL
// or eviction policy. A run ID never repeats (GitHub's databaseId is
// globally unique), so a stale entry is merely inert, never wrong — it is
// safe to let the file grow unbounded rather than build a pruning policy
// this bead's contract does not ask for. A missing-mapping lookup is not
// treated as this store's failure; it produces a clear, actionable
// not_found-shaped error from GetLogs itself (provider.go) rather than a
// silent or confusing one.
package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RunStore is this backend's persistent run_id->repo mapping, one file per
// invoking user (DefaultRunStorePath honours XDG_STATE_HOME, mirroring the
// sibling pg-connector-pr-github backend's own DefaultStorePath
// convention).
type RunStore struct {
	mu   sync.Mutex
	path string
}

// runStoreFile is the on-disk JSON shape: one repo string per run ID.
type runStoreFile struct {
	Runs map[string]string `json:"runs"`
}

// DefaultRunStorePath returns the canonical store file path for this
// backend, honouring XDG_STATE_HOME; fallback:
// ~/.local/state/pg-connector-ci-github-actions/run-repo.json.
func DefaultRunStorePath() string {
	const name = "pg-connector-ci-github-actions"
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, name, "run-repo.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", name, "run-repo.json")
}

// NewRunStore returns a RunStore backed by the file at path. The file need
// not exist yet — it is created (along with its parent directory) on first
// write.
func NewRunStore(path string) *RunStore {
	return &RunStore{path: path}
}

func (s *RunStore) load() (runStoreFile, error) {
	var data runStoreFile
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			data.Runs = map[string]string{}
			return data, nil
		}
		return data, fmt.Errorf("run store: read %s: %w", s.path, err)
	}
	if len(raw) == 0 {
		data.Runs = map[string]string{}
		return data, nil
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, fmt.Errorf("run store: parse %s: %w", s.path, err)
	}
	if data.Runs == nil {
		data.Runs = map[string]string{}
	}
	return data, nil
}

// save atomically writes data to s.path via a temp-file-and-rename in the
// same directory (so the rename is same-filesystem and therefore atomic).
func (s *RunStore) save(data runStoreFile) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("run store: mkdir %s: %w", dir, err)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("run store: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".run-store-*.json.tmp")
	if err != nil {
		return fmt.Errorf("run store: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename below succeeds
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("run store: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("run store: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("run store: rename into place: %w", err)
	}
	return nil
}

// SetRepo records runID's owning repo, overwriting any prior mapping (a run
// belongs to exactly one repo for its whole lifetime, so overwrite and
// first-write are indistinguishable and neither needs special-casing).
func (s *RunStore) SetRepo(runID, repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return err
	}
	data.Runs[runID] = repo
	return s.save(data)
}

// GetRepo returns runID's owning repo. ok is false when this backend has
// never recorded runID before — e.g. GetLogs called for a run whose PR was
// never listed via ListRuns/"ci list" in this environment — which the
// caller (GetLogs, provider.go) turns into its own clear, actionable
// not_found-shaped error rather than a silent or confusing failure.
func (s *RunStore) GetRepo(runID string) (repo string, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return "", false, err
	}
	repo, ok = data.Runs[runID]
	return repo, ok, nil
}
