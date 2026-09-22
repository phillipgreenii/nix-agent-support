package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/session"
)

// runCapacity reports the pool's occupancy: max_sessions, live, preserved,
// counted, and free (ADR 0072) — the read-only query an admission gate (the
// pg-router-ccpool-handler, in a later packet) consults before launching,
// reusing the exact same capacity definition Reap's Pass 2 already applies
// (session.Service.Capacity -> countedSessions).
func runCapacity(args []string) int {
	fs := flag.NewFlagSet("capacity", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "capacity: %v\n", err)
		return 1
	}
	svc, st, code := buildService()
	if code != 0 {
		return code
	}
	defer func() { _ = st.Close() }()

	c, err := svc.Capacity(context.Background(), cfg.Pool.MaxSessions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "capacity: %v\n", err)
		return 1
	}

	if *jsonOut {
		b, err := json.Marshal(c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "capacity: %v\n", err)
			return 1
		}
		fmt.Println(string(b))
		return 0
	}
	fmt.Print(renderCapacityText(c))
	return 0
}

// renderCapacityText is the pure human-line renderer for `ccpool capacity`.
func renderCapacityText(c session.Capacity) string {
	return fmt.Sprintf("free=%d counted=%d preserved=%d live=%d max=%d\n",
		c.Free, c.Counted, c.Preserved, c.Live, c.MaxSessions)
}
