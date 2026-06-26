// Package discover finds the distinct beads DBs reachable from a pn workspace.
// It walks up each repo path (and the root) for a .beads dir, BOUNDED at the
// workspace root (never above — else it could resolve a foreign .beads and, via a
// matching wsid slug, cross-resolve), and dedupes by resolved Dolt identity
// (host:port|database|project_id) — NOT the .beads path or issue prefix, which
// differ per repo even when they map to one shared project.
package discover

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type DB struct {
	Dir      string // directory to pass to `bd -C`
	Identity string // dedupe key
}

// FindBeadsDir walks up from start looking for a `.beads` directory, never
// ascending above root. Returns the directory CONTAINING .beads.
func FindBeadsDir(start, root string) (string, bool) {
	cur, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	for {
		if fi, err := os.Stat(filepath.Join(cur, ".beads")); err == nil && fi.IsDir() {
			return cur, true
		}
		if cur == rootAbs {
			return "", false // reached the bound without finding one
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false // filesystem root
		}
		// Stop if the parent would escape the root subtree.
		if !withinRoot(parent, rootAbs) {
			return "", false
		}
		cur = parent
	}
}

func withinRoot(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

type metadata struct {
	Host      string `json:"dolt_server_host"`
	Database  string `json:"dolt_database"`
	ProjectID string `json:"project_id"`
}

// DoltIdentity reads dir/.beads metadata + port and returns the dedupe key.
func DoltIdentity(dir string) (string, error) {
	beads := filepath.Join(dir, ".beads")
	raw, err := os.ReadFile(filepath.Join(beads, "metadata.json"))
	if err != nil {
		return "", fmt.Errorf("read .beads/metadata.json in %q: %w", dir, err)
	}
	var m metadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("parse .beads/metadata.json in %q: %w", dir, err)
	}
	port := ""
	if pb, err := os.ReadFile(filepath.Join(beads, "dolt-server.port")); err == nil {
		port = strings.TrimSpace(string(pb))
	}
	return fmt.Sprintf("%s:%s|%s|%s", m.Host, port, m.Database, m.ProjectID), nil
}

// DistinctDBs resolves the distinct beads DBs for root + paths, deduping by Dolt
// identity. Paths with no .beads at/below root are skipped.
func DistinctDBs(paths []string, root string) ([]DB, error) {
	seen := map[string]bool{}
	var out []DB
	for _, p := range append([]string{root}, paths...) {
		dir, ok := FindBeadsDir(p, root)
		if !ok {
			continue
		}
		id, err := DoltIdentity(dir)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, DB{Dir: dir, Identity: id})
	}
	return out, nil
}
