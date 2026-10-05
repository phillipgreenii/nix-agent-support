// timebound.go: the umbrella's ONE time-bound syntax and the --since/
// --before flag plumbing shared by "<type> list", "search" and (rejected on)
// "<type> changes" (bead pg2-ttk9t; work-tracker design WT-D18 and its
// section 4.4 time-bound paragraph; search flavor from the 2026-09-18
// search time-bound design).
//
// parseTimeBound accepts exactly three forms:
//
//   - an RFC3339 timestamp ("2026-10-01T00:00:00Z");
//   - a Go duration ("168h", "90m") meaning "this long ago";
//   - a whole-day duration "<N>d" ("7d", "0d") meaning "this long ago".
//     time.ParseDuration rejects "7d", so the day suffix is parsed here,
//     modeled on parseDayDuration (ledger.go).
//
// Durations are subtracted from now as a fixed N*24h, never via calendar
// arithmetic, so a DST transition inside the window cannot shift a bound.
// A negative duration or anything else is an error: a bound in the future
// is never what a caller of a "last-updated" filter means.
//
// Phase 1's "activity list" consumes this same helper.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// maxBoundDays caps "<N>d" so N*24h cannot overflow time.Duration
// (math.MaxInt64 ns is roughly 106751 days).
const maxBoundDays = 100000

// parseTimeBound parses one --since/--before value against now. The result
// is always UTC.
func parseTimeBound(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("invalid time bound: empty value")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if strings.HasSuffix(s, "d") {
		digits := strings.TrimSuffix(s, "d")
		if !isAllDigits(digits) {
			return time.Time{}, errBadBound(s)
		}
		n, err := strconv.Atoi(digits)
		if err != nil || n > maxBoundDays {
			return time.Time{}, errBadBound(s)
		}
		return now.Add(-time.Duration(n) * 24 * time.Hour).UTC(), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return time.Time{}, errBadBound(s)
	}
	return now.Add(-d).UTC(), nil
}

func errBadBound(s string) error {
	return fmt.Errorf("invalid time bound %q: want an RFC3339 timestamp, a non-negative Go duration (e.g. \"168h\"), or whole days (e.g. \"7d\")", s)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// timeBoundFlags holds the raw --since/--before flag values of one command.
type timeBoundFlags struct {
	since, before string
}

const (
	sinceFlagName  = "since"
	beforeFlagName = "before"
)

// listTimeBoundAsymmetryHelp is appended to list's --since/--before help: the
// bound travels as list_since/list_before in the backend's request config and
// only a backend that reads those keys applies it (WT-D18).
const listTimeBoundAsymmetryHelp = "The bound applies to each entity's last-updated time and is delivered to backends as list_since/list_before in the request config; a backend that does not read those keys returns its unbounded result."

// searchTimeBoundAsymmetryHelp is the search flavor (search_since/search_before).
const searchTimeBoundAsymmetryHelp = "Delivered to backends as search_since/search_before in the request config; a backend that does not read those keys (e.g. a GitHub or Jira backend, whose query text can carry its own updated:/JQL qualifier) returns its unbounded result."

const timeBoundSyntaxHelp = "an RFC3339 timestamp, a Go duration (168h), or whole days (7d), the latter two meaning \"this long ago\""

// addTimeBoundFlags registers --since/--before on cmd. what describes what
// the bound applies to and asymmetry documents the freedom boundary.
func addTimeBoundFlags(cmd *cobra.Command, what, asymmetry string) *timeBoundFlags {
	f := &timeBoundFlags{}
	cmd.Flags().StringVar(&f.since, sinceFlagName, "", "only "+what+" at or after this bound ("+timeBoundSyntaxHelp+"). "+asymmetry)
	cmd.Flags().StringVar(&f.before, beforeFlagName, "", "only "+what+" strictly before this bound (same syntax as --"+sinceFlagName+"). "+asymmetry)
	return f
}

// resolve parses the flag values against now. An absent flag leaves that
// side of the range open. since must precede before when both are given.
func (f *timeBoundFlags) resolve(now time.Time) (scriptout.TimeRange, error) {
	var r scriptout.TimeRange
	var err error
	if f.since != "" {
		if r.Since, err = parseTimeBound(f.since, now); err != nil {
			return scriptout.TimeRange{}, fmt.Errorf("--%s: %w", sinceFlagName, err)
		}
	}
	if f.before != "" {
		if r.Before, err = parseTimeBound(f.before, now); err != nil {
			return scriptout.TimeRange{}, fmt.Errorf("--%s: %w", beforeFlagName, err)
		}
	}
	if !r.Since.IsZero() && !r.Before.IsZero() && !r.Since.Before(r.Before) {
		return scriptout.TimeRange{}, fmt.Errorf("--%s (%s) must be earlier than --%s (%s)",
			sinceFlagName, r.Since.Format(time.RFC3339), beforeFlagName, r.Before.Format(time.RFC3339))
	}
	return r, nil
}

// resolveInvalidArgument is resolve with any failure wrapped as the wire
// taxonomy's invalid_argument, ready for the CLI-level failure path.
func (f *timeBoundFlags) resolveInvalidArgument(now time.Time) (scriptout.TimeRange, error) {
	r, err := f.resolve(now)
	if err != nil {
		return scriptout.TimeRange{}, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	return r, nil
}

// listBackendConfig returns the config the umbrella sends to backend b for a
// "list" call: its static backends.<b> block, with list_since/list_before
// merged on ONLY when the caller bounded the call (an unbounded call gets
// the static block verbatim, byte-identical to before this feature).
func listBackendConfig(reg *Registry, b string, r scriptout.TimeRange) (json.RawMessage, error) {
	config, err := reg.BackendConfig(b)
	if err != nil {
		return nil, err
	}
	return r.MergeInto(config, scriptout.ConfigKeyListSince, scriptout.ConfigKeyListBefore)
}

// searchBackendConfig is listBackendConfig for the "search" op.
func searchBackendConfig(reg *Registry, b string, r scriptout.TimeRange) (json.RawMessage, error) {
	config, err := reg.BackendConfig(b)
	if err != nil {
		return nil, err
	}
	return r.MergeInto(config, scriptout.ConfigKeySearchSince, scriptout.ConfigKeySearchBefore)
}

// rejectChangesTimeBounds is the "changes --since/--before" refusal. changes
// NEVER passes a range: the ledger derives removals from present_ids, so a
// bounded present_ids would tombstone every entity merely older than the
// window (WT-D18). The flags are registered (hidden) only so the refusal is
// a proper invalid_argument rather than cobra's generic unknown-flag error.
func rejectChangesTimeBounds(cmd *cobra.Command) error {
	for _, name := range []string{sinceFlagName, beforeFlagName} {
		if cmd.Flags().Changed(name) {
			return scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf(
				"changes does not accept --%s: a time-bounded present_ids would tombstone every entity older than the window; use \"list --%s\" for a ranged read", name, name,
			))
		}
	}
	return nil
}
