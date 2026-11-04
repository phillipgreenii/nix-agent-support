// search.go: the `search` subcommand — asks the recorder's search for matches,
// optionally for one session and bounded by --since/--before. It reads no
// transcript file.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/recorderfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

type searchMatchJSON struct {
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Line      int    `json:"line"`
	Snippet   string `json:"snippet"`
	Timestamp string `json:"timestamp,omitempty"`
}

type searchJSONDoc struct {
	Query   string            `json:"query"`
	Matches []searchMatchJSON `json:"matches"`
}

// maxBoundDays caps "<N>d" so N*24h cannot overflow time.Duration
// (math.MaxInt64 ns is roughly 106751 days). It matches the cap of
// pg-connector's parseTimeBound so one bound syntax holds across both tools.
const maxBoundDays = 100000

// parseTimeBound parses a --since/--before value as one of three forms:
//
//   - a time.ParseDuration string ("24h", "90m"), interpreted as "this long
//     ago" relative to now — mirrors this repo's existing
//     attention.perBackend.threshold convention;
//   - a whole-day duration "<N>d" ("7d", "0d"), N one or more ASCII digits
//     (at most maxBoundDays), also "this long ago". time.ParseDuration
//     rejects "7d", so the suffix is parsed here. The offset is a fixed
//     N*24h, never calendar arithmetic, so a DST transition inside the
//     window cannot shift the bound;
//   - an absolute RFC3339 timestamp.
//
// The forms never collide syntactically (a duration string has no
// "-"/":"/"T" punctuation and an RFC3339 timestamp never ends in a lowercase
// "d"), so duration is tried first with no ambiguity.
//
// Accepting a negative Go duration ("-1h", a bound in the future) is a
// pa-monitor-only legacy form that pg-connector's umbrella parser rejects;
// it is kept here unchanged so every input accepted before "<N>d" was added
// keeps its result.
func parseTimeBound(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if digits, ok := strings.CutSuffix(s, "d"); ok && isAllDigits(digits) {
		n, err := strconv.Atoi(digits)
		if err != nil || n > maxBoundDays {
			return time.Time{}, errBadTimeBound(s)
		}
		return now.Add(-time.Duration(n) * 24 * time.Hour), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errBadTimeBound(s)
	}
	return t, nil
}

func errBadTimeBound(s string) error {
	return fmt.Errorf("invalid time bound %q: not a duration (e.g. \"24h\"), whole days (e.g. \"7d\", at most %d) or RFC3339 timestamp", s, maxBoundDays)
}

// isAllDigits reports whether s is non-empty and entirely ASCII digits.
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

// parseSearchArgs parses `search`'s own argv: exactly one positional query
// token, an optional "--json" flag (accepted but not required — search has
// no text-output mode, so this flag is a no-op kept only for command-line
// symmetry with status/info), and optional "--session <id>"/"--since
// <bound>"/"--before <bound>" value flags — any of these MAY appear in any
// order. now is injected for testability; production callers pass
// time.Now().UTC().
func parseSearchArgs(args []string, now time.Time) (query, sessionID string, since, before time.Time, substring bool, err error) {
	rest, _ := stripJSONFlag(args)
	var positional []string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--substring":
			substring = true
		case "--session":
			if i+1 >= len(rest) {
				return "", "", time.Time{}, time.Time{}, false, errors.New("search: --session requires a value")
			}
			sessionID = rest[i+1]
			i++
		case "--since":
			if i+1 >= len(rest) {
				return "", "", time.Time{}, time.Time{}, false, errors.New("search: --since requires a value")
			}
			since, err = parseTimeBound(rest[i+1], now)
			if err != nil {
				return "", "", time.Time{}, time.Time{}, false, fmt.Errorf("search: --since: %w", err)
			}
			i++
		case "--before":
			if i+1 >= len(rest) {
				return "", "", time.Time{}, time.Time{}, false, errors.New("search: --before requires a value")
			}
			before, err = parseTimeBound(rest[i+1], now)
			if err != nil {
				return "", "", time.Time{}, time.Time{}, false, fmt.Errorf("search: --before: %w", err)
			}
			i++
		default:
			positional = append(positional, rest[i])
		}
	}
	if len(positional) != 1 {
		return "", "", time.Time{}, time.Time{}, false, fmt.Errorf("search: expected exactly one query argument, got %d", len(positional))
	}
	return positional[0], sessionID, since, before, substring, nil
}

// searcher is where matches come from: the recorder's search
// (recorderfacts.Facts), never the transcript files.
type searcher interface {
	Search(ctx context.Context, query, sessionID string, since, before time.Time, substring bool) ([]recorderfacts.SearchHit, error)
}

// searchSessions searches for query (in sessionID only, when non-empty) within
// [since, before) (either may be the zero time, meaning unbounded) through
// srch and writes the result as JSON to w.
func searchSessions(w io.Writer, srch searcher, query, sessionID string, since, before time.Time, substring bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	hits, err := srch.Search(ctx, query, sessionID, since, before, substring)
	if err != nil {
		return err
	}
	doc := searchJSONDoc{Query: query, Matches: []searchMatchJSON{}}
	for _, h := range hits {
		mj := searchMatchJSON{SessionID: h.SessionID, Role: h.Role, Snippet: h.Snippet}
		if !h.Time.IsZero() {
			mj.Timestamp = h.Time.UTC().Format(time.RFC3339)
		}
		doc.Matches = append(doc.Matches, mj)
	}
	return json.NewEncoder(w).Encode(doc)
}

// runSearch implements the `search` subcommand: `pa-monitor search
// [--json] [--substring] <query> [--session <id>] [--since <bound>] [--before <bound>]`. It
// asks the recorder; while the recorder is down it says "daemon offline" and
// exits 1 (no fallback to the transcript files).
func runSearch(args []string) {
	query, sessionID, since, before, substring, err := parseSearchArgs(args, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(exitUsage)
	}
	facts := recorderfacts.New(client.Options{Name: "pa-monitor-search"})
	if err := searchSessions(os.Stdout, facts, query, sessionID, since, before, substring); err != nil {
		fmt.Fprintf(os.Stderr, "search: %v\n", err)
		os.Exit(exitGeneric)
	}
}
