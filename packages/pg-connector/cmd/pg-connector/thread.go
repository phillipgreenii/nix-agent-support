// thread.go: the "pg-connector thread" CLI verb group, wiring the thread
// capability's already-landed Provider/NewDispatchTable
// (pkg/provider/thread, bead pg2-2j5ac.40.3) and wire schema
// (pkg/schema/thread.go) into pg-connector's Tier-1 core — the CLI layer
// pg2-2j5ac.40.3 landed everything else for (Provider/NewDispatchTable,
// the wire schema, registry.go's entityTypes entry) but never wired
// itself (bead pg2-2j5ac.40.6, discovered while working sibling packet
// pg2-2j5ac.40.4 "pg-desk run thread"). registry.go's entityTypes
// already lists "thread" [cmd/pg-connector/registry.go], so this file
// makes no registry change of its own.
//
// thread is read-only by design (pkg/provider/thread.Provider declares
// only Show/List — see that package's own doc comment), mirroring
// pkg/provider/attention.Provider's/pkg/provider/search.Provider's own
// read-only precedent: no create/comment/transition/update/close/deps
// verb here, unlike issue.go/pr.go.
//
// "thread changes" (bead pg2-955py) adds NO new algorithm here at all,
// exactly like "calendar changes" (calendar.go's own doc comment): its
// constructor, newThreadChangesCmd, lives in changes.go (that file's own
// FOURTH newChangesCmd(entityType) caller), reusing that file's existing
// delta-ledger implementation completely unchanged. This is consistent
// with "read-only by design" above — changes only ever reports entities
// pg-connector already fetched via the SAME "list" wire op
// fanOutThreadList below calls, never a create/comment/transition/
// update/close/deps mutation — it was missing only because thread had no
// CLI surface at all when changes.go was first written (see that file's
// own SCOPE NOTE and UPDATE (thread, ...) comments), not because it was
// deliberately excluded once thread existed.
//
// Freedom-boundary choices made by this packet (stated here per this
// bead's own Contract, which requires either choice to be recorded):
//
//   - show is a plain id-keyed targeted op via DispatchTargeted (the
//     Tier-1 multi-instance try-each resolution policy), mirroring
//     agentsession.go's newAgentSessionShowCmd exactly — NOT
//     cache_dispatch.go's dispatchShowWithCache umbrella-entity-cache
//     fallback that issue.go's/pr.go's own show uses. Reason: thread has
//     exactly one Tier-2 backend today (pg-connector-thread-slack),
//     mirroring calendar.go's own documented "a single-backend
//     capability does not yet warrant it" choice, rather than issue/pr's
//     cache-backed show. Either was acceptable per this bead's own
//     Contract; DispatchTargeted alone keeps this file's surface minimal
//     until a second thread backend exists to make the cache fallback
//     pull its weight.
//   - list is registered under the SAME list-valued, named-query "list"
//     op convention as pr/issue (never calendar.go's own parameter-based
//     fan-out — pkg/provider/thread/dispatch.go's "list" handler resolves
//     args.query against config.queries centrally, identically to
//     pkg/provider/issue/dispatch.go's own "list" entry). fanOutThreadList
//     mirrors issue.go's fanOutIssueList/newIssueListCmd fan-out-by-
//     named-query shape, including query_not_recognized handling via
//     list.go's shared allQueryNotRecognized/listQueryNotRecognizedErr.
//     The same single-backend reasoning as show applies here too: no
//     cacheFallbackEntities/putLiveEntity cache-fallback wiring, omitted
//     for consistency with calendar.go's own choice rather than included
//     for consistency with issue/pr.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

func newThreadCmd() *cobra.Command {
	threadCmd := &cobra.Command{
		Use:   "thread",
		Short: "Thread capability commands (read-only)",
	}
	threadCmd.AddCommand(newThreadShowCmd())
	threadCmd.AddCommand(newThreadListCmd())
	threadCmd.AddCommand(newThreadChangesCmd())
	return threadCmd
}

func newThreadShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a thread's current state",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportThreadTargetedOutcome(cmd, nil, err, humanizeThreadShow)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "thread", "show", map[string]string{"id": args[0]}, *backendFlag)
		return reportThreadTargetedOutcome(cmd, resp, dispatchErr, humanizeThreadShow)
	}
	return cmd
}

// threadListOutcome is "thread list"'s wire response, mirroring
// issueListOutcome's identical {entities, present_ids, sources} shape
// (issue.go's own doc comment on issueListOutcome explains PresentIDs'
// "always populated regardless of ids_only" convention, which applies
// identically here with Thread in place of Issue).
type threadListOutcome struct {
	Entities   []schema.Thread `json:"entities"`
	PresentIDs []string        `json:"present_ids"`
	Sources    []SourceResult  `json:"sources"`
}

// fanOutThreadList mirrors fanOutIssueList (issue.go) exactly, decoding
// into schema.ThreadListResult instead of schema.IssueListResult — minus
// the cache-fallback/cache-write calls issue.go's own version makes (see
// this file's header comment for why: no cache wiring for a single-
// backend capability, mirroring calendar.go's own "deliberately no
// umbrella entity-cache fallback" choice).
func fanOutThreadList(ctx context.Context, reg *Registry, backends []string, query string, idsOnly bool) threadListOutcome {
	out := threadListOutcome{
		Entities:   make([]schema.Thread, 0),
		PresentIDs: make([]string, 0),
		Sources:    make([]SourceResult, 0, len(backends)),
	}
	for _, b := range backends {
		config, err := reg.BackendConfig(b)
		if err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		resp, err := scriptout.Invoke(ctx, b, "list", map[string]any{"query": query, "ids_only": idsOnly}, config)
		if err != nil {
			out.Sources = append(out.Sources, classifyListSource(b, err))
			continue
		}
		var result schema.ThreadListResult
		if err := scriptout.Decode(resp.Result, &result); err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		out.Entities = append(out.Entities, result.Entities...)
		out.PresentIDs = append(out.PresentIDs, result.PresentIDs...)
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: len(result.PresentIDs)})
	}
	return out
}

func newThreadListCmd() *cobra.Command {
	var query string
	var idsOnly bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List threads matching a named query, fanned out across every registered thread backend unless --backend pins one",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered thread backend")
	cmd.Flags().StringVar(&query, "query", "", "named query to run, resolved against each backend's own config.queries (required)")
	cmd.Flags().BoolVar(&idsOnly, "ids-only", false, "return only each matched thread's id, omitting full entity detail")
	_ = cmd.MarkFlagRequired("query")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		// Both of these are Tier-1 CLI-level failures before any backend
		// is ever dispatched (no config found, or --backend names a
		// backend not registered under connector.thread). This packet's
		// own acceptance criteria require every error path — registry-
		// load failure and dispatch failure alike — to route through
		// writeTargetedResult/writeFanOutResult rather than a bare
		// `return err`, so both are reported through
		// reportThreadTargetedOutcome (a nil resp there synthesizes a
		// scriptout.ErrorResponse envelope on stdout via
		// scriptout.ErrorResponse, mirroring newThreadShowCmd's own
		// LoadRegistry-failure handling above) — deliberately NOT
		// mirroring issue.go's/calendar.go's own newIssueListCmd/
		// newCalendarListCmd, whose identical LoadRegistry/
		// resolveListBackends failure paths still bare-`return err`
		// (an existing, out-of-scope gap in those files, not
		// reintroduced here).
		reg, err := LoadRegistry()
		if err != nil {
			return reportThreadTargetedOutcome(cmd, nil, err, humanizeThreadListOutcomeError)
		}
		backends, err := resolveListBackends(reg, "thread", *backendFlag)
		if err != nil {
			return reportThreadTargetedOutcome(cmd, nil, err, humanizeThreadListOutcomeError)
		}
		outcome := fanOutThreadList(cmd.Context(), reg, backends, query, idsOnly)
		if allQueryNotRecognized(outcome.Sources) {
			return reportThreadTargetedOutcome(cmd, nil, listQueryNotRecognizedErr("thread", query), humanizeThreadListOutcomeError)
		}
		return writeFanOutResult(cmd, outcome, listExitCode(outcome.Sources), func() string {
			return humanizeThreadListOutcome(outcome)
		})
	}
	return cmd
}

// humanizeThreadListOutcome formats "thread list"'s fan-out outcome for
// human display, mirroring humanizeIssueListOutcome's own shape
// (issue.go): --ids-only leaves Entities empty by design and renders
// PresentIDs as a plain id list instead of falling through to the empty
// "(none)" branch.
func humanizeThreadListOutcome(o threadListOutcome) string {
	var b strings.Builder
	switch {
	case len(o.Entities) > 0:
		fmt.Fprintf(&b, "threads (%d):\n", len(o.Entities))
		for _, th := range o.Entities {
			fmt.Fprintf(&b, "  [%s] %s (%d replies)\n", th.ID, th.Channel, th.ReplyCount)
		}
	case len(o.PresentIDs) > 0:
		fmt.Fprintf(&b, "threads (%d, ids only):\n", len(o.PresentIDs))
		for _, id := range o.PresentIDs {
			fmt.Fprintf(&b, "  %s\n", id)
		}
	default:
		b.WriteString("threads: (none)\n")
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.Sources))
	return strings.TrimRight(b.String(), "\n")
}

// reportThreadTargetedOutcome writes resp's outcome to stdout, mirroring
// reportIssueTargetedOutcome's/reportAgentSessionTargetedOutcome's own
// thin wrapper around output.go's writeTargetedResult.
func reportThreadTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	return writeTargetedResult(cmd, resp, err, humanize)
}

// humanizeThreadListOutcomeError is the humanize callback passed to
// reportThreadTargetedOutcome for every "thread list" CLI-level failure
// path (registry-load failure, an unresolvable --backend pin, or the
// allQueryNotRecognized umbrella failure) — writeHumanTargeted
// (output.go) never actually calls humanize on the error branch (resp.
// Error != nil short-circuits straight to its own "error: code: message"
// line), so this only needs to satisfy humanizeResult's signature, never
// to render anything itself. Named rather than a repeated inline closure
// so every one of "thread list"'s three CLI-level failure call sites
// shares one definition.
func humanizeThreadListOutcomeError(json.RawMessage) (string, error) { return "", nil }

// humanizeThreadShow formats a `thread show` result (schema.Thread) for
// human display.
func humanizeThreadShow(raw json.RawMessage) (string, error) {
	var th schema.Thread
	if err := scriptout.Decode(raw, &th); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "thread %s [%s]\n", th.ID, th.Channel)
	if th.Permalink != "" {
		fmt.Fprintf(&b, "  permalink: %s\n", th.Permalink)
	}
	if th.StartedBy != "" {
		fmt.Fprintf(&b, "  started_by: %s\n", th.StartedBy)
	}
	if len(th.Participants) > 0 {
		fmt.Fprintf(&b, "  participants: %s\n", strings.Join(th.Participants, ", "))
	}
	fmt.Fprintf(&b, "  reply_count: %d\n", th.ReplyCount)
	if th.Text != "" {
		fmt.Fprintf(&b, "  text: %s\n", th.Text)
	}
	fmt.Fprintf(&b, "  mentions_me: %t\n", th.MentionsMe)
	return strings.TrimRight(b.String(), "\n"), nil
}
