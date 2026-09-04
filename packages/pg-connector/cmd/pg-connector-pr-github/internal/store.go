// store.go: pg-connector-pr-github's fresh, backend-local persistent store,
// keyed by PR id and (nested) by comment/review-thread id, backing the
// categorize and feedback_set writes [design: §6.1, §8, §9]. It is NOT a
// port of pg-pr's SQLite internal/store.Feedback table — that table is
// FK-required against pg-pr's own pull_request table, a dependency this
// backend's self-contained module forbids (§9, §5.2) — so this is a fresh
// design with no pg-pr precedent to carry over.
//
// The scriptout wire protocol execs a NEW PROCESS per call ("one request,
// one response, one process per call" — pkg/scriptout's own doc comment), so
// an in-memory-only store would lose every write the instant that process
// exits. This store persists to a small JSON file on disk instead, written
// via a temp-file-and-rename so a crash mid-write cannot corrupt the copy a
// later read sees.
package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Store is the backend's persistent state, one file per invoking user
// (DefaultStorePath honours XDG_STATE_HOME, mirroring pg-pr's own
// internal/store.DefaultPath convention).
type Store struct {
	mu   sync.Mutex
	path string
}

// prState is one PR's persisted write-side state.
type prState struct {
	Category     string                        `json:"category,omitempty"`
	Dispositions map[string]schema.Disposition `json:"dispositions,omitempty"`
}

// storeFile is the on-disk JSON shape: one prState per PR id.
type storeFile struct {
	PRs map[string]prState `json:"prs"`
}

// DefaultStorePath returns the canonical store file path for this backend,
// honouring XDG_STATE_HOME; fallback: ~/.local/state/pg-connector-pr-github/store.json.
func DefaultStorePath() string {
	const name = "pg-connector-pr-github"
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, name, "store.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", name, "store.json")
}

// NewStore returns a Store backed by the file at path. The file need not
// exist yet — it is created (along with its parent directory) on first
// write.
func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) load() (storeFile, error) {
	var data storeFile
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			data.PRs = map[string]prState{}
			return data, nil
		}
		return data, fmt.Errorf("store: read %s: %w", s.path, err)
	}
	if len(raw) == 0 {
		data.PRs = map[string]prState{}
		return data, nil
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, fmt.Errorf("store: parse %s: %w", s.path, err)
	}
	if data.PRs == nil {
		data.PRs = map[string]prState{}
	}
	return data, nil
}

// save atomically writes data to s.path via a temp-file-and-rename in the
// same directory (so the rename is same-filesystem and therefore atomic).
func (s *Store) save(data storeFile) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store: mkdir %s: %w", dir, err)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".store-*.json.tmp")
	if err != nil {
		return fmt.Errorf("store: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("store: rename into place: %w", err)
	}
	return nil
}

// SetCategory sets prID's category — a plain set/overwrite into a dedicated
// field, never a GitHub label [design: §6.1].
func (s *Store) SetCategory(prID, category string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return err
	}
	st := data.PRs[prID]
	st.Category = category
	data.PRs[prID] = st
	return s.save(data)
}

// SetDisposition sets prID's commentID disposition.
func (s *Store) SetDisposition(prID, commentID string, disposition schema.Disposition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return err
	}
	st := data.PRs[prID]
	if st.Dispositions == nil {
		st.Dispositions = map[string]schema.Disposition{}
	}
	st.Dispositions[commentID] = disposition
	data.PRs[prID] = st
	return s.save(data)
}

// PRState is a read-only snapshot of prID's persisted state, used by Show
// to merge the current category/dispositions into the freshly-fetched
// PR-schema response [design: §2, §6.1]. A PR with no persisted writes yet
// returns a zero-value PRState (empty category, nil Dispositions) rather
// than an error — "never written yet" is not a failure.
type PRState struct {
	Category     string
	Dispositions map[string]schema.Disposition
}

// Get returns prID's current persisted state.
func (s *Store) Get(prID string) (PRState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return PRState{}, err
	}
	st := data.PRs[prID]
	return PRState{Category: st.Category, Dispositions: st.Dispositions}, nil
}
