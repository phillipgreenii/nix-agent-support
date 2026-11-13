package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/consumption"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/x/exitcode"
)

// exitNoSuchSession is the documented exit code of `ccpool consumption` for an
// unknown session (exit 1 stays generic; `state` uses 1 for this).
const exitNoSuchSession = exitcode.NotFound

// runConsumption prints one session's consumption reading (INV-METER-1): the
// model, token totals and an estimated cost in integer USD cents at API list
// prices, read on demand from the session transcript. Read-only and policy
// free: ccpool has no cap, flag or stop here.
//
// Exit: 0 reading printed (a live session with no transcript yet reads zeros);
// 1 config/store/transcript error; 2 usage; 8 no such session.
func runConsumption(args []string) int {
	fs := flag.NewFlagSet("consumption", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)
	externalID := fs.Arg(0)
	if fs.NArg() > 1 { // flags may follow the id: ccpool consumption <id> --json
		_ = fs.Parse(fs.Args()[1:])
	}
	if externalID == "" {
		fmt.Fprintln(os.Stderr, "usage: ccpool consumption <external_id> [--json]")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		slog.Error("consumption: config load failed", "err", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		slog.Error("consumption: store open failed", "err", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	row, ok, _ := st.GetByExternalID(context.Background(), externalID)
	if !ok {
		fmt.Fprintln(os.Stderr, "no such session")
		return exitNoSuchSession
	}
	r, err := consumption.Read(row.TranscriptPath, nil)
	if err != nil {
		slog.Error("consumption: transcript read failed", "err", err)
		return 1
	}
	if *jsonOut {
		b, err := json.Marshal(r)
		if err != nil {
			slog.Error("consumption: json render failed", "err", err)
			return 1
		}
		fmt.Println(string(b))
		return 0
	}
	fmt.Printf("model=%s input=%d output=%d cache_creation=%d cache_read=%d estimated_cost_cents=%d\n",
		r.Model, r.Input, r.Output, r.CacheCreation, r.CacheRead, r.EstimatedCostCents)
	return 0
}
