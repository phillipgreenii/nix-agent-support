package main

import (
	"context"
	"fmt"
	"os"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// attendCandidates returns the live sessions waiting on the human: needs_input
// always, plus done when includeDone. Dead rows (no live tmux pane) are dropped
// so the picker never selects a target runAttach can't attach to. Order is
// preserved from the store (last_activity DESC).
func attendCandidates(rows []store.Session, includeDone bool, liveFn func(socket, target string) bool, socket string) []store.Session {
	var out []store.Session
	for _, r := range rows {
		match := r.State == store.NeedsInput || (includeDone && r.State == store.Done)
		if match && liveFn(socket, r.TmuxSession) {
			out = append(out, r)
		}
	}
	return out
}

func runAttend(args []string) int {
	_ = args
	cfg, err := config.Load()
	if err != nil {
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		return 1
	}
	defer st.Close()
	rows, err := st.List(context.Background())
	if err != nil {
		return 1
	}
	var waiting []string
	for _, r := range rows {
		if r.State == store.NeedsInput {
			waiting = append(waiting, r.Name)
		}
	}
	switch len(waiting) {
	case 0:
		fmt.Println("no sessions waiting on input")
		return 0
	case 1:
		return runAttach([]string{waiting[0]})
	default:
		fmt.Fprintln(os.Stderr, "sessions waiting on input:")
		for _, n := range waiting {
			fmt.Println(" ", n)
		}
		fmt.Fprintln(os.Stderr, "attach one with: ccpool attach <name>")
		return 0
	}
}
