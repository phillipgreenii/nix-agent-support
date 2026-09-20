package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/gitfacet"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
)

// filterFlag collects repeated `--filter key=value` into a map (mirrors new.go's
// envFlag). Implements flag.Value so `ccpool list` can take --filter repeatedly.
type filterFlag map[string]string

func (f filterFlag) String() string { return "" }

func (f filterFlag) Set(kv string) error {
	k, v, ok := strings.Cut(kv, "=")
	if !ok {
		return fmt.Errorf("invalid --filter %q, want key=value", kv)
	}
	f[k] = v
	return nil
}

// parseFilters validates raw "key=value" strings into a map (used by tests and as
// the flag-free parse path). Pure.
func parseFilters(raw []string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range raw {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("invalid filter %q, want key=value", kv)
		}
		out[k] = v
	}
	return out, nil
}

// filterRowsByExternalIDSet keeps only rows whose ExternalID is in keep,
// preserving input order. Pure.
func filterRowsByExternalIDSet(rows []store.Session, keep map[string]bool) []store.Session {
	var out []store.Session
	for _, r := range rows {
		if keep[r.ExternalID] {
			out = append(out, r)
		}
	}
	return out
}

func runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	all := fs.Bool("all", false, "show cold terminal rows hidden by retention")
	stateFilter := fs.String("state", "", "only show rows in this state")
	jsonOut := fs.Bool("json", false, "emit a JSON array of sessions instead of the text table")
	pathsOnly := fs.Bool("paths", false, "print only each visible session's launch directory (worktree path), one per line, no header, no other fields — combine with --state needs_input to get a comm/grep -v-able skip list before a manual worktree cleanup sweep (ADR 0037)")
	filters := filterFlag{}
	fs.Var(filters, "filter", "only show sessions whose metadata matches key=value (repeatable, AND-combined)")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("list: config load failed", "err", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		slog.Error("list: store open failed", "err", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	rows, err := st.List(context.Background())
	if err != nil {
		slog.Error("list: list sessions failed", "err", err)
		return 1
	}

	if len(filters) > 0 {
		ids, err := st.ListExternalIDsByMeta(context.Background(), filters)
		if err != nil {
			slog.Error("list: filter failed", "err", err)
			return 1
		}
		keep := make(map[string]bool, len(ids))
		for _, id := range ids {
			keep[id] = true
		}
		rows = filterRowsByExternalIDSet(rows, keep)
	}

	// --paths short-circuits both the table and JSON renderers: it prints ONLY
	// the launch directory (store.Session.CWD, i.e. the worktree path per ADR
	// 0038 — caller-owned, persisted at creation, never re-resolved) for each
	// visible row, one per line. This is the "documented one-liner" pg2-lyriv
	// asked for: `ccpool list --state needs_input --paths` prints exactly the
	// worktree paths a manual cleanup sweep MUST NOT remove (ADR 0037), ready to
	// comm/grep -v out of a candidate-for-deletion list with no jq/JSON-parsing
	// required. Deliberately uses the STORED launch dir, never a live pane-cwd
	// query (renderListJSON's `cwd`/`worktree` git facets) or a git resolve
	// against it — both would themselves depend on the very directory this
	// command is meant to answer for even after it no longer exists on disk.
	if *pathsOnly {
		out := renderListPaths(rows, *all, *stateFilter, tmux.HasSession, cfg.Tmux.Socket,
			time.Now(), time.Duration(cfg.List.DoneTTL), time.Duration(cfg.List.FailedTTL))
		fmt.Print(out)
		return 0
	}

	// metaFn reads each row's metadata from the store so the JSON shape carries a
	// `meta` object per row (consumers get metadata in one call).
	metaFn := func(externalID string) map[string]string {
		m, err := st.Meta(context.Background(), externalID)
		if err != nil {
			return nil
		}
		return m
	}

	if *jsonOut {
		// cwd is the LIVE pane current path (tmux display-message), falling back
		// to the launch cwd when the session is not live; the git facets resolve
		// against that cwd (fail-soft to null outside a repo). Resolvers are
		// injected so the renderer stays pure (pg2-gxxl).
		out, err := renderListJSON(rows, *all, *stateFilter,
			tmux.HasSession, tmux.PaneCurrentPath, gitfacet.Resolve, metaFn, cfg.Tmux.Socket,
			time.Now(), time.Duration(cfg.List.DoneTTL), time.Duration(cfg.List.FailedTTL))
		if err != nil {
			slog.Error("list: json render failed", "err", err)
			return 1
		}
		fmt.Println(out)
		return 0
	}

	out := renderList(rows, *all, *stateFilter, tmux.HasSession, cfg.Tmux.Socket,
		time.Now(), time.Duration(cfg.List.DoneTTL), time.Duration(cfg.List.FailedTTL))
	fmt.Print(out)
	return 0
}

// liveRow pairs a store row with its reconciled liveness for rendering.
type liveRow struct {
	row  store.Session
	live bool
}

// visibleRows applies the state filter and the retention view (unless all),
// reconciling liveness via liveFn, and returns the rows to render in store order.
// Shared by the text and JSON renderers so both honor identical view hygiene.
func visibleRows(rows []store.Session, all bool, stateFilter string,
	liveFn func(socket, target string) bool, socket string,
	now time.Time, doneTTL, failedTTL time.Duration,
) []liveRow {
	var out []liveRow
	for _, r := range rows {
		if stateFilter != "" && string(r.State) != stateFilter {
			continue
		}
		live := liveFn(socket, r.TmuxSession)
		if !all && hiddenByRetention(r, live, now, doneTTL, failedTTL) {
			continue
		}
		out = append(out, liveRow{row: r, live: live})
	}
	return out
}

// renderList reconciles liveness via liveFn, applies the retention view, and
// returns the rendered table. Pure (no I/O) so it is unit-testable.
func renderList(rows []store.Session, all bool, stateFilter string,
	liveFn func(socket, target string) bool, socket string,
	now time.Time, doneTTL, failedTTL time.Duration,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-16s %-12s %-5s %-20s %s\n", "EXTERNAL_ID", "NAME", "STATE", "LIVE", "LAST ACTIVITY", "CLAUDE_SESSION_ID")
	for _, lr := range visibleRows(rows, all, stateFilter, liveFn, socket, now, doneTTL, failedTTL) {
		r := lr.row
		liveStr := "no"
		if lr.live {
			liveStr = "yes"
		}
		fmt.Fprintf(&b, "%-24s %-16s %-12s %-5s %-20s %s\n",
			r.ExternalID, r.Name, r.State, liveStr,
			time.Unix(r.LastActivityAt, 0).Format("2006-01-02 15:04:05"),
			shortUUID(r.ClaudeSessionID))
	}
	return b.String()
}

// renderListPaths is the pure renderer behind --paths: it applies the same
// state-filter + retention view hygiene as renderList/renderListJSON (via
// visibleRows), then prints each visible row's launch directory
// (store.Session.CWD), one per line, with no header and no other fields. The
// launch dir is used unconditionally — never a live-pane query or a git
// resolve — because both of those would need the directory to still exist,
// which is exactly the thing a caller of --paths cannot assume (pg2-lyriv:
// the worktree backing a LIVE needs_input row can be deleted out from under
// it while tmux still reports the session alive).
func renderListPaths(rows []store.Session, all bool, stateFilter string,
	liveFn func(socket, target string) bool, socket string,
	now time.Time, doneTTL, failedTTL time.Duration,
) string {
	var b strings.Builder
	for _, lr := range visibleRows(rows, all, stateFilter, liveFn, socket, now, doneTTL, failedTTL) {
		fmt.Fprintln(&b, lr.row.CWD)
	}
	return b.String()
}

// listJSON is the --json shape consumed by pg-router's Runner.List. `live` is
// SEPARATE from `state` (tmux has-session liveness, not folded into state).
// Location facets (pg2-gxxl):
//   - launch_dir: directory ccpool launched the session in (store.Session.CWD).
//     Always present.
//   - cwd: the LIVE pane current working directory for a live session, falling
//     back to launch_dir when the session is not live or the pane query fails.
//     Always present; KEEPS its name for backward compat (pg-router maps it).
//   - git_repo_root / worktree / branch: git-dependent facets resolved against
//     cwd; pointers with omitempty, so they marshal to absent when cwd is not
//     inside a git work tree (fail-soft, never error the whole list).
//
// transcript_path, launch_dir and cwd are always present (no omitempty) so
// consumers get a stable schema.
type listJSON struct {
	ExternalID      string            `json:"external_id"`
	Name            string            `json:"name"`
	State           string            `json:"state"`
	Live            bool              `json:"live"`
	TranscriptPath  string            `json:"transcript_path"`
	ClaudeSessionID string            `json:"claude_session_id"`
	LaunchDir       string            `json:"launch_dir"`
	CWD             string            `json:"cwd"`
	GitRepoRoot     *string           `json:"git_repo_root,omitempty"`
	Worktree        *string           `json:"worktree,omitempty"`
	Branch          *string           `json:"branch,omitempty"`
	Meta            map[string]string `json:"meta,omitempty"`
}

// renderListJSON marshals the visible rows as a JSON array (one object per
// session), applying the same view hygiene as renderList; --all bypasses
// retention identically. An empty result marshals as [] (never null), so
// pg-router always unmarshals a JSON array.
//
// pathFn and gitFn are injected (mirroring liveFn) so the renderer stays PURE
// and list_test.go stays hermetic. pathFn resolves a session's LIVE pane cwd;
// gitFn resolves the git-dependent facets for a cwd. Both are consulted ONLY
// for live rows: a non-live row reports cwd == launch_dir with no git facets
// (no pane to query). For a live row, cwd is pathFn's result, falling back to
// launch_dir when the pane query errors; the git facets are resolved against
// that effective cwd.
func renderListJSON(rows []store.Session, all bool, stateFilter string,
	liveFn func(socket, target string) bool,
	pathFn func(socket, target string) (string, error),
	gitFn func(cwd string) gitfacet.Facets,
	metaFn func(externalID string) map[string]string,
	socket string,
	now time.Time, doneTTL, failedTTL time.Duration,
) (string, error) {
	out := []listJSON{}
	for _, lr := range visibleRows(rows, all, stateFilter, liveFn, socket, now, doneTTL, failedTTL) {
		r := lr.row
		item := listJSON{
			ExternalID:      r.ExternalID,
			Name:            r.Name,
			State:           string(r.State),
			Live:            lr.live,
			TranscriptPath:  r.TranscriptPath,
			ClaudeSessionID: r.ClaudeSessionID,
			LaunchDir:       r.CWD,
			CWD:             r.CWD, // default: fall back to launch dir
		}
		if lr.live {
			// Resolve the LIVE pane cwd; fall back to launch dir on error.
			if p, err := pathFn(socket, r.TmuxSession); err == nil && p != "" {
				item.CWD = p
			}
			// Git facets resolve against the effective (live or fallback) cwd.
			f := gitFn(item.CWD)
			item.GitRepoRoot = f.RepoRoot
			item.Worktree = f.Worktree
			item.Branch = f.Branch
		}
		if metaFn != nil {
			if m := metaFn(r.ExternalID); len(m) > 0 {
				item.Meta = m
			}
		}
		out = append(out, item)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// hiddenByRetention implements the view-hygiene predicate: hiding a row from the
// default list is NOT deleting the session — it stays resumable. `Terminal()` is
// gone (ADR 0015 has no terminal concept), so retention keys off the two SETTLED
// last-observed outcomes directly: a row is hidden only when it is NOT live and
// its state is idle/errored older than its TTL (idle→doneTTL, errored→failedTTL).
// Any other state (or a live row) is always shown.
func hiddenByRetention(r store.Session, live bool, now time.Time, doneTTL, failedTTL time.Duration) bool {
	if live {
		return false
	}
	age := now.Sub(time.Unix(r.LastActivityAt, 0))
	switch r.State {
	case store.Idle:
		return age > doneTTL
	case store.Errored:
		return age > failedTTL
	}
	return false
}

func shortUUID(u string) string {
	if len(u) > 8 {
		return u[:8]
	}
	return u
}
