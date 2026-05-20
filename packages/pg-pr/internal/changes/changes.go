// Package changes implements the `pg-pr changes --since <ts>` query.
//
// It reports pg-pr-managed beads that were created, updated, or closed
// since a caller-supplied timestamp, plus any per-repo errors recorded in
// the sync state file ($XDG_STATE_HOME/pg-pr/repo-state.json).
//
// Managed bead scope:
//
//   - issue_type == "merge-request" (always included)
//   - issue_type == "feedback"      (always included)
//   - issue_type == "task" or "bug" (included; A3 does NOT filter these to
//     pg-pr ancestry because `bd list` does not return dependency edges in
//     the JSON. A future revision may add per-bead `bd show --json` lookups
//     to filter precisely. For now, callers may see unrelated task/bug rows
//     in workspaces that mix pg-pr usage with other work — explicitly
//     documented here.)
package changes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// Bead is a flattened view of one bead's identity for the change report.
// Fields mirror the bd JSON output we actually need; further metadata
// stays in bd.
type Bead struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RepoError is a single sync error scraped from the state file.
type RepoError struct {
	Repo    string `json:"repo"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// ChangeSet is the JSON payload emitted by `pg-pr changes --since`.
type ChangeSet struct {
	Since   time.Time   `json:"since"`
	Created []Bead      `json:"created,omitempty"`
	Updated []Bead      `json:"updated,omitempty"`
	Closed  []Bead      `json:"closed,omitempty"`
	Errors  []RepoError `json:"errors,omitempty"`
}

// managedTypes is the allow-list of bd issue_types this command surfaces.
// Tasks/bugs are included for completeness — see package doc.
var managedTypes = map[string]bool{
	"merge-request": true,
	"feedback":      true,
	"task":          true,
	"bug":           true,
}

// Since returns the ChangeSet for all managed beads with activity at or
// after ts. The runner is used to invoke `bd list --json --all
// --updated-after <ts>`; tests inject a stub runner. The stateFile path
// is read (best-effort) for repo errors; a missing or unreadable file is
// not an error.
func Since(ctx context.Context, ts time.Time, runner beads.Runner, stateFile string) (*ChangeSet, error) {
	if runner == nil {
		return nil, errors.New("changes: runner required")
	}
	cs := &ChangeSet{Since: ts.UTC()}

	tsStr := ts.UTC().Format(time.RFC3339)
	stdout, err := runner.Run(ctx,
		"list", "--json", "--all", "--limit", "0", "--updated-after", tsStr,
	)
	if err != nil {
		return cs, fmt.Errorf("changes: bd list: %w", err)
	}
	rows, err := parseRows(stdout)
	if err != nil {
		return cs, fmt.Errorf("changes: parse bd JSON: %w", err)
	}

	for _, r := range rows {
		if !managedTypes[r.Type] {
			continue
		}
		bead := Bead{
			ID:        r.ID,
			Type:      r.Type,
			Title:     r.Title,
			Status:    r.Status,
			UpdatedAt: r.UpdatedAt,
		}
		switch {
		case r.Status == "closed" && !r.ClosedAt.IsZero() && !r.ClosedAt.Before(ts):
			cs.Closed = append(cs.Closed, bead)
		case !r.CreatedAt.IsZero() && !r.CreatedAt.Before(ts):
			cs.Created = append(cs.Created, bead)
		default:
			cs.Updated = append(cs.Updated, bead)
		}
	}

	cs.Errors = loadStateErrors(stateFile)
	return cs, nil
}

// row is the subset of bd's --json list shape we care about.
type row struct {
	ID        string    `json:"id"`
	Type      string    `json:"issue_type"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ClosedAt  time.Time `json:"closed_at"`
}

// parseRows unmarshals bd's JSON array, gracefully accepting an empty
// stdout (which bd emits when nothing matches).
func parseRows(stdout string) ([]row, error) {
	if stdout == "" {
		return nil, nil
	}
	var rows []row
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// loadStateErrors reads the sync state file and returns one RepoError per
// repo whose last_error is non-nil. A missing or unparseable file yields
// an empty slice (this is not a fatal condition for the command).
func loadStateErrors(path string) []RepoError {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var sf struct {
		Repos map[string]struct {
			LastError *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"last_error,omitempty"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil
	}
	out := make([]RepoError, 0, len(sf.Repos))
	for repo, st := range sf.Repos {
		if st.LastError == nil {
			continue
		}
		out = append(out, RepoError{
			Repo:    repo,
			Code:    st.LastError.Code,
			Message: st.LastError.Message,
		})
	}
	return out
}

// DefaultStateFile returns the same default sync state file path the sync
// engine uses. Exposed so the CLI wiring can find it without importing
// the (private) sync helper.
func DefaultStateFile() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pg-pr", "repo-state.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pg-pr", "repo-state.json")
}
