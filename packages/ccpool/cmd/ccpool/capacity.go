package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/telemetry"
)

// runCapacity reports the pool's occupancy: max_sessions, live, preserved,
// counted, and free (ADR 0072) — the read-only query an admission gate (the
// pg-router-ccpool-handler, in a later packet) consults before launching,
// reusing the exact same capacity definition Reap's Pass 2 already applies
// (session.Service.Capacity -> countedSessions).
func runCapacity(args []string) int {
	opts := parseCapacityArgs(args)

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

	if err := emitCapacityMetrics(opts.emitMetrics, cfg.PoolRoot, c, telemetry.RecordPoolCapacity); err != nil {
		fmt.Fprintf(os.Stderr, "capacity: emit metrics: %v\n", err)
		return 1
	}

	if opts.json {
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

// capacityOpts is `ccpool capacity`'s parsed flag set.
type capacityOpts struct {
	json        bool
	emitMetrics bool
}

// parseCapacityArgs parses `ccpool capacity`'s flags. --emit-metrics is
// opt-in and off by default: the dispatch/admission-gate path also calls
// `capacity --json` and must not emit metrics (bead pg2-om899.6).
func parseCapacityArgs(args []string) capacityOpts {
	fs := flag.NewFlagSet("capacity", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	emit := fs.Bool("emit-metrics", false, "also emit the ccpool_pool_capacity OTLP gauge")
	_ = fs.Parse(args)
	return capacityOpts{json: *jsonOut, emitMetrics: *emit}
}

// capacityRecorder records one pool's capacity snapshot:
// telemetry.RecordPoolCapacity in production, a spy in tests.
type capacityRecorder func(poolRoot string, c telemetry.PoolCapacity) error

// emitCapacityMetrics hands c to record, keyed by the canonical (symlink-
// resolved) poolRoot whose basename becomes the gauge's pool attribute, iff
// emit is set. With emit false it records nothing.
func emitCapacityMetrics(emit bool, poolRoot string, c session.Capacity, record capacityRecorder) error {
	if !emit {
		return nil
	}
	return record(poolRoot, telemetry.PoolCapacity{
		MaxSessions: int64(c.MaxSessions), Live: int64(c.Live), Preserved: int64(c.Preserved),
		Counted: int64(c.Counted), Free: int64(c.Free),
	})
}

// renderCapacityText is the pure human-line renderer for `ccpool capacity`.
func renderCapacityText(c session.Capacity) string {
	return fmt.Sprintf("free=%d counted=%d preserved=%d live=%d max=%d\n",
		c.Free, c.Counted, c.Preserved, c.Live, c.MaxSessions)
}
