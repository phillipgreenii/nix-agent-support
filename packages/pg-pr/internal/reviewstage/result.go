package reviewstage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Result is the routing/provenance sidecar written next to a staged Draft by
// the daemon's review-consumption hook (pg2-4c5i.36). It is kept OUT of the
// human-editable Draft so the human can inspect/edit the Draft between draft
// and post without disturbing (or being disturbed by) routing metadata.
//
// Path scheme: "<repo-slug>-<pr>.result.json", distinct from the Draft's
// "<repo-slug>-<pr>.json" so the two artifacts never collide.
type Result struct {
	Repo      string `json:"repo"`
	PR        int    `json:"pr"`
	Ownership string `json:"ownership"`         // "mine" | "team" (from the draft-review bead label)
	HeadSHA   string `json:"head_sha"`          // SHA the review worktree was checked out at
	BeadID    string `json:"bead_id"`           // the draft-review bead this result satisfies
	Verdict   string `json:"verdict,omitempty"` // approve|request-changes|comment (ADVISORY)
}

// ResultPathFor returns the sidecar file path for a (repo, pr) result.
func ResultPathFor(dir, repo string, pr int) string {
	slug := strings.ReplaceAll(repo, "/", "-")
	return filepath.Join(dir, fmt.Sprintf("%s-%d.result.json", slug, pr))
}

// SaveResult writes the result sidecar atomically (mirrors Save).
func SaveResult(dir string, r *Result) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}
	path := ResultPathFor(dir, r.Repo, r.PR)
	data, err := json.MarshalIndent(r, "", "  ")
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

// LoadResult reads a result sidecar. Returns os.ErrNotExist when none is
// present (mirrors Load).
func LoadResult(dir, repo string, pr int) (*Result, error) {
	data, err := os.ReadFile(ResultPathFor(dir, repo, pr))
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}
	return &r, nil
}

// ClearResult removes the result sidecar. Missing files are not an error.
func ClearResult(dir, repo string, pr int) error {
	err := os.Remove(ResultPathFor(dir, repo, pr))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
