// issue.go: the "pg-connector issue" CLI verb group, built by the "generic
// issue entity/capability" packet on top of the Tier-1 core's
// registry/dispatcher and outcome-reporting helper, mirroring pr.go's
// identical structure. pg-connector remains the only user-facing CLI
// surface — issue is one of its verb groups, never a separate binary.
//
// Each of these four verbs is a targeted op and uses the Tier-1
// targeted-op exit-code scheme (0/4/1) via outcome.go's TargetedExitCode —
// this file calls the dispatcher and hands TargetedExitCode the raw
// per-call result/error it got back; it never decides the exit code
// itself. show/comment/transition are id-keyed and dispatch via
// dispatch.go's DispatchTargeted, which implements this docket's
// multi-instance resolution policy across every backend registered under
// connector.issue (try each in registration order, stopping at the first
// non-not_found answer). create is the one id-less write in this
// docket's scope and stays on Dispatch itself, which keeps hard-failing
// at N > 1 registered backends with no --backend pin exactly as before —
// the multi-instance resolution policy is scoped to id-keyed ops only, by
// this phase's own operator ruling.
//
// Every one of these four verbs, PLUS the new "list" verb below, carries
// its own --backend flag (bead pg2-2j5ac.28.1, design's "id-less op
// rule"): on show/create/comment/transition it PINS dispatch straight to
// that one backend (on create, this is also what design's rule means by
// "an id-less op ... MUST require it when the op cannot fan out
// meaningfully" — create's own N>1-with-no-pin hard-fail in Dispatch is
// unaffected, but a caller CAN now resolve that ambiguity by supplying
// --backend); on list it either pins the fan-out to that one backend or
// is left empty to fan out across every registered issue backend (see
// newIssueListCmd).
//
// transition's --state value is a plain string, never validated here
// against a fixed set: valid target-state values are declared per-backend
// in that backend's own capabilities response (vocabulary.state), since
// Jira/beads/GitHub Issues do not share one state vocabulary — unlike
// pr's feedback-set, which validates --disposition client-side against a
// genuinely closed cross-backend enum (schema.ValidDispositions).
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

func newIssueCmd() *cobra.Command {
	issueCmd := &cobra.Command{
		Use:   "issue",
		Short: "Issue capability commands",
	}
	issueCmd.AddCommand(newIssueShowCmd())
	issueCmd.AddCommand(newIssueCreateCmd())
	issueCmd.AddCommand(newIssueCommentCmd())
	issueCmd.AddCommand(newIssueTransitionCmd())
	issueCmd.AddCommand(newIssueListCmd())
	return issueCmd
}

func newIssueShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show an issue's current state",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportIssueTargetedOutcome(cmd, nil, err, humanizeIssueShow)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "issue", "show", map[string]string{"id": args[0]}, *backendFlag)
		return reportIssueTargetedOutcome(cmd, resp, dispatchErr, humanizeIssueShow)
	}
	return cmd
}

func newIssueCreateCmd() *cobra.Command {
	var title, priority, issueType, description string
	var labels []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new issue",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend (required when more than one is registered under connector.issue, since create cannot fan out meaningfully)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportIssueTargetedOutcome(cmd, nil, err, humanizeIssueCreate)
		}
		// create is the id-less write in this docket's scope [bead
		// pg2-2j5ac.17.2]: it stays on Dispatch, not
		// DispatchTargeted, so it keeps hard-failing at N > 1
		// registered backends with no --backend pin exactly as before
		// the multi-instance resolution policy was introduced.
		resp, dispatchErr := Dispatch(cmd.Context(), reg, "issue", "create", map[string]any{
			"title":       title,
			"priority":    priority,
			"labels":      labels,
			"issue_type":  issueType,
			"description": description,
		}, *backendFlag)
		return reportIssueTargetedOutcome(cmd, resp, dispatchErr, humanizeIssueCreate)
	}
	cmd.Flags().StringVar(&title, "title", "", "issue title (required)")
	cmd.Flags().StringVar(&priority, "priority", "", "issue priority")
	cmd.Flags().StringSliceVar(&labels, "labels", nil, "comma-separated labels")
	cmd.Flags().StringVar(&issueType, "issue-type", "", "issue type")
	// --description was added by bead pg2-akfw5 (review finding A-33: Create
	// previously had no way to set one at all).
	cmd.Flags().StringVar(&description, "description", "", "issue description")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func newIssueCommentCmd() *cobra.Command {
	var body string
	cmd := &cobra.Command{
		Use:   "comment <id>",
		Short: "Add a comment to an issue",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		humanize := func(json.RawMessage) (string, error) {
			return fmt.Sprintf("Comment added to issue %s", args[0]), nil
		}
		reg, err := LoadRegistry()
		if err != nil {
			return reportIssueTargetedOutcome(cmd, nil, err, humanize)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "issue", "comment", map[string]string{
			"id":   args[0],
			"body": body,
		}, *backendFlag)
		return reportIssueTargetedOutcome(cmd, resp, dispatchErr, humanize)
	}
	cmd.Flags().StringVar(&body, "body", "", "comment body (required)")
	_ = cmd.MarkFlagRequired("body")
	return cmd
}

func newIssueTransitionCmd() *cobra.Command {
	var state string
	cmd := &cobra.Command{
		Use:   "transition <id>",
		Short: "Transition an issue to a backend-declared target state",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		humanize := func(json.RawMessage) (string, error) {
			return fmt.Sprintf("Issue %s transitioned to %s", args[0], state), nil
		}
		reg, err := LoadRegistry()
		if err != nil {
			return reportIssueTargetedOutcome(cmd, nil, err, humanize)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "issue", "transition", map[string]string{
			"id":           args[0],
			"target_state": state,
		}, *backendFlag)
		return reportIssueTargetedOutcome(cmd, resp, dispatchErr, humanize)
	}
	cmd.Flags().StringVar(&state, "state", "", "target state (required); a backend's own capabilities response declares its accepted vocabulary")
	_ = cmd.MarkFlagRequired("state")
	return cmd
}

// issueListOutcome is "issue list"'s wire response — schema.Issue's own
// pkg/provider/issue/dispatch.go's identical shape, mirroring
// prListOutcome (pr.go) exactly; see fanOutIssueList's doc comment for
// what differs.
type issueListOutcome struct {
	Entities []schema.Issue `json:"entities"`
	Sources  []SourceResult `json:"sources"`
}

// fanOutIssueList mirrors pr.go's fanOutPRList exactly, decoding into
// schema.IssueListResult instead of schema.PRListResult.
func fanOutIssueList(ctx context.Context, reg *Registry, backends []string, query string, idsOnly bool) issueListOutcome {
	out := issueListOutcome{
		Entities: make([]schema.Issue, 0),
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
		var result schema.IssueListResult
		if err := scriptout.Decode(resp.Result, &result); err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		out.Entities = append(out.Entities, result.Entities...)
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: len(result.PresentIDs)})
	}
	return out
}

func newIssueListCmd() *cobra.Command {
	var query string
	var idsOnly bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List issues matching a named query, fanned out across every registered issue backend unless --backend pins one",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered issue backend")
	cmd.Flags().StringVar(&query, "query", "", "named query to run, resolved against each backend's own config.queries (required)")
	cmd.Flags().BoolVar(&idsOnly, "ids-only", false, "return only each matched issue's id, omitting full entity detail")
	_ = cmd.MarkFlagRequired("query")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return err
		}
		backends, err := resolveListBackends(reg, "issue", *backendFlag)
		if err != nil {
			return err
		}
		outcome := fanOutIssueList(cmd.Context(), reg, backends, query, idsOnly)
		if allQueryNotRecognized(outcome.Sources) {
			return reportIssueTargetedOutcome(cmd, nil, listQueryNotRecognizedErr("issue", query), func(json.RawMessage) (string, error) { return "", nil })
		}
		return writeFanOutResult(cmd, outcome, listExitCode(outcome.Sources), func() string {
			return humanizeIssueListOutcome(outcome)
		})
	}
	return cmd
}

// humanizeIssueListOutcome formats "issue list"'s fan-out outcome for
// human display, mirroring humanizePRListOutcome's own shape.
func humanizeIssueListOutcome(o issueListOutcome) string {
	var b strings.Builder
	if len(o.Entities) == 0 {
		b.WriteString("issues: (none)\n")
	} else {
		fmt.Fprintf(&b, "issues (%d):\n", len(o.Entities))
		for _, issue := range o.Entities {
			fmt.Fprintf(&b, "  [%s] %q [%s]\n", issue.ID, issue.Title, issue.State)
		}
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.Sources))
	return strings.TrimRight(b.String(), "\n")
}

// reportIssueTargetedOutcome writes resp's outcome to stdout — in the
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
func reportIssueTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	return writeTargetedResult(cmd, resp, err, humanize)
}

// formatIssue renders issue's identity/state (prefixed to distinguish a
// freshly created issue from one merely shown) as human-readable text.
// description/assignee/parent/deps rendering was added by bead pg2-akfw5
// (review finding A-33) alongside
// the schema.Issue fields it displays.
func formatIssue(prefix string, issue schema.Issue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %q [%s]\n", prefix, issue.ID, issue.Title, issue.State)
	if issue.Priority != "" {
		fmt.Fprintf(&b, "  priority: %s\n", issue.Priority)
	}
	if issue.IssueType != "" {
		fmt.Fprintf(&b, "  type: %s\n", issue.IssueType)
	}
	if issue.URL != "" {
		fmt.Fprintf(&b, "  url: %s\n", issue.URL)
	}
	if len(issue.Labels) > 0 {
		fmt.Fprintf(&b, "  labels: %s\n", strings.Join(issue.Labels, ", "))
	}
	if issue.Assignee != "" {
		fmt.Fprintf(&b, "  assignee: %s\n", issue.Assignee)
	}
	if issue.Parent != "" {
		fmt.Fprintf(&b, "  parent: %s\n", issue.Parent)
	}
	if len(issue.Deps) > 0 {
		deps := make([]string, len(issue.Deps))
		for i, d := range issue.Deps {
			deps[i] = fmt.Sprintf("%s (%s)", d.ID, d.Type)
		}
		fmt.Fprintf(&b, "  deps: %s\n", strings.Join(deps, ", "))
	}
	if issue.Description != "" {
		fmt.Fprintf(&b, "  description: %s\n", issue.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

// humanizeIssueShow formats an `issue show` result (schema.Issue) for
// human display.
func humanizeIssueShow(raw json.RawMessage) (string, error) {
	var issue schema.Issue
	if err := scriptout.Decode(raw, &issue); err != nil {
		return "", err
	}
	return formatIssue("issue", issue), nil
}

// humanizeIssueCreate formats an `issue create` result (schema.Issue,
// the backend-assigned identity/state of the newly created issue) for
// human display.
func humanizeIssueCreate(raw json.RawMessage) (string, error) {
	var issue schema.Issue
	if err := scriptout.Decode(raw, &issue); err != nil {
		return "", err
	}
	return formatIssue("created issue", issue), nil
}
