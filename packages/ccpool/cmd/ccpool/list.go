package main

import (
	"context"
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
	out := renderList(rows, *all, *stateFilter, tmux.HasSession, cfg.Tmux.Socket,
		time.Now(), time.Duration(cfg.List.DoneTTL), time.Duration(cfg.List.FailedTTL))
	fmt.Print(out)
	return 0
}

// renderList reconciles liveness via liveFn, applies the retention view, and
// returns the rendered table. Pure (no I/O) so it is unit-testable.
func renderList(rows []store.Session, all bool, stateFilter string,
	liveFn func(socket, target string) bool, socket string,
	now time.Time, doneTTL, failedTTL time.Duration) string {

	var b strings.Builder
	fmt.Fprintf(&b, "%-20s %-12s %-5s %-20s %s\n", "NAME", "STATE", "LIVE", "LAST ACTIVITY", "UUID")
	for _, r := range rows {
		if stateFilter != "" && string(r.State) != stateFilter {
			continue
		}
		live := liveFn(socket, r.TmuxSession)
		if !all && hiddenByRetention(r, live, now, doneTTL, failedTTL) {
			continue
		}
		liveStr := "no"
		if live {
			liveStr = "yes"
		}
		fmt.Fprintf(&b, "%-20s %-12s %-5s %-20s %s\n",
			r.Name, r.State, liveStr,
			time.Unix(r.LastActivityAt, 0).Format("2006-01-02 15:04:05"),
			shortUUID(r.UUID))
	}
	return b.String()
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
