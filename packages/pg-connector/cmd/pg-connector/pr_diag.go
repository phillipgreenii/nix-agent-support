// pr_diag.go: the diagnostic and refresh verbs of the "pr" and "ci" groups:
// "pr refresh", "pr explain" and "ci explain" (spec: Caller experience).
//
// Each is a TARGETED op the umbrella forwards to the daemon-backed backend and
// sends exactly one request through DispatchTargetedOptional, so --backend pins,
// a registry that also holds an old backend without the op does not fail the
// call (a backend answering unknown_op is skipped), and the exit code is the
// targeted scheme (0, 4, 1) from writeTargetedResult. The verbs only read and
// queue: they write nothing to the umbrella's ledger or cache.
//
//   - "pr explain <id>" and "ci explain <pr-id>" send explain {id} and answer
//     "why is this stale", group by group.
//   - "pr refresh <id>" sends refresh {id} and "pr refresh --query Q" sends
//     refresh {query}; the backend queues an interactive refresh and answers at
//     once with its queued list, which this verb prints without waiting for the
//     refresh. The daemon's optional "groups" argument is deliberately not
//     exposed (the spec adds no flag for it).
//
// The explain and refresh answers have no shared Go type (the daemon's types
// live under cmd/pg-connector-github/internal, which the umbrella may not
// import), so this file decodes locally only what the human rendering needs; the
// JSON output passes the wire result through unchanged.
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// Wire op names of the verbs of this file.
const (
	opExplain = "explain"
	opRefresh = "refresh"
)

// newPrRefreshCmd is "pr refresh [<id>] [--query Q]": queue an interactive
// refresh of one PR (by id) or of every PR a configured query matches, and
// return at once.
func newPrRefreshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh [<id>]",
		Short: "Queue an interactive refresh of a PR, or of every PR a query matches, and return at once",
		Long: `Queue a class 2 (interactive) refresh in the backend and return at once with
the backend's queued list; it does not wait for the refresh to run.

Give exactly one of <id> (one PR) and --query <name> (every PR the configured
query matches); giving both or neither is invalid_argument and makes no backend
call.

Use it after an out-of-band write (git push, gh pr create) when the next read
must see that write. Polling paths SHOULD NOT use it.`,
		Args: cobra.MaximumNArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	query := cmd.Flags().String("query", "", "refresh every PR this configured query matches (instead of one <id>)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reqArgs, usageErr := refreshArgs(args, *query)
		if usageErr != nil {
			return reportPrTargetedOutcome(cmd, nil, usageErr, humanizeRefresh)
		}
		reg, err := LoadRegistry()
		if err != nil {
			return reportPrTargetedOutcome(cmd, nil, err, humanizeRefresh)
		}
		resp, dispatchErr := DispatchTargetedOptional(cmd.Context(), reg, "pr", opRefresh, reqArgs, *backendFlag)
		return reportPrTargetedOutcome(cmd, resp, dispatchErr, humanizeRefresh)
	}
	return cmd
}

// refreshArgs builds the refresh request arguments from the verb's positional
// arguments and --query: {id} or {query}. Exactly one must be given; anything
// else is invalid_argument, reported before any backend call.
func refreshArgs(args []string, query string) (map[string]any, error) {
	id := ""
	if len(args) > 0 {
		id = strings.TrimSpace(args[0])
	}
	query = strings.TrimSpace(query)
	switch {
	case id != "" && query != "":
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "pr refresh takes either an <id> or --query, not both")
	case id == "" && query == "":
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "pr refresh needs an <id> or --query <name>")
	case query != "":
		return map[string]any{"query": query}, nil
	default:
		return map[string]any{"id": id}, nil
	}
}

// newPrExplainCmd is "pr explain <id>": why is this PR stale, group by group.
func newPrExplainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explain <id>",
		Short: "Explain why a PR is stale: each field group's freshness, queue position, pending settle row and last changes",
		Long: `Answer "why is this stale" from the backend's explain op, without calling the
origin. For each field group the answer carries when it was fetched, until when it
is fresh, when it must be refetched, its last error, the head and base it was
fetched for and its queue position; it also carries the PR's pending settle row
(the groups it awaits and its deadline) and the last change rows when there are
any.`,
		Args: cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportPrTargetedOutcome(cmd, nil, err, humanizeExplain)
		}
		resp, dispatchErr := DispatchTargetedOptional(cmd.Context(), reg, "pr", opExplain, map[string]any{"id": args[0]}, *backendFlag)
		return reportPrTargetedOutcome(cmd, resp, dispatchErr, humanizeExplain)
	}
	return cmd
}

// newCiExplainCmd is "ci explain <pr-id>": the same document for the CI view of
// a PR (its summary and runs groups).
func newCiExplainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explain <pr-id>",
		Short: "Explain why a PR's CI view is stale: the summary and runs groups' freshness, queue position, pending settle row and last changes",
		Long: `Answer "why is this stale" for the CI view of a PR from the backend's explain
op, without calling the origin: for the summary and runs groups, when each was
fetched, until when it is fresh, when it must be refetched, its last error, the
head and base it was fetched for and its queue position; and the PR's pending
settle row and last change rows when there are any.`,
		Args: cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportCiTargetedOutcome(cmd, nil, err, humanizeExplain)
		}
		resp, dispatchErr := DispatchTargetedOptional(cmd.Context(), reg, "ci", opExplain, map[string]any{"id": args[0]}, *backendFlag)
		return reportCiTargetedOutcome(cmd, resp, dispatchErr, humanizeExplain)
	}
	return cmd
}

// explainDoc decodes the part of the daemon's explain answer the human
// rendering shows. Times stay the wire's RFC 3339 strings; an absent optional
// field decodes to its zero value (or nil) and is omitted from the rendering.
type explainDoc struct {
	Instance      string               `json:"instance"`
	ID            string               `json:"id"`
	Ref           string               `json:"ref"`
	NodeID        string               `json:"node_id"`
	KeepAlive     bool                 `json:"keep_alive"`
	LastAccess    string               `json:"last_access"`
	TombstonedAt  string               `json:"tombstoned_at"`
	Groups        []explainGroup       `json:"groups"`
	Note          string               `json:"note"`
	PendingSettle *explainPendingRow   `json:"pending_settle"`
	LastChanges   []explainChangeEntry `json:"last_changes"`
}

type explainGroup struct {
	Group           string `json:"group"`
	FetchedAt       string `json:"fetched_at"`
	FreshUntil      string `json:"fresh_until"`
	MaxAgeAt        string `json:"max_age_at"`
	Fresh           bool   `json:"fresh"`
	ExpiredByMaxAge bool   `json:"expired_by_max_age"`
	LastError       string `json:"last_error"`
	FetchedForHead  string `json:"fetched_for_head"`
	FetchedForBase  string `json:"fetched_for_base"`
	QueuePosition   *int   `json:"queue_position"`
	Status          string `json:"status"`
	Reason          string `json:"reason"`
	StampsMatch     *bool  `json:"stamps_match"`
	Active          *bool  `json:"active"`
	Failing         *bool  `json:"failing"`
	CachedJobs      *int   `json:"cached_jobs"`
}

type explainPendingRow struct {
	Awaited []struct {
		Group string `json:"group"`
		Since string `json:"since"`
	} `json:"awaited"`
	DetectedAt string   `json:"detected_at"`
	Deadline   string   `json:"deadline"`
	Kinds      []string `json:"kinds"`
}

type explainChangeEntry struct {
	Seq     int64    `json:"seq"`
	Version int64    `json:"version"`
	Kinds   []string `json:"kinds"`
	Origin  string   `json:"origin"`
	At      string   `json:"at"`
}

// humanizeExplain formats an explain answer for human display: the entity
// header, one block per group in the document's order, then the pending settle
// row and the last change rows when present. An absent optional field is
// omitted rather than printed as a zero value.
func humanizeExplain(raw json.RawMessage) (string, error) {
	var d explainDoc
	if err := scriptout.Decode(raw, &d); err != nil {
		return "", err
	}
	var b strings.Builder
	head := d.ID
	if d.Ref != "" && d.Ref != d.ID {
		head += " (" + d.Ref + ")"
	}
	if d.Instance != "" {
		fmt.Fprintf(&b, "%s %s\n", d.Instance, head)
	} else {
		fmt.Fprintf(&b, "%s\n", head)
	}
	writeField(&b, 2, "node_id", d.NodeID)
	fmt.Fprintf(&b, "  keep_alive: %t\n", d.KeepAlive)
	writeField(&b, 2, "last_access", d.LastAccess)
	writeField(&b, 2, "tombstoned_at", d.TombstonedAt)
	writeField(&b, 2, "note", d.Note)
	if len(d.Groups) == 0 {
		b.WriteString("  groups: (none stored)\n")
	} else {
		fmt.Fprintf(&b, "  groups (%d):\n", len(d.Groups))
		for _, g := range d.Groups {
			writeExplainGroup(&b, g)
		}
	}
	if p := d.PendingSettle; p != nil {
		b.WriteString("  pending settle:\n")
		writeField(&b, 4, "detected_at", p.DetectedAt)
		writeField(&b, 4, "deadline", p.Deadline)
		if len(p.Kinds) > 0 {
			writeField(&b, 4, "kinds", strings.Join(p.Kinds, ", "))
		}
		if len(p.Awaited) > 0 {
			b.WriteString("    awaiting:\n")
			for _, a := range p.Awaited {
				line := a.Group
				if a.Since != "" {
					line += " (since " + a.Since + ")"
				}
				fmt.Fprintf(&b, "      - %s\n", line)
			}
		}
	}
	if len(d.LastChanges) > 0 {
		fmt.Fprintf(&b, "  last changes (%d, newest first):\n", len(d.LastChanges))
		for _, c := range d.LastChanges {
			fmt.Fprintf(&b, "    - seq %d version %d", c.Seq, c.Version)
			if len(c.Kinds) > 0 {
				fmt.Fprintf(&b, " kinds [%s]", strings.Join(c.Kinds, ", "))
			}
			if c.Origin != "" {
				fmt.Fprintf(&b, " origin %s", c.Origin)
			}
			if c.At != "" {
				fmt.Fprintf(&b, " at %s", c.At)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func writeExplainGroup(b *strings.Builder, g explainGroup) {
	state := "stale"
	if g.Fresh {
		state = "fresh"
	}
	if g.ExpiredByMaxAge {
		state += ", expired by max age"
	}
	fmt.Fprintf(b, "    - %s [%s]\n", g.Group, state)
	writeField(b, 6, "status", g.Status)
	writeField(b, 6, "reason", g.Reason)
	writeField(b, 6, "fetched_at", g.FetchedAt)
	writeField(b, 6, "fresh_until", g.FreshUntil)
	writeField(b, 6, "max_age_at", g.MaxAgeAt)
	writeField(b, 6, "last_error", g.LastError)
	writeField(b, 6, "fetched_for_head", g.FetchedForHead)
	writeField(b, 6, "fetched_for_base", g.FetchedForBase)
	if g.StampsMatch != nil {
		fmt.Fprintf(b, "      stamps_match: %t\n", *g.StampsMatch)
	}
	if g.QueuePosition != nil {
		fmt.Fprintf(b, "      queue_position: %d\n", *g.QueuePosition)
	}
	if g.Active != nil {
		fmt.Fprintf(b, "      active: %t\n", *g.Active)
	}
	if g.Failing != nil {
		fmt.Fprintf(b, "      failing: %t\n", *g.Failing)
	}
	if g.CachedJobs != nil {
		fmt.Fprintf(b, "      cached_jobs: %d\n", *g.CachedJobs)
	}
}

// writeField writes "<indent spaces><name>: <value>\n", or nothing when value
// is empty (an absent optional field is omitted, never printed as a zero value).
func writeField(b *strings.Builder, indent int, name, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "%s%s: %s\n", strings.Repeat(" ", indent), name, value)
}

// refreshQueued decodes a refresh answer ({queued: [{id, groups}]}).
type refreshQueued struct {
	Queued []struct {
		ID     string   `json:"id"`
		Groups []string `json:"groups"`
	} `json:"queued"`
}

// humanizeRefresh formats a refresh answer: the backend's queued list.
func humanizeRefresh(raw json.RawMessage) (string, error) {
	var r refreshQueued
	if err := scriptout.Decode(raw, &r); err != nil {
		return "", err
	}
	if len(r.Queued) == 0 {
		return "refresh: nothing queued", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "refresh queued (%d):\n", len(r.Queued))
	for _, q := range r.Queued {
		if len(q.Groups) > 0 {
			fmt.Fprintf(&b, "  %s: %s\n", q.ID, strings.Join(q.Groups, ", "))
		} else {
			fmt.Fprintf(&b, "  %s\n", q.ID)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
