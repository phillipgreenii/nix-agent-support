package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
)

func runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	all := fs.Bool("all", false, "show cold terminal rows hidden by retention")
	stateFilter := fs.String("state", "", "only show rows in this state")
	jsonOut := fs.Bool("json", false, "emit a JSON array of sessions instead of the text table")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return 1
	}
	defer st.Close()

	rows, err := st.List(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "list:", err)
		return 1
	}

	if *jsonOut {
		// cwd is the launch cwd (store.Session.CWD). pr-pool's watchdog fail-closes
		// on cwd==REPO_ROOT, so reporting the launch cwd is a safe no-op until a
		// live pane_current_path capability lands (deferred follow-up).
		out, err := renderListJSON(rows, *all, *stateFilter, tmux.HasSession, cfg.Tmux.Socket,
			time.Now(), time.Duration(cfg.List.DoneTTL), time.Duration(cfg.List.FailedTTL))
		if err != nil {
			fmt.Fprintln(os.Stderr, "list:", err)
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

// visibleRows applies the state filter and the §11 retention view (unless all),
// reconciling liveness via liveFn, and returns the rows to render in store order.
// Shared by the text and JSON renderers so both honor identical view hygiene.
func visibleRows(rows []store.Session, all bool, stateFilter string,
	liveFn func(socket, target string) bool, socket string,
	now time.Time, doneTTL, failedTTL time.Duration) []liveRow {

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
	now time.Time, doneTTL, failedTTL time.Duration) string {

	var b strings.Builder
	fmt.Fprintf(&b, "%-20s %-12s %-5s %-20s %s\n", "NAME", "STATE", "LIVE", "LAST ACTIVITY", "UUID")
	for _, lr := range visibleRows(rows, all, stateFilter, liveFn, socket, now, doneTTL, failedTTL) {
		r := lr.row
		liveStr := "no"
		if lr.live {
			liveStr = "yes"
		}
		fmt.Fprintf(&b, "%-20s %-12s %-5s %-20s %s\n",
			r.Name, r.State, liveStr,
			time.Unix(r.LastActivityAt, 0).Format("2006-01-02 15:04:05"),
			shortUUID(r.UUID))
	}
	return b.String()
}

// listJSON is the --json shape consumed by pr-pool's Runner.List. `live` is
// SEPARATE from `state` (tmux has-session liveness, not folded into state).
// transcript_path and cwd are always present (no omitempty) so consumers get a
// stable schema. cwd is the launch cwd today; see runList for the deferred
// live-path enhancement.
type listJSON struct {
	Name           string `json:"name"`
	State          string `json:"state"`
	Live           bool   `json:"live"`
	TranscriptPath string `json:"transcript_path"`
	UUID           string `json:"uuid"`
	CWD            string `json:"cwd"`
}

// renderListJSON marshals the visible rows as a JSON array (one object per
// session), applying the same view hygiene as renderList; --all bypasses
// retention identically. An empty result marshals as [] (never null), so
// pr-pool always unmarshals a JSON array.
func renderListJSON(rows []store.Session, all bool, stateFilter string,
	liveFn func(socket, target string) bool, socket string,
	now time.Time, doneTTL, failedTTL time.Duration) (string, error) {

	out := []listJSON{}
	for _, lr := range visibleRows(rows, all, stateFilter, liveFn, socket, now, doneTTL, failedTTL) {
		r := lr.row
		out = append(out, listJSON{
			Name:           r.Name,
			State:          string(r.State),
			Live:           lr.live,
			TranscriptPath: r.TranscriptPath,
			UUID:           r.UUID,
			CWD:            r.CWD,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// hiddenByRetention implements the §11 view-hygiene predicate: a row is hidden
// only when it is NOT live AND terminal AND older than its TTL.
func hiddenByRetention(r store.Session, live bool, now time.Time, doneTTL, failedTTL time.Duration) bool {
	if live || !r.State.Terminal() {
		return false
	}
	age := now.Sub(time.Unix(r.LastActivityAt, 0))
	switch r.State {
	case store.Done:
		return age > doneTTL
	case store.Failed:
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
