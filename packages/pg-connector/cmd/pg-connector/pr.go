// pr.go: the "pg-connector pr" CLI verb group, built by the "generic pr
// entity/capability" packet on top of the Tier-1 core's registry/dispatcher
// and outcome-reporting helper. pg-connector remains the only user-facing
// CLI surface — pr is one of its verb groups, never a separate binary
// (interfaces.md's INTF-CLI).
//
// show/categorize/feedback-set are targeted, id-keyed ops dispatched via
// dispatch.go's DispatchTargeted, which implements this docket's
// multi-instance resolution policy across every backend registered under
// connector.pr (try each in registration order, stopping at the first
// non-not_found answer), and uses the Tier-1 targeted-op exit-code scheme
// (0/4/1) via outcome.go's TargetedExitCode — this file calls the
// dispatcher and hands TargetedExitCode the raw per-call result/error it
// got back; it never decides the exit code itself (INV-EXIT-1). Every one
// of these three, PLUS the new "list" verb below, carries its own
// --backend flag (bead pg2-2j5ac.28.1, design's "id-less op rule"):
// on show/categorize/feedback-set it PINS DispatchTargeted straight to
// that one backend, skipping the try-each policy; on list it either pins
// the fan-out to that one backend or is left empty to fan out across
// every registered pr backend (see newPrListCmd).
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

func newPrCmd() *cobra.Command {
	prCmd := &cobra.Command{
		Use:   "pr",
		Short: "PR capability commands",
	}
	prCmd.AddCommand(newPrShowCmd())
	prCmd.AddCommand(newPrCategorizeCmd())
	prCmd.AddCommand(newPrFeedbackSetCmd())
	prCmd.AddCommand(newPrListCmd())
	return prCmd
}

func newPrShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a PR's current full state, including comments/review-thread entries",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportPrTargetedOutcome(cmd, nil, err, humanizePRShow)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "pr", "show", map[string]string{"id": args[0]}, *backendFlag)
		return reportPrTargetedOutcome(cmd, resp, dispatchErr, humanizePRShow)
	}
	return cmd
}

func newPrCategorizeCmd() *cobra.Command {
	var category string
	cmd := &cobra.Command{
		Use:   "categorize <id>",
		Short: "Set a PR's category (a plain set/overwrite; never written as a GitHub label)",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportPrTargetedOutcome(cmd, nil, err, humanizePRCategorize)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "pr", "categorize", map[string]string{
			"id":       args[0],
			"category": category,
		}, *backendFlag)
		return reportPrTargetedOutcome(cmd, resp, dispatchErr, humanizePRCategorize)
	}
	cmd.Flags().StringVar(&category, "category", "", "category to set (required); a backend's own capabilities response declares its accepted vocabulary")
	_ = cmd.MarkFlagRequired("category")
	return cmd
}

func newPrFeedbackSetCmd() *cobra.Command {
	var disposition string
	cmd := &cobra.Command{
		Use:   "feedback-set <pr-id> <comment-id>",
		Short: "Set a PR comment/review-thread entry's disposition",
		Args:  cobra.ExactArgs(2),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		d := schema.Disposition(disposition)
		if !d.IsValid() {
			return fmt.Errorf("pg-connector: --disposition %q must be one of %v", disposition, schema.ValidDispositions)
		}
		reg, err := LoadRegistry()
		if err != nil {
			return reportPrTargetedOutcome(cmd, nil, err, humanizePRFeedbackSet)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "pr", "feedback_set", map[string]string{
			"id":          args[0],
			"comment_id":  args[1],
			"disposition": string(d),
		}, *backendFlag)
		return reportPrTargetedOutcome(cmd, resp, dispatchErr, humanizePRFeedbackSet)
	}
	cmd.Flags().StringVar(&disposition, "disposition", "", "one of open|will-fix|wont-fix|no-action (required)")
	_ = cmd.MarkFlagRequired("disposition")
	return cmd
}

// prListOutcome is "pr list"'s wire response: every queried backend's
// matched PRs concatenated into Entities (backend registration order,
// bead pg2-2j5ac.28.1 design's closing paragraph — "entities
// concatenated in backend registration order"), with each backend's own
// health as one sources[] row (INV-OUT-1), mirroring ciListOutcome's
// identical shape for the sibling PR-keyed "ci list" fan-out.
type prListOutcome struct {
	Entities []schema.PR    `json:"entities"`
	Sources  []SourceResult `json:"sources"`
}

// fanOutPRList queries "list" against every backend in backends (design
// 's request shape: {"query": query, "cursor": null, "ids_only":
// idsOnly}), concatenating their matched entities and building one
// sources[] row per backend queried. A backend answering
// query_not_recognized is reported disabled with a reason distinct from
// the generic "not applicable" (list.go's classifyListSource) so the
// caller can tell "every registered backend answered
// query_not_recognized" apart from "some backends don't implement list at
// all."
func fanOutPRList(ctx context.Context, reg *Registry, backends []string, query string, idsOnly bool) prListOutcome {
	// Entities and Sources both start as non-nil empty slices so a
	// zero-backend (misconfigured host) result, or a backend that
	// answers with zero matches, still marshals entities[]/sources[] as
	// [] rather than null [bug A15's convention, applied here].
	out := prListOutcome{
		Entities: make([]schema.PR, 0),
		Sources:  make([]SourceResult, 0, len(backends)),
	}
	for _, b := range backends {
		config, err := reg.BackendConfig(b)
		if err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		resp, err := scriptout.Invoke(ctx, b, "list", map[string]any{"query": query, "cursor": nil, "ids_only": idsOnly}, config)
		if err != nil {
			out.Sources = append(out.Sources, classifyListSource(b, err))
			continue
		}
		var result schema.PRListResult
		if err := scriptout.Decode(resp.Result, &result); err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		out.Entities = append(out.Entities, result.Entities...)
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: len(result.PresentIDs)})
	}
	return out
}

func newPrListCmd() *cobra.Command {
	var query string
	var idsOnly bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List PRs matching a named query, fanned out across every registered pr backend unless --backend pins one",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered pr backend")
	cmd.Flags().StringVar(&query, "query", "", "named query to run, resolved against each backend's own config.queries (required)")
	cmd.Flags().BoolVar(&idsOnly, "ids-only", false, "return only each matched PR's id, omitting full entity detail")
	_ = cmd.MarkFlagRequired("query")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return err
		}
		backends, err := resolveListBackends(reg, "pr", *backendFlag)
		if err != nil {
			return err
		}
		outcome := fanOutPRList(cmd.Context(), reg, backends, query, idsOnly)
		if allQueryNotRecognized(outcome.Sources) {
			// design: every registered backend answered
			// query_not_recognized -> the umbrella fails the whole call
			// as invalid_argument, reported through the same synthetic
			// wire envelope every other CLI-level pre-dispatch failure
			// uses. The humanize function is never invoked on this
			// error branch (writeTargetedResult only calls it on
			// success) so a trivial no-op stub is sufficient.
			return reportPrTargetedOutcome(cmd, nil, listQueryNotRecognizedErr("pr", query), func(json.RawMessage) (string, error) { return "", nil })
		}
		return writeFanOutResult(cmd, outcome, listExitCode(outcome.Sources), func() string {
			return humanizePRListOutcome(outcome)
		})
	}
	return cmd
}

// humanizePRListOutcome formats "pr list"'s fan-out outcome for human
// display, mirroring humanizeCiList's own shape for the analogous fan-out.
func humanizePRListOutcome(o prListOutcome) string {
	var b strings.Builder
	if len(o.Entities) == 0 {
		b.WriteString("prs: (none)\n")
	} else {
		fmt.Fprintf(&b, "prs (%d):\n", len(o.Entities))
		for _, pr := range o.Entities {
			fmt.Fprintf(&b, "  [%s] %s#%d %q [%s]\n", pr.ID, pr.Repo, pr.Number, pr.Title, pr.State)
		}
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.Sources))
	return strings.TrimRight(b.String(), "\n")
}

// reportPrTargetedOutcome writes resp's outcome to stdout — in the
// default OutputJSON mode, its wire envelope ("result" on success, or
// "error" per the taxonomy on failure) verbatim, matching the wire
// protocol's own "only stdout JSON is the contract" convention; in
// OutputHuman mode, humanize's formatted rendering instead
// [bead pg2-ox1k6] — see output.go's writeTargetedResult, which this
// delegates to. It translates err into pg-connector's own targeted-op
// exit code via outcome.go's TargetedExitCode, never deciding the exit
// code itself (INV-EXIT-1). A nil resp is a Tier-1 CLI-level failure
// before any well-formed wire response was produced (e.g. no backend
// registered, or an ambiguous multi-backend registration) — rather than
// returning a plain error, writeTargetedResult now builds a synthetic
// error envelope for it via scriptout.ErrorResponse and reports it
// through stdout exactly like a backend-reported failure
// [bug pg2-njx27].
func reportPrTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	return writeTargetedResult(cmd, resp, err, humanize)
}

// humanizePRShow formats a `pr show` result (schema.PR) for human display.
func humanizePRShow(raw json.RawMessage) (string, error) {
	var pr schema.PR
	if err := scriptout.Decode(raw, &pr); err != nil {
		return "", err
	}
	return formatPR(pr), nil
}

// formatPR renders pr's identity, review/feedback state, and the two
// dedicated write fields (category, disposition) as human-readable text.
func formatPR(pr schema.PR) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PR %s: %s#%d %q [%s]\n", pr.ID, pr.Repo, pr.Number, pr.Title, pr.State)
	fmt.Fprintf(&b, "  branch: %s -> %s\n", pr.Branch, pr.Base)
	fmt.Fprintf(&b, "  author: %s\n", pr.Author)
	fmt.Fprintf(&b, "  url: %s\n", pr.URL)
	fmt.Fprintf(&b, "  draft: %t  merged: %t\n", pr.Draft, pr.Merged)
	if pr.Category != "" {
		fmt.Fprintf(&b, "  category: %s\n", pr.Category)
	}
	if len(pr.Labels) > 0 {
		fmt.Fprintf(&b, "  labels: %s\n", strings.Join(pr.Labels, ", "))
	}
	if len(pr.Comments) > 0 {
		fmt.Fprintf(&b, "  comments (%d):\n", len(pr.Comments))
		for _, c := range pr.Comments {
			fmt.Fprintf(&b, "    - [%s] %s (%s): %s\n", c.ID, c.Author, prCommentStatus(c), c.Body)
		}
	}
	if len(pr.Reviews) > 0 {
		fmt.Fprintf(&b, "  reviews (%d):\n", len(pr.Reviews))
		for _, r := range pr.Reviews {
			fmt.Fprintf(&b, "    - [%s] %s: %s\n", r.ID, r.Author, r.State)
			for _, c := range r.Comments {
				fmt.Fprintf(&b, "        - [%s] %s (%s): %s\n", c.ID, c.Author, prCommentStatus(c), c.Body)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// prCommentStatus reports a PR comment/review-thread entry's current
// disposition, or its plain resolved/open state when no disposition has
// been set yet (Disposition is only ever populated once feedback_set has
// been called on it — see schema.PRComment).
func prCommentStatus(c schema.PRComment) string {
	if c.Disposition != "" {
		return string(c.Disposition)
	}
	if c.Resolved {
		return "resolved"
	}
	return "open"
}

// humanizePRCategorize formats a `pr categorize` result
// (schema.CategorizeResult) for human display.
func humanizePRCategorize(raw json.RawMessage) (string, error) {
	var r schema.CategorizeResult
	if err := scriptout.Decode(raw, &r); err != nil {
		return "", err
	}
	return fmt.Sprintf("PR %s: category set to %q", r.ID, r.Category), nil
}

// humanizePRFeedbackSet formats a `pr feedback-set` result
// (schema.FeedbackSetResult) for human display.
func humanizePRFeedbackSet(raw json.RawMessage) (string, error) {
	var r schema.FeedbackSetResult
	if err := scriptout.Decode(raw, &r); err != nil {
		return "", err
	}
	return fmt.Sprintf("PR %s: comment %s disposition set to %q", r.ID, r.CommentID, r.Disposition), nil
}
