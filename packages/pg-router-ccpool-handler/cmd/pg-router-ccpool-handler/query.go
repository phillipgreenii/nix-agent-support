package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// queryRequest decodes the source.query wire message (packages/pg-router/
// schemas/source.query.schema.json).
type queryRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Callback      string `json:"callback"`
}

// runQuery implements the `query` INTF-SOURCE subcommand (pull direction):
// reads the source.query request as JSON on stdin, writes source.query-reply
// JSON to stdout.
//
// As of docket pg2-oju6w's Task 5.8 (ADR 0065's "Source-side boundary"
// section), this is the beads-backed query source pg-router's own
// (now-deleted) built-in default query set used to provide in-process
// (packages/pg-router/internal/query/beads.go's BeadsReady,
// internal/roles/builtin.go's built-in role/query pairing) — moved here as
// a registered kind:"source" participant, reached over the wire instead:
//
//   - --query-config absent (or its PG_ROUTER_CCPOOL_HANDLER_QUERY env var
//     unset): this invocation is UNCONFIGURED and always answers inline
//     with ZERO events — the schema-legal stub reply this subcommand has
//     always given (kept unchanged so an operator who has not opted into
//     the real beads source yet, and this module's own live conformance
//     check, TestLiveQuery, keep working with no config at all).
//   - --query-config present: loads the beads-backed filters it names
//     (labels/exclude-labels/title-prefix/item-type/emitType — the same
//     ones BeadsReady used to carry), runs this module's own startup
//     pre-flight (precheck — bd reachable, beads-store prefix matches;
//     preflight.go), then answers with the REAL `bd ready` results, mapped
//     to wire events under the configured emitType.
//
// The --query-config-present branch is this module's own INTF-CCH-BEADS
// query-surface half (docs/behavior/interfaces.md) — this module's
// beads-backed source querying bd for events.
func runQuery(args []string) int {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	queryConfigPath := fs.String("query-config", os.Getenv(envQueryConfig), "path to this process's own beads-backed query config JSON (or "+envQueryConfig+")")
	cfgPath := fs.String("config", os.Getenv(envConfig), "path to this process's own launch config JSON (or "+envConfig+")")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "query:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "query: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "query takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query: read request from stdin:", err)
		return conformance.ExitError
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		writeErrorReply(os.Stdout, "malformed JSON: "+err.Error())
		return conformance.ExitError
	}
	if err := conformance.Check("source.query", v); err != nil {
		writeErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	var req queryRequest
	_ = json.Unmarshal(raw, &req) // Check above already proved this decodes.

	qf, configured, err := loadQueryConfig(*queryConfigPath)
	if err != nil {
		writeErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	if !configured {
		writeReply(os.Stdout, map[string]any{
			"schemaVersion": schemas.SchemaVersion,
			"id":            req.ID,
			"events":        []any{},
		})
		return conformance.ExitOK
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		writeErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	ctx := context.Background()
	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)
	if err := precheck(ctx, cfg.RepoRoot, cfg.BeadsPrefix, br); err != nil {
		writeErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	// Opportunistic reconciliation (pg2-hrppg): this query tick is this
	// binary's own only invocation that already recurs on a schedule while
	// the daemon is up (reconcile.go's own doc comment), so it is where a
	// session whose bead closed since the last tick gets reconciled, rather
	// than leaking until the eventual once-per-process preShutdown sweep.
	// Gated on `configured` (this branch) the same way precheck above is —
	// an unconfigured invocation (no --query-config; TestLiveQuery's own
	// case) must stay a pure, side-effect-free stub reply, never touching a
	// real ccpool/bd. Best effort: logged, never turned into a query
	// failure — this subcommand's primary job is answering the pull, not
	// housekeeping.
	if closed := reconcileClosedBeadSessions(ctx, ccpool.NewCLIRunner(cfg), gitWorktreeOpener, br, cfg.SessionPrefix); closed > 0 {
		slog.Info("query: reconciled sessions with closed beads", "closed", closed)
	}
	events, err := queryBeadsReady(ctx, br, qf)
	if err != nil {
		writeErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	writeReply(os.Stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"events":        events,
	})
	return conformance.ExitOK
}

// queryBeadsReady runs `bd ready` with qf's label filters, applies its
// optional title_prefix/item_type post-filters, and maps the resulting
// issues to wire events (event.schema.json: id/type/payload) under qf's
// EmitType — ported from packages/pg-router/internal/query/beads.go's
// BeadsReady.Run (docket pg2-oju6w's Task 5.8). Each event's `id` is
// EmitType+":"+issue.ID, the same FingerprintID dedup convention that
// file used (INV-EVT-3). payload carries the item's id/type/title/metadata
// fields flat — the SAME shape discover.ToQueueEvent writes on the
// pg-router side (Task 5.6) and this module's own itemFromPayload
// (dispatch.go) reads back, so an event this source emits round-trips
// through the core unchanged.
func queryBeadsReady(ctx context.Context, br beads.Runner, qf queryFile) ([]map[string]any, error) {
	issues, err := beads.Ready(ctx, br, labelArgs(qf.Labels, qf.ExcludeLabels)...)
	if err != nil {
		return nil, fmt.Errorf("beads-ready query: %w", err)
	}
	issues = postFilterIssues(issues, qf.TitlePrefix, qf.ItemType)
	events := make([]map[string]any, 0, len(issues))
	for _, iss := range issues {
		events = append(events, map[string]any{
			"id":   fingerprintID(qf.EmitType, iss.ID),
			"type": qf.EmitType,
			"payload": map[string]any{
				"id":       iss.ID,
				"type":     iss.Type,
				"title":    iss.Title,
				"metadata": iss.Metadata,
			},
		})
	}
	return events, nil
}

// fingerprintID mirrors packages/pg-router/internal/event.FingerprintID
// exactly (eventType + ":" + itemID) — the stable dedup id INV-EVT-3 keys
// on, re-derived here rather than imported since this module cannot reach
// pg-router's own internal/event package (Go's internal-package visibility
// rule).
func fingerprintID(eventType, itemID string) string { return eventType + ":" + itemID }

// labelArgs renders labels/exclude as `bd ready` flag pairs.
func labelArgs(labels, exclude []string) []string {
	var a []string
	for _, l := range labels {
		a = append(a, "--label", l)
	}
	for _, l := range exclude {
		a = append(a, "--exclude-label", l)
	}
	return a
}

// postFilterIssues applies the optional client-side title_prefix/item_type
// filters BeadsReady used to apply, verbatim.
func postFilterIssues(in []beads.Issue, titlePrefix, itemType string) []beads.Issue {
	if titlePrefix == "" && itemType == "" {
		return in
	}
	var out []beads.Issue
	for _, i := range in {
		if itemType != "" && i.Type != itemType {
			continue
		}
		if titlePrefix != "" && !strings.HasPrefix(i.Title, titlePrefix) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// writeReply/writeErrorReply are dispatch.go's own — this file uses them
// unchanged (same package, single definition).
