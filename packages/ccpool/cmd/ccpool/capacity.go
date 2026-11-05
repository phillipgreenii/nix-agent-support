package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/telemetry"
)

// runCapacity reports the pool's occupancy: max_sessions, live, preserved,
// counted, and free (ADR 0072), plus usage_limit when an account usage window
// is at its limit (Free is then 0) — the read-only query an admission gate (the
// pg-router-ccpool-handler, in a later packet) consults before launching,
// reusing the exact same capacity definition Reap's Pass 2 already applies
// (session.Service.Capacity -> countedSessions).
func runCapacity(args []string) int {
	opts := parseCapacityArgs(args)

	if len(opts.poolDirs) > 0 {
		if !opts.emitMetrics || opts.json {
			fmt.Fprintln(os.Stderr, "capacity: --pool-dir requires --emit-metrics and cannot be combined with --json")
			return 2
		}
		return emitCapacityForPools(opts.poolDirs, emitPoolCapacityMetrics)
	}

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

	c, err := poolCapacity(context.Background(), svc, cfg)
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

// poolCapacity reads one pool's occupancy. A hit usage window zeroes Free and is
// reported alongside, so every admission gate that reads Free (pg-router's
// handler among them) declines without needing to know about usage windows.
func poolCapacity(ctx context.Context, svc *session.Service, cfg config.Config) (session.Capacity, error) {
	c, err := svc.Capacity(ctx, cfg.Pool.MaxSessions)
	if err != nil {
		return session.Capacity{}, err
	}
	return c.WithUsageLimit(currentUsageLimit(ctx, cfg)), nil
}

// emitPoolCapacityMetrics loads the pool rooted at dir (without touching
// CCPOOL_POOL) and emits its capacity gauge.
func emitPoolCapacityMetrics(dir string) error {
	cfg, err := config.LoadForPool(dir)
	if err != nil {
		return err
	}
	svc, st, code := buildServiceFor(cfg)
	if code != 0 {
		return fmt.Errorf("build service failed (exit %d)", code)
	}
	defer func() {
		setSessionLabeler(nil)
		_ = st.Close()
	}()
	c, err := poolCapacity(context.Background(), svc, cfg)
	if err != nil {
		return err
	}
	return emitCapacityMetrics(true, cfg.PoolRoot, c, telemetry.RecordPoolCapacity)
}

// emitCapacityForPools emits every pool in dirs from this ONE process, so the
// process-wide fact reader (which caches its recorder connection) dials the
// recorder once however many pools there are. A failing pool is reported on
// stderr and does not stop the others; the result is 1 iff any pool failed.
func emitCapacityForPools(dirs []string, emit func(dir string) error) int {
	rc := 0
	for _, d := range dirs {
		if err := emit(d); err != nil {
			fmt.Fprintf(os.Stderr, "capacity: pool %q: %v\n", d, err)
			rc = 1
		}
	}
	return rc
}

// poolDirsFlag is a repeatable --pool-dir flag.
type poolDirsFlag []string

func (p *poolDirsFlag) String() string     { return strings.Join(*p, ",") }
func (p *poolDirsFlag) Set(v string) error { *p = append(*p, v); return nil }

// capacityOpts is `ccpool capacity`'s parsed flag set.
type capacityOpts struct {
	json        bool
	emitMetrics bool
	poolDirs    []string // --pool-dir: emit these pools' gauges from one process (needs --emit-metrics)
}

// parseCapacityArgs parses `ccpool capacity`'s flags. --emit-metrics is
// opt-in and off by default: the dispatch/admission-gate path also calls
// `capacity --json` and must not emit metrics (bead pg2-om899.6).
func parseCapacityArgs(args []string) capacityOpts {
	fs := flag.NewFlagSet("capacity", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	emit := fs.Bool("emit-metrics", false, "also emit the ccpool_pool_capacity OTLP gauge")
	var dirs poolDirsFlag
	fs.Var(&dirs, "pool-dir", "pool directory to emit capacity for (repeatable; requires --emit-metrics; one recorder dial for all pools)")
	_ = fs.Parse(args)
	return capacityOpts{json: *jsonOut, emitMetrics: *emit, poolDirs: dirs}
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
	line := fmt.Sprintf("free=%d counted=%d preserved=%d live=%d max=%d",
		c.Free, c.Counted, c.Preserved, c.Live, c.MaxSessions)
	if c.UsageLimit != nil {
		line += fmt.Sprintf(" usage_limit=%s resets_at=%s", c.UsageLimit.Window, c.UsageLimit.ResetsAt.Format(time.RFC3339))
	}
	return line + "\n"
}
