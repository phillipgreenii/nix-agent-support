package treestate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// State holds the set of collapsed path tree nodes.
type State struct {
	collapsed map[string]bool
}

type stateJSON struct {
	CollapsedPaths []string `json:"collapsedPaths"`
}

// NewState returns an empty in-memory State (for testing).
func NewState() *State {
	return &State{collapsed: make(map[string]bool)}
}

// Load reads collapse state from cacheDir/tree-state.json.
// Returns an empty State on any error or when cacheDir is "".
func Load(cacheDir string) *State {
	s := &State{collapsed: make(map[string]bool)}
	if cacheDir == "" {
		return s
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, "tree-state.json"))
	if err != nil {
		return s
	}
	var raw stateJSON
	if json.Unmarshal(data, &raw) != nil {
		return s
	}
	for _, p := range raw.CollapsedPaths {
		s.collapsed[p] = true
	}
	return s
}

// IsCollapsed reports whether the given path is collapsed.
func (s *State) IsCollapsed(path string) bool {
	return s.collapsed[path]
}

// Toggle flips the collapsed state for path.
func (s *State) Toggle(path string) {
	if s.collapsed[path] {
		delete(s.collapsed, path)
	} else {
		s.collapsed[path] = true
	}
}

// Save writes collapse state to cacheDir/tree-state.json.
// No-op when cacheDir is "". Creates cacheDir if it does not exist.
func (s *State) Save(cacheDir string) error {
	if cacheDir == "" {
		return nil
	}
	raw := stateJSON{}
	for p := range s.collapsed {
		raw.CollapsedPaths = append(raw.CollapsedPaths, p)
	}
	sort.Strings(raw.CollapsedPaths)
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cacheDir, "tree-state.json"), data, 0o644)
}
