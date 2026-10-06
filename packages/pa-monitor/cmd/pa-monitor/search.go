// search.go: the `search` subcommand — resolves each in-scope session's
// transcript via session.ResolveTranscript, then scans it with
// claude-transcript's naive Search primitive, optionally bounded by
// --since/--before. No index, no daemon RPC of its own beyond whatever
// runSearch used to build the session list.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	claudetranscript "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
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
func parseSearchArgs(args []string, now time.Time) (query, sessionID string, since, before time.Time, err error) {
	rest, _ := stripJSONFlag(args)
	var positional []string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--session":
			if i+1 >= len(rest) {
				return "", "", time.Time{}, time.Time{}, errors.New("search: --session requires a value")
			}
			sessionID = rest[i+1]
			i++
		case "--since":
			if i+1 >= len(rest) {
				return "", "", time.Time{}, time.Time{}, errors.New("search: --since requires a value")
			}
			since, err = parseTimeBound(rest[i+1], now)
			if err != nil {
				return "", "", time.Time{}, time.Time{}, fmt.Errorf("search: --since: %w", err)
			}
			i++
		case "--before":
			if i+1 >= len(rest) {
				return "", "", time.Time{}, time.Time{}, errors.New("search: --before requires a value")
			}
			before, err = parseTimeBound(rest[i+1], now)
			if err != nil {
				return "", "", time.Time{}, time.Time{}, fmt.Errorf("search: --before: %w", err)
			}
			i++
		default:
			positional = append(positional, rest[i])
		}
	}
	if len(positional) != 1 {
		return "", "", time.Time{}, time.Time{}, fmt.Errorf("search: expected exactly one query argument, got %d", len(positional))
	}
	return positional[0], sessionID, since, before, nil
}

// searchSessions scans every session in sessions (or only sessionID, when
// non-empty) for query within [since, before] (either may be the zero
// time.Time, meaning unbounded), writing the result as JSON to w. A
// session whose transcript cannot be resolved (ResolveTranscript's ok ==
// false) is silently skipped — not an error, mirroring how a dead/cold
// session is silently skipped elsewhere in this CLI (e.g. runStatus's
// per-session GetSessionInfo loop).
func searchSessions(w io.Writer, claudeHome string, sessions []*session.Session, query, sessionID string, since, before time.Time) error {
	doc := searchJSONDoc{Query: query}
	for _, s := range sessions {
		if sessionID != "" && s.SessionID != sessionID {
			continue
		}
		path, _, ok := session.ResolveTranscript(claudeHome, s)
		if !ok {
			continue
		}
		matches, err := claudetranscript.Search(path, query, since, before)
		if err != nil {
			continue // unreadable transcript: skip, don't fail the whole search
		}
		for _, m := range matches {
			mj := searchMatchJSON{SessionID: s.SessionID, Role: m.Role, Line: m.Line, Snippet: m.Snippet}
			if !m.Timestamp.IsZero() {
				mj.Timestamp = m.Timestamp.UTC().Format(time.RFC3339)
			}
			doc.Matches = append(doc.Matches, mj)
		}
	}
	enc := json.NewEncoder(w)
	return enc.Encode(doc)
}

// runSearch implements the `search` subcommand: `pa-monitor search
// [--json] <query> [--session <id>] [--since <bound>] [--before <bound>]`.
// It dials the daemon the same way runStatus does (via the status --json
// packet's shared helpers), converts the returned SessionViews into
// session.Session values (only the fields ResolveTranscript needs:
// SessionID, Cwd, Name), and delegates to searchSessions.
func runSearch(args []string) {
	query, sessionID, since, before, err := parseSearchArgs(args, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(3)
	}

	ctx, cancel := contextWithTimeout()
	defer cancel()
	client, err := dialOrExit(ctx)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()

	state, err := getStateOrExit(ctx, client)
	if err != nil {
		return
	}

	var sessions []*session.Session
	for _, d := range state.GetDirs() {
		for _, v := range d.GetSessions() {
			if v.GetSessionId() == "" {
				continue
			}
			sessions = append(sessions, &session.Session{
				SessionID: v.GetSessionId(), Cwd: v.GetCwd(), Name: v.GetName(),
			})
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "search: %v\n", err)
		os.Exit(2)
	}
	claudeHome := home + "/.claude"
	if err := searchSessions(os.Stdout, claudeHome, sessions, query, sessionID, since, before); err != nil {
		fmt.Fprintf(os.Stderr, "search: %v\n", err)
		os.Exit(2)
	}
}
