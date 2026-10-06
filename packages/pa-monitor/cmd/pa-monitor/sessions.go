// sessions.go: the `sessions` subcommand — one JSON record per Claude Code
// session, built from the transcript tree (claude-transcript's Sessions
// rollup) rather than from daemon state or PID files. It therefore works when
// the daemon is down and for ended sessions whose PID files are gone.
//
// Output is ONE JSON document, {"sessions": [...]}, records ordered by
// started_at ascending. The connector backend decodes exactly this shape (see
// sessionRecordJSON); it is pinned by a golden-file characterization test.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	claudetranscript "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/account"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
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
}

// parseSessionsArgs parses `sessions`' own argv: an optional "--json" flag
// (accepted but not required — sessions has no text mode, exactly like
// search) and optional "--since <bound>"/"--before <bound>" value flags, in
// any order. An absent bound is the zero time.Time (open on that side). now is
// injected for testability; production callers pass time.Now().UTC().
func parseSessionsArgs(args []string, now time.Time) (since, before time.Time, err error) {
	rest, _ := stripJSONFlag(args)
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--since":
			if i+1 >= len(rest) {
				return time.Time{}, time.Time{}, errors.New("sessions: --since requires a value")
			}
			since, err = parseTimeBound(rest[i+1], now)
			if err != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("sessions: --since: %w", err)
			}
			i++
		case "--before":
			if i+1 >= len(rest) {
				return time.Time{}, time.Time{}, errors.New("sessions: --before requires a value")
			}
			before, err = parseTimeBound(rest[i+1], now)
			if err != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("sessions: --before: %w", err)
			}
			i++
		default:
			return time.Time{}, time.Time{}, fmt.Errorf("sessions: unexpected argument %q", rest[i])
		}
	}
	return since, before, nil
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

// writeSessions is the exit-free core of `sessions`: it walks projectsDir for
// sessions whose first event is in [since, before) and writes the document to
// w. An unreadable transcript's first prompt degrades to "" rather than
// failing the whole listing.
func writeSessions(w io.Writer, projectsDir string, since, before time.Time, prices usage.PriceTable, promptMaxChars int) error {
	recs, err := claudetranscript.Sessions(projectsDir, since, before)
	if err != nil {
		return err
	}
	doc := sessionsJSONDoc{Sessions: make([]sessionRecordJSON, 0, len(recs))}
	for _, r := range recs {
		prompt, perr := transcript.FirstPrompt(r.TranscriptPath)
		if perr != nil {
			prompt = ""
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
			FirstPrompt:    truncateRunes(prompt, promptMaxChars),
			Tokens: sessionTokensJSON{
				Input:      r.Tokens.Input,
				Output:     r.Tokens.Output,
				CacheRead:  r.Tokens.CacheRead,
				CacheWrite: r.Tokens.CacheWrite,
			},
		}
		if c, ok := sessionCost(prices, r.ModelTokens); ok {
			sj.CostUSD = &c
		}
		doc.Sessions = append(doc.Sessions, sj)
	}
	return json.NewEncoder(w).Encode(doc)
}

// runSessions implements the `sessions` subcommand: `pa-monitor sessions
// [--json] [--since <bound>] [--before <bound>]`. Exit codes follow search:
// 3 for an argument error, 2 for a runtime error, 0 otherwise.
func runSessions(args []string) {
	since, before, err := parseSessionsArgs(args, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(3)
	}
	cfg, err := configLoad()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sessions: %v\n", err)
		os.Exit(2)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sessions: %v\n", err)
		os.Exit(2)
	}
	projectsDir := filepath.Join(home, ".claude", "projects")
	prices := account.LoadAccount(cfg).PriceTable()
	if err := writeSessions(os.Stdout, projectsDir, since, before, prices, cfg.FirstPromptMaxChars); err != nil {
		fmt.Fprintf(os.Stderr, "sessions: %v\n", err)
		os.Exit(2)
	}
}
