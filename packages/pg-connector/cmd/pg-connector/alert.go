// alert.go: the "pg-connector alert" CLI verb group (list / show / history)
// — the Facade over N alert backends (ADR 0062 Decision item 9).
//
// connector.alert is list-valued (several alert backends may be registered at
// once, like pr/issue/ci/thread/calendar). "list" and "history" are fan-outs
// using the ordinary 0/2/3 exit scheme; "show" is a targeted op.
//
// Alerts are deliberately NOT cache-backed (INV-ALERT-5): a cached alert list
// would render a stale "all clear" or stale firing set as current, so there
// is no umbrella cache fallback on scriptout.ErrUnavailable. A degraded
// backend is reported degraded in sources[] and a consumer derives "unknown"
// from that row — never from an empty entities array. There is also no
// "changes" verb in v1 (it can be added later with newChangesCmd("alert")).
//
// "alert list --query" is OPTIONAL: omitted means each backend's entire
// firing set, unfiltered. The umbrella never interprets alert fields or query
// text (INV-CAP-1); it only fans out and concatenates.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

func newAlertCmd() *cobra.Command {
	alertCmd := &cobra.Command{
		Use:   "alert",
		Short: "Alert capability commands (read-only): currently-firing alerts and their history",
	}
	alertCmd.AddCommand(newAlertListCmd())
	alertCmd.AddCommand(newAlertShowCmd())
	alertCmd.AddCommand(newAlertHistoryCmd())
	return alertCmd
}

// alertListOutcome is "alert list"'s wire response: every queried backend's
// firing alerts concatenated into Entities, plus one sources[] row per
// backend (INV-OUT-1).
type alertListOutcome struct {
	Entities   []schema.Alert `json:"entities"`
	PresentIDs []string       `json:"present_ids"`
	Sources    []SourceResult `json:"sources"`
}

func fanOutAlertList(ctx context.Context, reg *Registry, backends []string, query string, idsOnly bool) alertListOutcome {
	out := alertListOutcome{
		Entities:   make([]schema.Alert, 0),
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
		var result schema.AlertListResult
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

func newAlertListCmd() *cobra.Command {
	var query string
	var idsOnly bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List currently-firing alerts, fanned out across every registered alert backend unless --backend pins one",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered alert backend")
	cmd.Flags().StringVar(&query, "query", "", "named query to run, resolved against each backend's own config.queries; omitted means each backend's entire firing set")
	cmd.Flags().BoolVar(&idsOnly, "ids-only", false, "return only each matched alert's id, omitting full entity detail")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return writeTargetedResult(cmd, nil, err, humanizeAlertNoop)
		}
		backends, err := resolveListBackends(reg, "alert", *backendFlag)
		if err != nil {
			return writeTargetedResult(cmd, nil, err, humanizeAlertNoop)
		}
		outcome := fanOutAlertList(cmd.Context(), reg, backends, query, idsOnly)
		if query != "" && allQueryNotRecognized(outcome.Sources) {
			return writeTargetedResult(cmd, nil, listQueryNotRecognizedErr("alert", query), humanizeAlertNoop)
		}
		return writeFanOutResult(cmd, outcome, listExitCode(outcome.Sources), func() string {
			return humanizeAlertListOutcome(outcome)
		})
	}
	return cmd
}

func newAlertShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a currently-firing alert (not_found when the id is not currently firing)",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return writeTargetedResult(cmd, nil, err, humanizeAlertShow)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "alert", "show", map[string]string{"id": args[0]}, *backendFlag)
		return writeTargetedResult(cmd, resp, dispatchErr, humanizeAlertShow)
	}
	return cmd
}

// alertHistoryOutcome is "alert history"'s wire response: every queried
// backend's episodes concatenated (concatenate merge strategy, as ci list
// does for runs), one sources[] row per backend.
type alertHistoryOutcome struct {
	Episodes  []schema.AlertEpisode `json:"episodes"`
	Truncated bool                  `json:"truncated"`
	Sources   []SourceResult        `json:"sources"`
}

func fanOutAlertHistory(ctx context.Context, reg *Registry, backends []string, since, until time.Time, query string) alertHistoryOutcome {
	out := alertHistoryOutcome{
		Episodes: make([]schema.AlertEpisode, 0),
		Sources:  make([]SourceResult, 0, len(backends)),
	}
	args := map[string]any{
		"since": since.Format(time.RFC3339),
		"until": until.Format(time.RFC3339),
		"query": query,
	}
	for _, b := range backends {
		config, err := reg.BackendConfig(b)
		if err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		resp, err := scriptout.Invoke(ctx, b, "list_history", args, config)
		if err != nil {
			if errors.Is(err, scriptout.ErrUnknownOp) {
				out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDisabled, Reason: "not applicable"})
				continue
			}
			out.Sources = append(out.Sources, classifyListSource(b, err))
			continue
		}
		var result schema.AlertHistoryResult
		if err := scriptout.Decode(resp.Result, &result); err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		out.Episodes = append(out.Episodes, result.Episodes...)
		out.Truncated = out.Truncated || result.Truncated
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: len(result.Episodes)})
	}
	return out
}

func newAlertHistoryCmd() *cobra.Command {
	var sinceFlag, untilFlag, query string
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List alert firing episodes in a time window, fanned out across every registered alert backend",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered alert backend")
	cmd.Flags().StringVar(&sinceFlag, "since", "", "RFC3339 start of the window (required)")
	cmd.Flags().StringVar(&untilFlag, "until", "", "RFC3339 end of the window (required)")
	cmd.Flags().StringVar(&query, "query", "", "optional named query, resolved against each backend's own config.queries")
	_ = cmd.MarkFlagRequired("since")
	_ = cmd.MarkFlagRequired("until")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		since, err := time.Parse(time.RFC3339, sinceFlag)
		if err != nil {
			return fmt.Errorf("pg-connector: --since %q: not a valid RFC3339 timestamp: %w", sinceFlag, err)
		}
		until, err := time.Parse(time.RFC3339, untilFlag)
		if err != nil {
			return fmt.Errorf("pg-connector: --until %q: not a valid RFC3339 timestamp: %w", untilFlag, err)
		}
		reg, err := LoadRegistry()
		if err != nil {
			return writeTargetedResult(cmd, nil, err, humanizeAlertNoop)
		}
		backends, err := resolveListBackends(reg, "alert", *backendFlag)
		if err != nil {
			return writeTargetedResult(cmd, nil, err, humanizeAlertNoop)
		}
		outcome := fanOutAlertHistory(cmd.Context(), reg, backends, since, until, query)
		if query != "" && allQueryNotRecognized(outcome.Sources) {
			return writeTargetedResult(cmd, nil, listQueryNotRecognizedErr("alert", query), humanizeAlertNoop)
		}
		return writeFanOutResult(cmd, outcome, listExitCode(outcome.Sources), func() string {
			return humanizeAlertHistoryOutcome(outcome)
		})
	}
	return cmd
}

func humanizeAlertNoop(json.RawMessage) (string, error) { return "", nil }

func humanizeAlertListOutcome(o alertListOutcome) string {
	var b strings.Builder
	switch {
	case len(o.Entities) > 0:
		fmt.Fprintf(&b, "alerts (%d):\n", len(o.Entities))
		for _, a := range o.Entities {
			sev := string(a.Severity)
			if sev == "" {
				sev = "-"
			}
			ack := ""
			if a.Acknowledged != nil && *a.Acknowledged {
				ack = " (acknowledged)"
			}
			fmt.Fprintf(&b, "  [%s] %s severity=%s since=%s%s\n", a.ID, a.Title, sev, a.Since, ack)
		}
	case len(o.PresentIDs) > 0:
		fmt.Fprintf(&b, "alerts (%d, ids only):\n", len(o.PresentIDs))
		for _, id := range o.PresentIDs {
			fmt.Fprintf(&b, "  %s\n", id)
		}
	default:
		b.WriteString("alerts: (none)\n")
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.Sources))
	return strings.TrimRight(b.String(), "\n")
}

func humanizeAlertHistoryOutcome(o alertHistoryOutcome) string {
	var b strings.Builder
	if len(o.Episodes) > 0 {
		fmt.Fprintf(&b, "alert episodes (%d):\n", len(o.Episodes))
		for _, e := range o.Episodes {
			end := e.EndedAt
			if end == "" {
				end = "(still firing)"
			}
			fmt.Fprintf(&b, "  [rule=%s] %s %s -> %s\n", e.RuleID, e.Title, e.StartedAt, end)
		}
	} else {
		b.WriteString("alert episodes: (none)\n")
	}
	if o.Truncated {
		b.WriteString("(truncated)\n")
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.Sources))
	return strings.TrimRight(b.String(), "\n")
}

func humanizeAlertShow(raw json.RawMessage) (string, error) {
	var a schema.Alert
	if err := scriptout.Decode(raw, &a); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "alert %s [%s]\n", a.ID, a.Provider)
	fmt.Fprintf(&b, "  title: %s\n", a.Title)
	if a.Description != "" {
		fmt.Fprintf(&b, "  description: %s\n", a.Description)
	}
	if a.Severity != "" {
		fmt.Fprintf(&b, "  severity: %s\n", a.Severity)
	}
	if a.Acknowledged != nil {
		fmt.Fprintf(&b, "  acknowledged: %t\n", *a.Acknowledged)
	}
	fmt.Fprintf(&b, "  since: %s\n", a.Since)
	if a.URL != "" {
		fmt.Fprintf(&b, "  url: %s\n", a.URL)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
