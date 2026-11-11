package levels

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Store holds the dwell state of every resource of one sensor subcommand,
// persisted as `<state-dir>/<subcommand>.state.json`. A missing, unreadable or
// malformed file is a LOST file: every resource then starts at the worst level
// of its scale (see Initial), so a lost file cannot release the herd.
type Store struct {
	path string
	data map[string]State
	// Lost is true when the file could not be used; LoadErr holds the cause for
	// a file that existed but was unreadable or malformed (nil when absent).
	Lost    bool
	LoadErr error
}

// StatePath returns the state file of a subcommand in dir.
func StatePath(dir, subcommand string) string {
	return filepath.Join(dir, subcommand+".state.json")
}

// NewMemoryStore returns a store that never touches the disk (the `--raw`
// mode, and tests).
func NewMemoryStore() *Store { return &Store{data: map[string]State{}} }

// OpenStore reads the state file at path. It never fails: a file that cannot be
// used yields an empty store flagged Lost.
func OpenStore(path string) *Store {
	s := &Store{path: path, data: map[string]State{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		s.Lost = true
		if !errors.Is(err, fs.ErrNotExist) {
			s.LoadErr = err
		}
		return s
	}
	var file struct {
		Resources map[string]State `json:"resources"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		s.Lost = true
		s.LoadErr = fmt.Errorf("state file %s: %w", path, err)
		return s
	}
	if file.Resources != nil {
		s.data = file.Resources
	}
	return s
}

// Get returns the state of a resource, or Initial(scale, now) when none is
// recorded.
func (s *Store) Get(resource string, scale Scale, now time.Time) State {
	if st, ok := s.data[resource]; ok {
		return st
	}
	return Initial(scale, now)
}

// Put records the state of a resource.
func (s *Store) Put(resource string, st State) { s.data[resource] = st }

// Extra returns a copy of a resource's extra key/value slot.
func (s *Store) Extra(resource string) map[string]string {
	out := map[string]string{}
	for k, v := range s.data[resource].Extra {
		out[k] = v
	}
	return out
}

// SetExtra replaces a resource's extra slot, keeping its level and since (a
// resource with no record yet gets an empty level, read as lost by Step).
func (s *Store) SetExtra(resource string, extra map[string]string) {
	st := s.data[resource]
	st.Extra = extra
	s.data[resource] = st
}

// Save writes the store atomically (temp file and rename). A memory store is
// a no-op.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(struct {
		Resources map[string]State `json:"resources"`
	}{s.data}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// Apply runs Step for one observation against the store and records the result:
// the one call a sensor makes per resource. The returned level is the one to
// announce.
func (s *Store) Apply(obs Observation, now time.Time, dwell time.Duration, bypass bool) (string, bool) {
	st := s.Get(obs.Resource, obs.Scale, now)
	next, changed := Step(st, obs, now, dwell, bypass)
	s.Put(obs.Resource, next)
	return next.Level, changed
}
