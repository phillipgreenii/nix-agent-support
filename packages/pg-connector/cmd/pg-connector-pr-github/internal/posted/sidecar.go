package posted

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// ServiceName is the directory under the user state home that holds this
// backend's state.
const ServiceName = "pg-connector-pr-github"

// PostedDirName is the sidecar directory inside the state home.
const PostedDirName = "posted"

// LocksDirName is the lock directory inside the state home.
const LocksDirName = "locks"

// EnvStateDir overrides the backend state home (the directory that holds
// posted/ and locks/).
const EnvStateDir = "PG_CONNECTOR_PR_GITHUB_STATE_DIR"

// StateVersion is the version of the on-disk sidecar shape.
const StateVersion = 1

// ErrCorrupt is wrapped by Load when the sidecar exists but cannot be decoded.
// The file is left untouched: the operator deletes it to allow a repost, and
// treating it as empty would silently re-post dismissed comments.
var ErrCorrupt = errors.New("posted: sidecar is corrupt")

// LastAppend records the most recent append to a PR's pending review.
type LastAppend struct {
	At    string `json:"at"`
	Added int    `json:"added"`
	Head  string `json:"head"`
}

// State is the per-PR sidecar: every fingerprint confirmed posted, the heads
// whose body section was written, and the last append. It is plain JSON so the
// operator can delete it to allow a repost.
type State struct {
	Version      int         `json:"version"`
	Fingerprints []string    `json:"fingerprints"`
	BodyHeads    []string    `json:"body_heads"`
	LastAppend   *LastAppend `json:"last_append,omitempty"`
}

// Has reports whether fp is recorded as confirmed posted.
func (s State) Has(fp string) bool {
	for _, f := range s.Fingerprints {
		if f == fp {
			return true
		}
	}
	return false
}

// AddFingerprints records fingerprints, skipping ones already present.
func (s *State) AddFingerprints(fps ...string) {
	for _, fp := range fps {
		if !s.Has(fp) {
			s.Fingerprints = append(s.Fingerprints, fp)
		}
	}
}

// AddBodyHead records that the body section for head was written.
func (s *State) AddBodyHead(head string) {
	for _, h := range s.BodyHeads {
		if h == head {
			return
		}
	}
	s.BodyHeads = append(s.BodyHeads, head)
}

// StateHomeFromEnv resolves the backend state home: EnvStateDir if set, else
// $XDG_STATE_HOME/pg-connector-pr-github, else
// $HOME/.local/state/pg-connector-pr-github. It returns "" when no home can be
// determined.
func StateHomeFromEnv(getenv func(string) string) string {
	if d := getenv(EnvStateDir); d != "" {
		return d
	}
	if state := getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, ServiceName)
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", ServiceName)
	}
	return ""
}

// Store reads and writes per-PR sidecar files under Dir (the posted/
// directory).
type Store struct {
	Dir string
}

// StoreFromEnv builds the production store at <state home>/posted.
func StoreFromEnv(getenv func(string) string) Store {
	home := StateHomeFromEnv(getenv)
	if home == "" {
		return Store{}
	}
	return Store{Dir: filepath.Join(home, PostedDirName)}
}

// pathPart is the only spelling accepted for a repo owner or name: it keeps a
// hostile or malformed id from escaping the directory.
var pathPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// fileStem returns "<owner>__<repo>__<n>" after validating each part.
func fileStem(owner, repo string, pr int) (string, error) {
	if !pathPart.MatchString(owner) || owner == ".." || !pathPart.MatchString(repo) || repo == ".." {
		return "", fmt.Errorf("posted: %q/%q is not a safe owner/name", owner, repo)
	}
	if pr <= 0 {
		return "", fmt.Errorf("posted: PR number %d is not positive", pr)
	}
	return fmt.Sprintf("%s__%s__%d", owner, repo, pr), nil
}

// Path returns the sidecar file path for a PR.
func (s Store) Path(owner, repo string, pr int) (string, error) {
	if s.Dir == "" {
		return "", errors.New("posted: no state directory could be determined (set " + EnvStateDir + ")")
	}
	stem, err := fileStem(owner, repo, pr)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, stem+".json"), nil
}

// Load reads a PR's sidecar. A missing file is an empty State, not an error
// (a wiped state directory is an accepted situation). An undecodable file
// returns an error wrapping ErrCorrupt and leaves the file in place.
func (s Store) Load(owner, repo string, pr int) (State, error) {
	path, err := s.Path(owner, repo, pr)
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{Version: StateVersion}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("posted: read sidecar: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
	}
	return st, nil
}

// Save writes a PR's sidecar atomically: a temporary file in the same
// directory is written, synced and renamed into place. Any failure is an
// error; callers write an entry only for fingerprints confirmed by the
// post-write re-read.
func (s Store) Save(owner, repo string, pr int, st State) error {
	path, err := s.Path(owner, repo, pr)
	if err != nil {
		return err
	}
	st.Version = StateVersion
	if st.Fingerprints == nil {
		st.Fingerprints = []string{}
	}
	if st.BodyHeads == nil {
		st.BodyHeads = []string{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("posted: marshal sidecar: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("posted: create directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".sidecar-*.tmp")
	if err != nil {
		return fmt.Errorf("posted: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("posted: write sidecar: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("posted: sync sidecar: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("posted: close sidecar: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return fmt.Errorf("posted: chmod sidecar: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("posted: rename sidecar into place: %w", err)
	}
	return nil
}
