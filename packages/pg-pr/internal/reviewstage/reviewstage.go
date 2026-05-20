// Package reviewstage manages staged pending reviews on disk under
// $XDG_STATE_HOME/pg-pr/reviews/<repo-slug>-<pr>.json.
//
// A staged review is the JSON payload an agent will eventually post via
// `pg-pr review post`. Persisting between `draft` and `post` lets a human
// inspect and edit the file before any GitHub API call.
package reviewstage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// Draft is the on-disk shape of a staged review.
type Draft struct {
	Repo     string        `json:"repo"`
	PR       int           `json:"pr"`
	Body     string        `json:"body,omitempty"`
	Comments []api.Comment `json:"comments,omitempty"`
}

// DefaultDir returns the base directory under which staged reviews live.
//
// Priority:
//  1. $PG_PR_STATE_HOME (test override).
//  2. $XDG_STATE_HOME/pg-pr.
//  3. ~/.local/state/pg-pr.
func DefaultDir() string {
	if v := os.Getenv("PG_PR_STATE_HOME"); v != "" {
		return filepath.Join(v, "reviews")
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "pg-pr", "reviews")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pg-pr", "reviews")
}

// PathFor returns the file path for a (repo, pr) draft.
func PathFor(dir, repo string, pr int) string {
	slug := strings.ReplaceAll(repo, "/", "-")
	return filepath.Join(dir, fmt.Sprintf("%s-%d.json", slug, pr))
}

// Save writes the draft atomically.
func Save(dir string, d *Draft) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}
	path := PathFor(dir, d.Repo, d.PR)
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// Load reads a staged draft. Returns os.ErrNotExist when no draft is staged.
func Load(dir, repo string, pr int) (*Draft, error) {
	data, err := os.ReadFile(PathFor(dir, repo, pr))
	if err != nil {
		return nil, err
	}
	var d Draft
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	return &d, nil
}

// Clear removes the staged draft. Missing files are not an error.
func Clear(dir, repo string, pr int) error {
	err := os.Remove(PathFor(dir, repo, pr))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Dedup removes comments from `incoming` that already exist in `existing`.
// Match key: (Path, Line, first 100 chars of Body).
func Dedup(incoming, existing []api.Comment) (unique []api.Comment, skipped int) {
	seen := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		seen[dedupKey(e)] = struct{}{}
	}
	for _, c := range incoming {
		if _, dup := seen[dedupKey(c)]; dup {
			skipped++
			continue
		}
		unique = append(unique, c)
	}
	return unique, skipped
}

func dedupKey(c api.Comment) string {
	body := c.Body
	if len(body) > 100 {
		body = body[:100]
	}
	return fmt.Sprintf("%s\x00%d\x00%s", c.Path, c.Line, body)
}
