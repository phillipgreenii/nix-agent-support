// activity.go: the "pg-connector activity list" Tier-1-only CLI verb.
//
// activity.sources is a top-level, always-list-valued registration
// independent of connector.<type> (Registry.ActivitySources), so this is a
// FAN-OUT op like "attention list"/"ci list": it queries every registered
// activity.sources backend (or the one --backend pins), with no targeted-op
// form. Unlike attention, the per-source results are NOT merged: the items
// of every succeeding source are concatenated in config order, each wrapped
// with its source, with no dedup, no cap and no cross-source sorting.
//
// The op is range-shaped and stateless: list_activity takes
// {"since": <RFC3339, optional>, "before": <RFC3339, required>} in its op
// ARGS (never the config channel, unlike list/search's list_since keys), and
// answers {"items": [...], "truncated": <bool>}. There is no cursor, no
// ledger and no cache entry. The --since/--before flag plumbing and the one
// time-bound syntax are the shared helpers in timebound.go.
//
// A backend not implementing list_activity (the wire-level unknown_op
// sentinel) is reported disabled/"not applicable", mirroring
// attention.go/auth.go/ci.go's own convention for the same case.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// activitySourceRow is one row of "activity list"'s sources[] array. It is
// verb-local rather than the shared SourceResult because it carries a
// per-source truncated that MUST serialize as false when false (the shared
// row has no such field). Count is that source's raw item count.
type activitySourceRow struct {
	Source    string       `json:"source"`
	Status    SourceStatus `json:"status"`
	Count     int          `json:"count"`
	Truncated bool         `json:"truncated"`
	Reason    string       `json:"reason,omitempty"`
}

// activityRow is one concatenated item: the unmodified schema.ActivityItem
// plus the backend that emitted it.
type activityRow struct {
	Source string              `json:"source"`
	Item   schema.ActivityItem `json:"item"`
}

// ActivityOutcome is "activity list"'s wire response.
type ActivityOutcome struct {
	Sources []activitySourceRow `json:"sources"`
	Items   []activityRow       `json:"items"`
}

// fanOutOutcome projects the rows onto the shared fan-out envelope so
// FanOutOutcome.ExitCode() stays the one exit-code scheme (disabled counts
// as healthy, degraded as failed, zero sources as total failure); truncated
// is deliberately not part of the projection, so it never reaches the exit
// code.
func (o ActivityOutcome) fanOutOutcome() FanOutOutcome {
	out := FanOutOutcome{Sources: make([]SourceResult, 0, len(o.Sources))}
	for _, s := range o.Sources {
		out.Sources = append(out.Sources, SourceResult{Source: s.Source, Status: s.Status, Count: s.Count, Reason: s.Reason})
	}
	return out
}

// ExitCode returns the fan-out scheme's 0/2/3.
func (o ActivityOutcome) ExitCode() int { return o.fanOutOutcome().ExitCode() }

// resolveActivityBackends returns the sources an "activity list" call
// queries: every registered activity source in config order, or just the one
// pinned by --backend (an unregistered pin is an error, mirroring
// resolveListBackends).
func resolveActivityBackends(reg *Registry, pinned string) ([]string, error) {
	backends, err := reg.ActivitySources()
	if err != nil {
		return nil, err
	}
	if pinned == "" {
		return backends, nil
	}
	for _, b := range backends {
		if b == pinned {
			return []string{b}, nil
		}
	}
	return nil, fmt.Errorf("dispatch: --backend %q is not registered for activity.sources (registered: %v)", pinned, backends)
}

// fanOutActivityList sends list_activity (range in the op args, static
// backends.<name> block as config) to every backend in backends, in order,
// building one sources[] row per backend and concatenating each succeeding
// backend's items.
func fanOutActivityList(ctx context.Context, reg *Registry, backends []string, args schema.ActivityListArgs) ActivityOutcome {
	// Both slices start non-nil so a zero-source result still marshals
	// sources/items as [] rather than null.
	out := ActivityOutcome{
		Sources: make([]activitySourceRow, 0, len(backends)),
		Items:   make([]activityRow, 0),
	}
	for _, b := range backends {
		config, err := reg.BackendConfig(b)
		if err != nil {
			out.Sources = append(out.Sources, activitySourceRow{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		resp, err := reg.Invoke(ctx, b, "list_activity", args, config)
		if err != nil {
			if errors.Is(err, scriptout.ErrUnknownOp) {
				out.Sources = append(out.Sources, activitySourceRow{Source: b, Status: SourceDisabled, Reason: "not applicable"})
				continue
			}
			out.Sources = append(out.Sources, activitySourceRow{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		var result schema.ActivityListResult
		if err := scriptout.Decode(resp.Result, &result); err != nil {
			out.Sources = append(out.Sources, activitySourceRow{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		for _, it := range result.Items {
			out.Items = append(out.Items, activityRow{Source: b, Item: it})
		}
		out.Sources = append(out.Sources, activitySourceRow{Source: b, Status: SourceSucceeded, Count: len(result.Items), Truncated: result.Truncated})
	}
	return out
}

// activityTimeBoundAsymmetryHelp documents how "activity list" delivers its
// bounds, in the shared flag help's asymmetry slot.
const activityTimeBoundAsymmetryHelp = "Delivered to each backend in the list_activity request args (since inclusive, before exclusive), never in its config. Defaults: --before is now; an omitted --since leaves the range open at the start."

func newActivityCmd() *cobra.Command {
	activityCmd := &cobra.Command{
		Use:   "activity",
		Short: "Activity capability commands",
	}
	activityCmd.AddCommand(newActivityListCmd())
	return activityCmd
}

func newActivityListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Fan list_activity out across every registered activity.sources backend over a time range, concatenated in config order",
		Long: "List what the operator did in a time range, fanned out across every registered activity.sources backend (or the one --backend pins).\n\n" +
			"--since and --before each accept " + timeBoundSyntaxHelp + ". --since is inclusive and --before is exclusive; " +
			"--before defaults to now, and an omitted --since leaves the range open at the start. --since must be earlier than the effective --before.\n\n" +
			"Items are concatenated in config order, each carrying its source; nothing is merged, deduped, capped or re-sorted. " +
			"A source whose result was capped reports truncated: true on its sources[] row (a warning, never an exit code). " +
			"Exit codes: 0 all sources healthy, 2 at least one degraded, 3 no healthy source.",
		Args: cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this activity source instead of every registered one")
	bounds := addTimeBoundFlags(cmd, "activity occurring", activityTimeBoundAsymmetryHelp)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		now := time.Now()
		rng, err := bounds.resolveInvalidArgument(now)
		if err == nil && rng.Before.IsZero() {
			rng.Before = now.UTC()
			if !rng.Since.IsZero() && !rng.Since.Before(rng.Before) {
				err = scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf(
					"--%s (%s) must be earlier than --%s (%s, now by default)",
					sinceFlagName, rng.Since.Format(time.RFC3339), beforeFlagName, rng.Before.Format(time.RFC3339),
				))
			}
		}
		if err != nil {
			return writeTargetedResult(cmd, nil, err, nil)
		}
		reg, err := LoadRegistry()
		if err != nil {
			return err
		}
		backends, err := resolveActivityBackends(reg, *backendFlag)
		if err != nil {
			return err
		}
		callArgs := schema.ActivityListArgs{Before: rng.Before.UTC().Format(time.RFC3339)}
		if !rng.Since.IsZero() {
			callArgs.Since = rng.Since.UTC().Format(time.RFC3339)
		}
		outcome := fanOutActivityList(cmd.Context(), reg, backends, callArgs)
		return writeFanOutResult(cmd, outcome, outcome.ExitCode(), func() string {
			return humanizeActivityList(outcome)
		})
	}
	return cmd
}

// humanizeActivityList formats an "activity list" outcome for human display,
// mirroring humanizeAttentionList's shape.
func humanizeActivityList(o ActivityOutcome) string {
	var b strings.Builder
	if len(o.Items) == 0 {
		b.WriteString("activity: (none)\n")
	} else {
		fmt.Fprintf(&b, "activity (%d):\n", len(o.Items))
		for _, r := range o.Items {
			fmt.Fprintf(&b, "  %s [%s] %s: %s (%s)\n", r.Item.OccurredAt, r.Item.Kind, r.Item.EntityID, r.Item.Summary, r.Source)
		}
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.fanOutOutcome().Sources))
	for _, s := range o.Sources {
		if s.Truncated {
			fmt.Fprintf(&b, "\n  (%s: truncated, the range is incompletely covered)", s.Source)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
