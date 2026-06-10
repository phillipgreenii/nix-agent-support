package main

import (
	"context"
	"fmt"
	"os"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

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
