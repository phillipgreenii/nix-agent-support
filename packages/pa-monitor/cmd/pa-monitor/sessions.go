// sessions.go: the `sessions` subcommand — one JSON record per Claude Code
// session, built from the transcript tree (claude-transcript's Sessions
// rollup) rather than from daemon state or PID files. It therefore works when
// the daemon is down and for ended sessions whose PID files are gone. A
// per-file summary cache (sessionsCacheDir) spares re-reading the transcripts
// that did not change since the previous call; it never changes the output.
//
// Output is ONE JSON document, {"sessions": [...]}, records ordered by
// started_at ascending. The connector backend decodes exactly this shape (see
// sessionRecordJSON); it is pinned by a golden-file characterization test.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"time"

	claudetranscript "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/account"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/pa-monitor/internal/recorderfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// sessionTokensJSON is the per-session token tally.
type sessionTokensJSON struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// sessionRecordJSON is one session record. There is deliberately no "dispatched"
// key in this phase. CostUSD is a pointer so the key is omitted (not zero)
// when pricing is not known.
type sessionRecordJSON struct {
	SessionID      string            `json:"session_id"`
	Cwd            string            `json:"cwd"`
	Branch         string            `json:"branch"`
	Model          string            `json:"model"`
	StartedAt      string            `json:"started_at"`
	EndedAt        string            `json:"ended_at"`
	UserTurns      int               `json:"user_turns"`
	AssistantTurns int               `json:"assistant_turns"`
	FirstPrompt    string            `json:"first_prompt"`
	Tokens         sessionTokensJSON `json:"tokens"`
	CostUSD        *float64          `json:"cost_usd,omitempty"`
}

// sessionsJSONDoc is the stdout document. Sessions is always a non-nil slice
// so an empty window encodes as [] and never null.
type sessionsJSONDoc struct {
	Sessions []sessionRecordJSON `json:"sessions"`
	// DefaultedSince is set only when the call named no window and no --all, so
	// the list was bounded to sessions that started at or after this time (the
	// default window); a caller reading the whole history passes --all.
	DefaultedSince string `json:"defaulted_since,omitempty"`
}

// defaultSessionsWindow bounds a `sessions` call that names no --since, --before
// or --all. The recorder's unbounded scan of every transcript record outruns the
// recorder's query deadline on a large tree, while a 7-day window answers within
// it.
const defaultSessionsWindow = 7 * 24 * time.Hour

// sessionsWindow returns the window a `sessions` call runs with: the given
// bounds, or (none given and all unset) the default window ending now. The
// third result is the start of the default window, zero when none was applied.
func sessionsWindow(since, before time.Time, all bool, now time.Time) (time.Time, time.Time, time.Time) {
	if all || !since.IsZero() || !before.IsZero() {
		return since, before, time.Time{}
	}
	d := now.Add(-defaultSessionsWindow)
	return d, before, d
}

// parseSessionsArgs parses `sessions`' own argv: an optional "--json" flag
// (accepted but not required — sessions has no text mode, exactly like
// search), optional "--since <bound>"/"--before <bound>" value flags and an
// optional "--stats" flag (one line of scan statistics on stderr), in any
// order. An absent bound is the zero time.Time (open on that side). now is
// injected for testability; production callers pass time.Now().UTC().
func parseSessionsArgs(args []string, now time.Time) (since, before time.Time, stats bool, err error) {
	rest, _ := stripJSONFlag(args)
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--since":
			if i+1 >= len(rest) {
				return time.Time{}, time.Time{}, false, errors.New("sessions: --since requires a value")
			}
			since, err = parseTimeBound(rest[i+1], now)
			if err != nil {
				return time.Time{}, time.Time{}, false, fmt.Errorf("sessions: --since: %w", err)
			}
			i++
		case "--before":
			if i+1 >= len(rest) {
				return time.Time{}, time.Time{}, false, errors.New("sessions: --before requires a value")
			}
			before, err = parseTimeBound(rest[i+1], now)
			if err != nil {
				return time.Time{}, time.Time{}, false, fmt.Errorf("sessions: --before: %w", err)
			}
			i++
		case "--stats":
			stats = true
		case "--all":
			// handled by the caller (sessionsWindow): open window, no default bound
		default:
			return time.Time{}, time.Time{}, false, fmt.Errorf("sessions: unexpected argument %q", rest[i])
		}
	}
	return since, before, stats, nil
}

// truncateRunes cuts s to at most max runes. When it cuts, the last rune of the
// result is "…" so a reader can tell the prompt was shortened. max < 1 is
// treated as 1 (callers pass a positive, defaulted value).
func truncateRunes(s string, max int) string {
	if max < 1 {
		max = 1
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// sessionCost prices a session's per-model tallies with prices. ok is false —
// and the cost key must be omitted — when the session has no model tokens or
// when ANY model is absent from the price table. Models are summed in sorted
// order so the float result is deterministic.
func sessionCost(prices usage.PriceTable, modelTokens map[string]claudetranscript.TokenTotals) (float64, bool) {
	if len(modelTokens) == 0 {
		return 0, false
	}
	models := make([]string, 0, len(modelTokens))
	for m := range modelTokens {
		models = append(models, m)
	}
	sort.Strings(models)
	var total float64
	for _, m := range models {
		t := modelTokens[m]
		c, found := prices.Cost(m, usage.ModelTokens{
			Input:         int(t.Input),
			Output:        int(t.Output),
			CacheCreation: int(t.CacheWrite),
			CacheRead:     int(t.CacheRead),
		})
		if !found {
			return 0, false
		}
		total += c
	}
	return total, true
}

// sessionLister is where the listing comes from: the recorder's transcript
// records (recorderfacts.Facts), never the transcript files.
type sessionLister interface {
	Sessions(ctx context.Context, since, before time.Time) ([]recorderfacts.SessionSummary, error)
}

// writeSessions is the exit-free core of `sessions`: it lists the sessions whose
// first event is in [since, before) through lister and writes the document to w.
func writeSessions(w io.Writer, lister sessionLister, since, before time.Time, prices usage.PriceTable, promptMaxChars int, defaultedSince time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	recs, err := lister.Sessions(ctx, since, before)
	if err != nil {
		return err
	}
	doc := sessionsJSONDoc{Sessions: make([]sessionRecordJSON, 0, len(recs))}
	if !defaultedSince.IsZero() {
		doc.DefaultedSince = defaultedSince.UTC().Format(time.RFC3339)
	}
	for _, r := range recs {
		var total sessionTokensJSON
		modelTokens := make(map[string]claudetranscript.TokenTotals, len(r.ModelTokens))
		for m, t := range r.ModelTokens {
			total.Input += t.Input
			total.Output += t.Output
			total.CacheRead += t.CacheRead
			total.CacheWrite += t.CacheWrite
			modelTokens[m] = claudetranscript.TokenTotals{Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite}
		}
		sj := sessionRecordJSON{
			SessionID:      r.SessionID,
			Cwd:            r.Cwd,
			Branch:         r.Branch,
			Model:          r.Model,
			StartedAt:      r.StartedAt.UTC().Format(time.RFC3339),
			EndedAt:        r.EndedAt.UTC().Format(time.RFC3339),
			UserTurns:      r.UserTurns,
			AssistantTurns: r.AssistantTurns,
			FirstPrompt:    truncateRunes(r.FirstPrompt, promptMaxChars),
			Tokens:         total,
		}
		if c, ok := sessionCost(prices, modelTokens); ok {
			sj.CostUSD = &c
		}
		doc.Sessions = append(doc.Sessions, sj)
	}
	return json.NewEncoder(w).Encode(doc)
}

// runSessions implements the `sessions` subcommand: `pa-monitor sessions
// [--json] [--since <bound>] [--before <bound>] [--all] [--stats]`. It reads the
// recorder; while the recorder is down it says "daemon offline" and exits 1
// (there is no fallback to the transcript files). Exit codes follow search: 2
// for an argument error, 1 for a runtime error, 0 otherwise. --stats is
// accepted and ignored (there is no scan to report).
func runSessions(args []string) {
	now := time.Now().UTC()
	since, before, _, err := parseSessionsArgs(args, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(exitUsage)
	}
	since, before, defaulted := sessionsWindow(since, before, slices.Contains(args, "--all"), now)
	if !defaulted.IsZero() {
		fmt.Fprintf(os.Stderr, "sessions: no window given; listing sessions that started since %s (7d). Pass --since/--before to choose, or --all for every session (may exceed the recorder query deadline).\n", defaulted.Format(time.RFC3339))
	}
	cfg, err := configLoad()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sessions: %v\n", err)
		os.Exit(exitGeneric)
	}
	prices := account.LoadAccount(cfg).PriceTable()
	facts := recorderfacts.New(client.Options{Name: "pa-monitor-sessions"})
	if err := writeSessions(os.Stdout, facts, since, before, prices, cfg.FirstPromptMaxChars, defaulted); err != nil {
		fmt.Fprintf(os.Stderr, "sessions: %v\n", err)
		os.Exit(exitGeneric)
	}
}
