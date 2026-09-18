// calendar.go: the "pg-connector calendar" CLI verb group — this docket's
// ("pg-connector-calendar-osx-bridge: calendar Tier-2 backend", pg2-o2dmu)
// own pg2-o2dmu.3 packet ("calendar CLI verb group + registry wiring"),
// built on top of pkg/provider/calendar's already-landed Provider
// interface and pkg/schema/calendar.go's already-landed wire schema (both
// [landed: pg2-o2dmu.1], this docket's own first code-producing packet),
// plus the Tier-1 core's registry/dispatcher/outcome-reporting helpers
// [design: "Tier-1 contract"].
//
// connector.calendar is list-valued (multiple simultaneously-registered
// calendar backends, matching pr/issue/ci/thread's own convention — never
// scm's single-valued one) [mirrors thread's own pg2-2j5ac.40.3
// precedent, same file (registry.go), same list]. Both verbs below use
// the ordinary fan-out exit-code scheme (0/2/3) via outcome.go's
// FanOutOutcome.ExitCode — never the targeted 0/4/1 scheme — since
// connector.calendar is list-valued.
//
// "calendar list" is a fan-out-BY-PARAMETER op (a time range, never a
// named query) dispatched via the distinctly-named "list_events" wire op
// through a direct scriptout.Invoke call — STRUCTURALLY mirroring
// ci.go's fanOutCIList/newCiListCmd (this packet's own Contract), never
// the generic named-query "list" op pr/issue's own "list" verb uses. It
// deliberately has NO umbrella entity-cache fallback on
// scriptout.ErrUnavailable, unlike ci/pr/issue's own cache-backed
// fan-outs: this packet's own Files section makes no cache.go/
// cache_dispatch.go change, so a degraded backend is simply reported
// degraded, matching auth.go's/config_validate.go's plainer fan-outs.
//
// "calendar list" does NOT resolve --calendar (a human-typed display
// name) to a daemon-native calendar id itself — it passes the value
// straight through to ListEvents's own calendar parameter unchanged. That
// resolution is packet 2's own backend-internal concern (this docket's
// Contract, Binding decisions), squarely on the OTHER side of the
// Provider interface seam from this CLI layer.
//
// "calendar changes" adds NO new algorithm here at all: its own
// constructor, newCalendarChangesCmd, lives in changes.go (this file's
// sibling), as changes.go's own THIRD newChangesCmd(entityType) caller,
// reusing that file's existing delta-ledger implementation completely
// unchanged (this packet's own Contract/Binding decisions).
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

func newCalendarCmd() *cobra.Command {
	calendarCmd := &cobra.Command{
		Use:   "calendar",
		Short: "Calendar capability commands",
	}
	calendarCmd.AddCommand(newCalendarListCmd())
	calendarCmd.AddCommand(newCalendarChangesCmd())
	return calendarCmd
}

// calendarListOutcome is "calendar list"'s wire response: every queried
// calendar backend's matched events concatenated into Entities, with each
// backend's own health as one sources[] row (INV-OUT-1) — mirroring
// ciListOutcome's/prListOutcome's identical shape for their own analogous
// fan-outs. PresentIDs mirrors prListOutcome's own PresentIDs field (bug
// pg2-nc3iy's fix): schema.CalendarListResult always populates it
// regardless of Entities (pkg/schema/calendar.go's own doc comment, "the
// same generic shape ThreadListResult documents in full"), so it travels
// through here too, additive to the {entities, sources} shape every other
// list-valued fan-out already exposes.
type calendarListOutcome struct {
	Entities   []schema.CalendarEvent `json:"entities"`
	PresentIDs []string               `json:"present_ids"`
	Sources    []SourceResult         `json:"sources"`
}

// exitCode delegates to the shared fan-out scheme (0/2/3) — the exact
// same classification logic ciListOutcome.exitCode/FanOutOutcome.ExitCode
// use, applied to calendarListOutcome's own Sources rows.
func (o calendarListOutcome) exitCode() int {
	return FanOutOutcome{Sources: o.Sources}.ExitCode()
}

// fanOutCalendarList queries "list_events" against every backend in
// backends with the given [start, end) window and calendar pin (empty
// meaning "every calendar this backend is configured for," per
// Provider.ListEvents' own doc comment), concatenating their matched
// events and building one sources[] row per backend queried —
// structurally mirroring fanOutCIList (ci.go): a direct, per-backend
// scriptout.Invoke call using this fan-out's own distinctly-named wire op,
// never the generic "list" op. A backend not implementing list_events
// (recognized generically via the wire-level unknown_op sentinel,
// matching every other fan-out's own convention) is reported disabled
// with reason "not applicable" rather than a forced/meaningless answer;
// any other error is reported degraded with that error's own message —
// this fan-out has no cache-fallback path (see this file's own header
// comment for why).
func fanOutCalendarList(ctx context.Context, reg *Registry, backends []string, start, end time.Time, calendar string) calendarListOutcome {
	// Entities/PresentIDs/Sources all start as non-nil empty slices so a
	// zero-backend (misconfigured host) result, or a backend that answers
	// with zero matching events, still marshals entities[]/present_ids[]/
	// sources[] as [] rather than null [bug A15's convention, applied
	// here].
	out := calendarListOutcome{
		Entities:   make([]schema.CalendarEvent, 0),
		PresentIDs: make([]string, 0),
		Sources:    make([]SourceResult, 0, len(backends)),
	}
	// This packet's own Binding decisions: the scriptout.Invoke args map
	// MUST use exactly the start/end/calendar keys packet 1's Produces
	// pins (RFC3339 strings for start/end) — resolved ONCE here, before
	// the per-backend loop, so every backend in the fan-out sees the
	// identical resolved window and calendar pin.
	args := map[string]any{
		"start":    start.Format(time.RFC3339),
		"end":      end.Format(time.RFC3339),
		"calendar": calendar,
	}
	for _, b := range backends {
		config, err := reg.BackendConfig(b)
		if err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		resp, err := scriptout.Invoke(ctx, b, "list_events", args, config)
		if err != nil {
			if errors.Is(err, scriptout.ErrUnknownOp) {
				out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDisabled, Reason: "not applicable"})
				continue
			}
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		var result schema.CalendarListResult
		if err := scriptout.Decode(resp.Result, &result); err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		out.Entities = append(out.Entities, result.Entities...)
		out.PresentIDs = append(out.PresentIDs, result.PresentIDs...)
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: len(result.Entities)})
	}
	return out
}

// resolveCalendarListWindow resolves "calendar list"'s own [start, end)
// time-range arguments, once, before any backend is dispatched: an
// explicit --start/--end is parsed as RFC3339 (the exact wire-envelope arg
// format list_events' own dispatch-table entry parses, so a malformed
// value is caught here with the same strictness rather than surfacing as
// a less legible per-backend invalid_argument later); an unset flag
// defaults to "today" [design: "Defaults to 'today' when unset, matching
// the daily-focus use case"].
//
// Binding decision (this packet's own freedom boundary — the design does
// not pin exact boundary semantics further): "today" is midnight-to-
// midnight LOCAL time — the start of today through the start of tomorrow,
// half-open, mirroring Provider.ListEvents' own "[start, end)" contract —
// rather than a rolling 24h-from-now window.
func resolveCalendarListWindow(startFlag, endFlag string) (time.Time, time.Time, error) {
	now := time.Now()
	year, month, day := now.Date()
	startOfToday := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
	startOfTomorrow := startOfToday.AddDate(0, 0, 1)

	start := startOfToday
	if startFlag != "" {
		parsed, err := time.Parse(time.RFC3339, startFlag)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("pg-connector: --start %q: not a valid RFC3339 timestamp: %w", startFlag, err)
		}
		start = parsed
	}
	end := startOfTomorrow
	if endFlag != "" {
		parsed, err := time.Parse(time.RFC3339, endFlag)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("pg-connector: --end %q: not a valid RFC3339 timestamp: %w", endFlag, err)
		}
		end = parsed
	}
	return start, end, nil
}

func newCalendarListCmd() *cobra.Command {
	var startFlag, endFlag, calendarFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List calendar events in a time range, fanned out across every registered calendar backend",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered calendar backend")
	cmd.Flags().StringVar(&startFlag, "start", "", "RFC3339 start of the time range (inclusive); defaults to the start of today, local time")
	cmd.Flags().StringVar(&endFlag, "end", "", "RFC3339 end of the time range (exclusive); defaults to the start of tomorrow, local time")
	cmd.Flags().StringVar(&calendarFlag, "calendar", "", "pin to exactly this configured calendar name, passed through unchanged to each backend's own ListEvents call (never resolved to a daemon id at this layer); empty means every calendar a backend is configured for")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		start, end, err := resolveCalendarListWindow(startFlag, endFlag)
		if err != nil {
			return err
		}
		reg, err := LoadRegistry()
		if err != nil {
			return err
		}
		backends, err := resolveListBackends(reg, "calendar", *backendFlag)
		if err != nil {
			return err
		}
		outcome := fanOutCalendarList(cmd.Context(), reg, backends, start, end, calendarFlag)
		return writeFanOutResult(cmd, outcome, outcome.exitCode(), func() string {
			return humanizeCalendarListOutcome(outcome)
		})
	}
	return cmd
}

// humanizeCalendarListOutcome formats "calendar list"'s fan-out outcome
// for human display, mirroring humanizePRListOutcome's/humanizeCiList's
// own shape for their analogous fan-outs — this packet's own Contract
// names pr.go's humanizePRShow/formatPR-style helpers as this function's
// structural template.
func humanizeCalendarListOutcome(o calendarListOutcome) string {
	var b strings.Builder
	switch {
	case len(o.Entities) > 0:
		fmt.Fprintf(&b, "calendar events (%d):\n", len(o.Entities))
		for _, e := range o.Entities {
			fmt.Fprintf(&b, "  [%s] %q %s -> %s (calendar=%s)\n", e.ID, e.Title, e.Start, e.End, e.Calendar)
		}
	case len(o.PresentIDs) > 0:
		fmt.Fprintf(&b, "calendar events (%d, ids only):\n", len(o.PresentIDs))
		for _, id := range o.PresentIDs {
			fmt.Fprintf(&b, "  %s\n", id)
		}
	default:
		b.WriteString("calendar events: (none)\n")
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.Sources))
	return strings.TrimRight(b.String(), "\n")
}
