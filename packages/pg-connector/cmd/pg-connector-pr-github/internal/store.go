// store.go: pg-connector-pr-github's fresh, backend-local persistent store,
// keyed by PR id and (nested) by comment/review-thread id, backing the
// categorize and feedback_set writes (interfaces.md's pr op catalog; entity_store_test.go). It is NOT a
// port of pg-pr's SQLite internal/store.Feedback table — that table is
// FK-required against pg-pr's own pull_request table, a dependency this
// backend's self-contained module forbids (layout_convention_test.go) — so
// this is a fresh design with no pg-pr precedent to carry over. The pg-pr
// feedback data that DOES need to reach this store crosses over via a
// one-shot import, not a shared table — see migrate.go and ADR 0063.
//
// The scriptout wire protocol execs a NEW PROCESS per call ("one request,
// one response, one process per call" — pkg/scriptout's own doc comment), so
// an in-memory-only store would lose every write the instant that process
// exits. This store persists to a small JSON file on disk instead, written
// via a temp-file-and-rename so a crash mid-write cannot corrupt the copy a
// later read sees, and the whole read-modify-write cycle is additionally
// guarded by a cross-process flock (see withLock) because "exec a new
// process per call" also means a new sync.Mutex per call — an in-process
// mutex alone protects nothing across the two (or more) OS processes that
// pr-pool's df-feedback role can dispatch concurrently for the same PR
// (finding A17).
package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Store is the backend's persistent state, one file per invoking user
// (DefaultStorePath honours XDG_STATE_HOME, mirroring pg-pr's own
// internal/store.DefaultPath convention).
//
// mu guards only same-process concurrent Store method calls (e.g. two
// goroutines sharing one *Store in a test); it is NOT the mechanism that
// makes concurrent writes safe — that is the cross-process flock withLock
// takes around every load-modify-save cycle, since the scriptout protocol
// gives every call its own process and therefore its own, otherwise
// unsynchronized, mu.
type Store struct {
	mu   sync.Mutex
	path string
}

// currentStoreVersion is this store file format's schema version, mirroring
// pg-pr's own internal/store/migrate.go schemaVersion precedent (finding
// A18) — just sized for a small JSON file rather than a SQL migration
// ladder. Bump it and append a step to storeMigrations whenever storeFile's
// shape changes in a way existing on-disk files need upgrading to read
// correctly under the new shape.
const currentStoreVersion = 1

// storeMigrations is the ordered list of in-place upgrades applied to reach
// currentStoreVersion. Index i upgrades a decoded storeFile from version i
// to version i+1 (mirroring pg-pr's migrate.go "index i migrates user_version
// i -> i+1" convention), so this slice's length MUST always equal
// currentStoreVersion — load's upgrade loop indexes it for every v in
// [data.Version, currentStoreVersion), and a pre-versioning on-disk file (no
// "version" key at all, decoding to the Go zero value 0) is version 0 just
// as much as an explicit `"version":0` would be.
//
// Index 0 upgrades version 0 (every store.json written before this bead's
// fix — no version field, but otherwise today's exact prs shape) to version
// 1 (the same shape, now with the version field stamped). No on-disk data
// needs reshaping to make that upgrade — only save() stamping
// currentStoreVersion does — so this step is a no-op function that exists
// purely so the loop below has an entry to call instead of indexing past
// the end of an empty slice. A future field-shape change appends the NEXT
// step here and bumps currentStoreVersion, exactly as pg-pr's own
// migrations slice grows.
var storeMigrations = []func(*storeFile) error{
	func(*storeFile) error { return nil }, // v0 -> v1: no shape change, only the version stamp is new.
}

// prState is one PR's persisted write-side state.
type prState struct {
	Category     string                        `json:"category,omitempty"`
	Dispositions map[string]schema.Disposition `json:"dispositions,omitempty"`
}

// storeFile is the on-disk JSON shape: one prState per PR id, plus the
// schema version that shape was written under (finding A18).
type storeFile struct {
	Version int                `json:"version"`
	PRs     map[string]prState `json:"prs"`
}

// DefaultStorePath returns the canonical store file path for this backend,
// honouring XDG_STATE_HOME; fallback: ~/.local/state/pg-connector-pr-github/store.json.
//
// A missing/unresolvable $HOME is reported, not silently swallowed (finding
// A18): the previous version of this function discarded os.UserHomeDir's
// error and fell back to a "" home, which filepath.Join quietly turned into
// a cwd-relative path (".local/state/...") — exactly wrong under launchd or
// a git hook, where the caller's cwd is not the operator's home and is often
// not even writable. Callers MUST check the returned error rather than
// treating a non-nil string as always usable.
func DefaultStorePath() (string, error) {
	const name = "pg-connector-pr-github"
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, name, "store.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("store: resolve $HOME (XDG_STATE_HOME is also unset): %w", err)
	}
	return filepath.Join(home, ".local", "state", name, "store.json"), nil
}

// NewStore returns a Store backed by the file at path. The file need not
// exist yet — it is created (along with its parent directory) on first
// write.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// lockPath is a stable sibling file used purely as an flock handle. It is
// deliberately NOT s.path itself: save() replaces s.path via rename, and a
// lock held on a file descriptor survives that file being renamed out from
// under the path — a second process opening s.path fresh by name would then
// be locking a *different* inode than the first process still holds,
// defeating mutual exclusion. A separate, never-renamed lock file avoids
// that hazard entirely (the same reason internal/prlock and this workspace's
// other flock users key their lock file by name rather than locking the
// data file directly).
func (s *Store) lockPath() string {
	return s.path + ".lock"
}

// lockTimeout bounds how long withLock waits for a contended store before
// giving up. The scriptout protocol's per-call process is short-lived (open,
// decode a small JSON file, mutate, encode, close) so a holder should
// release quickly; this is a backstop against a wedged/killed-mid-syscall
// holder, not a tuning knob normal operation should ever approach.
const lockTimeout = 5 * time.Second

const lockPollInterval = 10 * time.Millisecond

// withLock creates (if needed) and holds an exclusive, cross-process flock
// on s.lockPath() for the duration of fn, then releases it. Every Store
// method that reads-then-writes (SetCategory, SetDisposition) or that must
// not observe a concurrent writer's half-finished state (Get) goes through
// this, which is what actually fixes finding A17: the scriptout protocol's
// "one process per call" model means an in-process sync.Mutex (s.mu) never
// spans two concurrent callers, but this OS-level flock does, because it is
// anchored to a file on disk rather than to process memory.
//
// A blocking (non-LOCK_NB) flock would be simpler but has no way to give up
// on a wedged holder; this instead retries a non-blocking flock on
// lockPollInterval until it succeeds or lockTimeout elapses, mirroring
// internal/prlock's own NB-retry shape in the sibling pg-pr module (that
// package cannot be imported directly — different Go module, internal
// package — so the shape is re-derived here rather than shared).
func (s *Store) withLock(fn func() error) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store: mkdir %s: %w", dir, err)
	}
	f, err := os.OpenFile(s.lockPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("store: open lock file %s: %w", s.lockPath(), err)
	}
	defer func() { _ = f.Close() }()

	deadline := time.Now().Add(lockTimeout)
	for {
		lockErr := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if lockErr == nil {
			break
		}
		if !errors.Is(lockErr, unix.EWOULDBLOCK) {
			return fmt.Errorf("store: flock %s: %w", s.lockPath(), lockErr)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("store: timed out after %s waiting for lock %s (held by another pg-connector-pr-github invocation)", lockTimeout, s.lockPath())
		}
		time.Sleep(lockPollInterval)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()

	return fn()
}

// load reads and decodes s.path. A missing file (never written yet) or an
// empty file both decode to a fresh, empty storeFile at currentStoreVersion
// — "never written yet" is not a failure (see PRState's own doc comment
// below).
//
// A file that exists, is non-empty, but fails to PARSE as JSON — finding
// A18's "a single corrupted byte makes every subsequent op fail
// permanently, with no repair path" — is quarantined (best-effort renamed
// aside with its corrupted bytes preserved for forensics) rather than
// returned as a hard error, and load then proceeds as if the file were
// fresh/empty. This is what makes read-only ops (Show, via Get) survive a
// corrupted store rather than failing forever: there is no on-disk state a
// human or a repair tool needs to fix before this backend works again, only
// a quarantined file they MAY inspect later. Losing the corrupted file's
// unreadable content is an accepted trade-off — it was already unreadable —
// preferable to a permanent, silent outage.
//
// load does NOT take s.mu or the cross-process flock itself; callers that
// need both the read and any subsequent write to be atomic across processes
// call load from inside withLock.
func (s *Store) load() (storeFile, error) {
	var data storeFile
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return freshStoreFile(), nil
		}
		return data, fmt.Errorf("store: read %s: %w", s.path, err)
	}
	if len(raw) == 0 {
		return freshStoreFile(), nil
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		quarantinePath := fmt.Sprintf("%s.corrupt.%d", s.path, time.Now().UnixNano())
		// Best-effort: if even the quarantine rename fails (e.g. read-only
		// filesystem), still recover with a fresh in-memory store rather
		// than propagating the parse error — quarantining is a courtesy to
		// a later human, not a precondition for recovering.
		_ = os.Rename(s.path, quarantinePath)
		return freshStoreFile(), nil
	}
	if data.PRs == nil {
		data.PRs = map[string]prState{}
	}
	if data.Version > currentStoreVersion {
		return storeFile{}, fmt.Errorf("store: %s was written by a newer version of this backend (schema version %d > %d this binary understands)", s.path, data.Version, currentStoreVersion)
	}
	for v := data.Version; v < currentStoreVersion; v++ {
		if err := storeMigrations[v](&data); err != nil {
			return storeFile{}, fmt.Errorf("store: migrate %s from version %d to %d: %w", s.path, v, v+1, err)
		}
	}
	data.Version = currentStoreVersion
	return data, nil
}

// freshStoreFile returns the zero-state storeFile a never-written-to or
// unreadable store loads as: no PRs yet, already stamped at
// currentStoreVersion (a brand-new store has nothing to migrate).
func freshStoreFile() storeFile {
	return storeFile{Version: currentStoreVersion, PRs: map[string]prState{}}
}

// save atomically writes data to s.path via a temp-file-and-rename in the
// same directory (so the rename is same-filesystem and therefore atomic),
// fsyncing the temp file's contents before the rename and the containing
// directory's entry after it (finding A18: save previously never fsynced
// anything, despite this file's own top-of-file doc comment claiming
// crash-safety — a rename alone only orders the two operations relative to
// each other in the page cache, it does not make either durable against a
// power loss or kernel panic before the page cache is flushed).
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
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename below succeeds
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("store: rename into place: %w", err)
	}
	if err := fsyncDir(dir); err != nil {
		return fmt.Errorf("store: fsync directory %s after rename: %w", dir, err)
	}
	return nil
}

// fsyncDir fsyncs dir itself so the rename's directory-entry update in
// save() is durable, not just the renamed-in file's contents. Without this,
// a crash right after a successful rename can still lose the rename on some
// filesystems/OSes, even though the temp file's own bytes were fsynced.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// SetCategory sets prID's category — a plain set/overwrite into a dedicated
// field, never a GitHub label (interfaces.md's pr op catalog).
func (s *Store) SetCategory(prID, category string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withLock(func() error {
		data, err := s.load()
		if err != nil {
			return err
		}
		st := data.PRs[prID]
		st.Category = category
		data.PRs[prID] = st
		return s.save(data)
	})
}

// SetDisposition sets prID's commentID disposition.
func (s *Store) SetDisposition(prID, commentID string, disposition schema.Disposition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withLock(func() error {
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
	})
}

// PRState is a read-only snapshot of prID's persisted state, used by Show
// to merge the current category/dispositions into the freshly-fetched
// PR-schema response (interfaces.md's pr op catalog). A PR with no persisted writes yet
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
	var out PRState
	err := s.withLock(func() error {
		data, err := s.load()
		if err != nil {
			return err
		}
		st := data.PRs[prID]
		out = PRState(st)
		return nil
	})
	return out, err
}
