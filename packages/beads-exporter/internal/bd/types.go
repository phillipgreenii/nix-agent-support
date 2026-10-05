// Package bd is the adapter between the exporter and the bd command line. All
// execution goes through the Runner interface so tests can substitute a
// recording fake, and every child process gets a fully re-asserted environment.
package bd

import (
	"context"
	"time"
)

// Bead is the subset of bd's issue JSON the exporter decodes.
type Bead struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	DeferUntil *time.Time `json:"defer_until"`
	Labels     []string   `json:"labels"`
	IssueType  string     `json:"issue_type"`
	Priority   int        `json:"priority"`
	Assignee   string     `json:"assignee"`
	CreatedAt  *time.Time `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	UpdatedAt  *time.Time `json:"updated_at"`
	// IsTemplate is omitted by bd unless true.
	IsTemplate bool `json:"is_template"`
}

// ListOpts selects the list invocation. The zero value is bd's default list
// view: every bead that is not closed, excluding gates, ephemeral wisps,
// templates and custom statuses in the done or frozen categories.
type ListOpts struct {
	// All adds --all so closed beads are visible.
	All bool
	// CreatedAfter, when non-zero, adds --created-after (RFC3339, UTC).
	CreatedAfter time.Time
	// ClosedAfter, when non-zero, adds --closed-after (RFC3339, UTC).
	ClosedAfter time.Time
}

// Adapter is the read-only view of one beads database.
type Adapter interface {
	// List runs bd list.
	List(ctx context.Context, opts ListOpts) ([]Bead, error)
	// Ready runs bd ready; extra holds queue flags passed through verbatim.
	Ready(ctx context.Context, extra []string) ([]Bead, error)
	// Blocked runs bd blocked and returns the dependency-blocked beads.
	Blocked(ctx context.Context) ([]Bead, error)
	// CountByStatus runs bd count --by-status and returns status -> count.
	CountByStatus(ctx context.Context) (map[string]int, error)
	// Statuses runs bd statuses and returns every stored status name,
	// built-in and custom.
	Statuses(ctx context.Context) ([]string, error)
}
