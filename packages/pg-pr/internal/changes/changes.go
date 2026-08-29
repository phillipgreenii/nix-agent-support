// Package changes implements the `pg-pr changes --since <ts>` query.
//
// It reports pg-pr-managed beads that were created, updated, or closed
// since a caller-supplied timestamp (Since), plus any per-repo sync errors
// recorded by the sync engine (LoadRepoErrors). The latter used to live in a
// separate $XDG_STATE_HOME/pg-pr/repo-state.json file; it now reads the
// sync engine's own SQLite store (schema v17's repo_sync_state table,
// pg2-ynhr.8) instead.
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
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
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

// RepoError is a single sync error read from the sync engine's store
// (see LoadRepoErrors).
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
// --updated-after <ts>`; tests inject a stub runner. The returned
// ChangeSet.Errors is always empty — callers that also want per-repo sync
// errors call LoadRepoErrors separately and merge it in (a fan-out caller
// querying several bd workspaces needs that lookup only once, not once per
// workspace).
func Since(ctx context.Context, ts time.Time, runner beads.Runner) (*ChangeSet, error) {
	if runner == nil {
		return nil, errors.New("changes: runner required")
	}
	cs := &ChangeSet{Since: ts.UTC()}

	tsStr := ts.UTC().Format(time.RFC3339)
	stdout, err := runner.Run(
		ctx,
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

// LoadRepoErrors reads the sync engine's per-repo sync state from the SQLite
// store at path and returns one RepoError per repo whose most recent sync
// attempt failed. It follows the same stat-before-open contract as the rest
// of this CLI's store-reading code paths (see cmd/pg-pr/pr_view.go's
// loadPRView): a store that doesn't exist yet (an idle, never-synced
// machine) is not an error and this function never creates one as a side
// effect. path == "" is likewise treated as "nothing to read", not an error.
func LoadRepoErrors(ctx context.Context, path string) ([]RepoError, error) {
	if path == "" {
		return nil, nil
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, nil
	}
	db, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("changes: open store: %w", err)
	}
	defer func() { _ = db.Close() }()

	states, err := db.RepoSyncStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("changes: read repo sync state: %w", err)
	}
	out := make([]RepoError, 0, len(states))
	for _, st := range states {
		if st.LastErrorMessage == "" {
			continue
		}
		out = append(out, RepoError{Repo: st.Repo, Code: st.LastErrorCode, Message: st.LastErrorMessage})
	}
	return out, nil
}
