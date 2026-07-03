// Package ticketlink extracts external ticket keys from a PR's branch name,
// title, and body using config-driven regex patterns.
//
// The patterns come from the pg-pr configuration (RepoConfig.TicketPatterns)
// so no project-specific keys are hard-coded in this package. The generic
// default pattern ([A-Z]+-\d+) matches common issue-tracker formats (Jira,
// Linear, etc.) and is applied if the caller passes it; nothing is baked in
// here.
//
// Parse is pure (no I/O, no clock, no network) and therefore fully
// table-testable.
package ticketlink

import (
	"log/slog"
	"regexp"
)

// Parse searches branch, title, and body for ticket keys matching any of the
// given compiled patterns. Results are returned in encounter order with
// branch-sourced keys first, then title-sourced, then body-sourced. Duplicate
// keys (same string, regardless of source) are removed so each key appears at
// most once. A PR with no linked ticket returns nil (not an error).
//
// patterns is a slice of Go regular-expression strings. Invalid patterns are
// skipped (with a slog.Warn) so a mis-configured entry does not break the
// sync loop while still being discoverable in logs.
func Parse(branch, title, body string, patterns []string) []string {
	compiled := compilePatterns(patterns)
	if len(compiled) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var keys []string

	for _, src := range []string{branch, title, body} {
		for _, re := range compiled {
			matches := re.FindAllString(src, -1)
			for _, m := range matches {
				if _, dup := seen[m]; !dup {
					seen[m] = struct{}{}
					keys = append(keys, m)
				}
			}
		}
	}

	if len(keys) == 0 {
		return nil
	}
	return keys
}

// compilePatterns returns the subset of patterns that compile successfully.
func compilePatterns(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			// Skip invalid patterns; a mis-configured pattern should not break
			// the sync loop, but log a warning so it is discoverable.
			slog.Default().Warn("ticketlink: invalid ticket pattern skipped",
				"pattern", p, "err", err.Error())
			continue
		}
		out = append(out, re)
	}
	return out
}
