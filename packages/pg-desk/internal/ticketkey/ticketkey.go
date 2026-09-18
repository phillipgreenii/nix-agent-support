// Package ticketkey extracts external ticket keys (Jira and similar
// trackers) from a PR's branch name, title, and body using config-driven
// regex patterns, and answers whether a standalone id is itself shaped
// like one.
//
// This is a NEW package local to packages/pg-desk, porting
// packages/pg-pr/internal/ticketlink.Parse's exact matching algorithm
// rather than importing it: packages/pg-pr and packages/pg-desk are
// separate Go modules, and ticketlink sits under an internal/ segment
// whose visibility is scoped to importers under packages/pg-pr/... — Go's
// toolchain refuses the cross-module import outright regardless of module
// boundaries, since internal/ visibility is a directory-tree rule, not a
// module-boundary one [docket pg2-2j5ac.40's packet "pg-desk run issue for
// Jira" Contract]. Sized to just this scan; it does not duplicate
// ticketlink's full test suite, only its matching behavior.
//
// The patterns come from pg-desk's own configuration
// (config.Config.TicketPatterns) so no project-specific keys are hard-coded
// here. Both Parse and MatchesShape are pure (no I/O, no clock, no
// network) and therefore fully table-testable.
package ticketkey

import (
	"log/slog"
	"regexp"
)

// Parse searches branch, title, and body for ticket keys matching any of
// the given compiled patterns. Results are returned in encounter order
// with branch-sourced keys first, then title-sourced, then body-sourced.
// Duplicate keys (same string, regardless of source) are removed so each
// key appears at most once. A PR (or a single field, when the caller
// passes the other two as "") with no linked ticket returns nil (not an
// error).
//
// patterns is a slice of Go regular-expression strings. Invalid patterns
// are skipped (with a slog.Warn) so a mis-configured entry does not break
// the caller while still being discoverable in logs.
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

// MatchesShape reports whether id, taken as a WHOLE string, is itself
// shaped like a ticket key: an anchored, full-string match (`^<pattern>$`)
// against any of patterns — deliberately NOT Parse's own FindAllString
// substring search [Binding decisions: "an incoming id that matches any
// compiled pattern (anchored, full-string match — ^<pattern>$, not
// FindAllString's substring search) is Jira-shaped"]. Parse answers "does
// this text CONTAIN a ticket key anywhere"; MatchesShape answers "IS this
// whole string itself one." Both are driven by the identical
// config.Config.TicketPatterns list (never two independently-invented
// patterns) — cmd/pg-desk/run.go's own "issue" dispatch uses this to tell
// a Jira ticket key apart from a beads id. An empty id, or an empty/nil
// patterns list, never matches (mirrors Parse's own "no patterns -> no
// match" default).
func MatchesShape(id string, patterns []string) bool {
	if id == "" {
		return false
	}
	for _, p := range patterns {
		re, err := regexp.Compile(`^(?:` + p + `)$`)
		if err != nil {
			slog.Default().Warn("ticketkey: invalid ticket pattern skipped",
				"pattern", p, "err", err.Error())
			continue
		}
		if re.MatchString(id) {
			return true
		}
	}
	return false
}

// compilePatterns returns the subset of patterns that compile successfully.
func compilePatterns(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			// Skip invalid patterns; a mis-configured pattern should not
			// break the caller, but log a warning so it is discoverable.
			slog.Default().Warn("ticketkey: invalid ticket pattern skipped",
				"pattern", p, "err", err.Error())
			continue
		}
		out = append(out, re)
	}
	return out
}
