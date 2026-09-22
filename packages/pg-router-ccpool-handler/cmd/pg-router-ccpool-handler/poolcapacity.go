package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router/conformance"
)

// metricPoolCapacity names the Prometheus exposition-format gauge
// runPoolCapacity/writePoolCapacity emit (bead pg2-mr0sl's "per-pool/per-role
// capacity metric" acceptance criterion). Deliberately NOT `pg_router_*`:
// that prefix names packages/pg-router core's own OTel-emitted catalog
// (internal/metrics's ten declared INV-OBS-1 members, exposed via
// cmd/pg-router/metrics_http.go) — a wholly different process and transport
// from this one. This subcommand runs in the pg-router-ccpool-handler
// binary and writes Prometheus TEXT EXPOSITION FORMAT directly (no OTel SDK,
// no core dependency, ADR 0065's boundary-crossing floor: pg-router core
// stays unaware ccpool exists at all), so naming it as if it belonged to
// that catalog would misattribute it and risk silent drift against
// INV-OBS-1's own "ten members total" count. The `pg_router_ccpool_handler_`
// prefix instead names the actual emitting component.
const metricPoolCapacity = "pg_router_ccpool_handler_pool_capacity"

// poolEntry is one parsed `--pool name=dir` occurrence: name is the
// operator-facing label (this module's own role name, e.g. "review"/
// "feedback"/"worker" — home/programs/pg-router-ccpool-handler's nix module
// derives it 1:1 from each role's own `ccpool.pool.dir`), dir is the ccpool
// pool directory (CCPOOL_POOL/--pool) to query.
type poolEntry struct {
	name string
	dir  string
}

// poolFlag is a repeatable flag.Value collecting `--pool name=dir`
// occurrences — mirrors packages/pg-router/cmd/pg-router's own
// stringSliceFlag pattern (a plain repeatable string list; no precedent
// exists yet for a repeatable name=value PAIR flag, so this is this
// subcommand's own small addition to that pattern).
type poolFlag struct{ entries []poolEntry }

func (p *poolFlag) String() string {
	parts := make([]string, len(p.entries))
	for i, e := range p.entries {
		parts[i] = e.name + "=" + e.dir
	}
	return strings.Join(parts, ",")
}

func (p *poolFlag) Set(v string) error {
	name, dir, ok := strings.Cut(v, "=")
	if !ok || name == "" || dir == "" {
		return fmt.Errorf("--pool must be name=dir (non-empty name and dir), got %q", v)
	}
	p.entries = append(p.entries, poolEntry{name: name, dir: dir})
	return nil
}

// poolCapacityFn resolves one named pool's live ccpool.Capacity — the seam
// runPoolCapacity's production wiring (ccpoolCapacityFor) and this file's
// own tests (a fake, zero real processes) both satisfy.
type poolCapacityFn func(ctx context.Context, dir string) (ccpool.Capacity, error)

// ccpoolCapacityFor is the production poolCapacityFn: a real
// ccpool.NewCLIRunnerForPool scoped to dir (the SAME per-pool crossing
// dispatch.go's own buildDeps uses — INTF-CCH-CCPOOL), reused rather than
// re-implemented so a query against pool dir goes through the identical
// CCPOOL_POOL-override mechanism a real dispatch would. config.Default() is
// safe here: Capacity's own `ccpool capacity --json` call reads none of
// Config's launch-flag fields (Effort/Model/PermissionMode/...).
func ccpoolCapacityFor(ctx context.Context, dir string) (ccpool.Capacity, error) {
	return ccpool.NewCLIRunnerForPool(config.Default(), dir).Capacity(ctx)
}

// runPoolCapacity implements the `pool-capacity` subcommand (bead pg2-mr0sl).
// Unlike register/dispatch/query/postStartup/preShutdown, this is NOT a
// wire-facing INTF-HANDLER/INTF-SOURCE subcommand pg-router core ever
// spawns — it is an operational/observability tool invoked directly (e.g.
// by a periodic systemd/launchd timer;
// home/programs/pg-router-ccpool-handler's own `poolMetrics` option group
// wires exactly this). It exists so a deployment running review/feedback/
// worker each against their own dedicated ccpool pool
// (roles.CCPoolConfig.PoolDir) can still see each pool's occupancy as one
// independently-scrapable Prometheus series, rather than only via the
// `ccpool --pool <dir> capacity` CLI by hand.
func runPoolCapacity(args []string) int {
	fs := flag.NewFlagSet("pool-capacity", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pools := &poolFlag{}
	fs.Var(pools, "pool", "name=dir pair naming a ccpool pool to report capacity for (repeatable)")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "pool-capacity:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "pool-capacity: unexpected argument:", fs.Arg(0))
		return conformance.ExitUsage
	}
	if len(pools.entries) == 0 {
		fmt.Fprintln(os.Stderr, "pool-capacity: at least one --pool name=dir is required")
		return conformance.ExitUsage
	}
	return writePoolCapacity(os.Stdout, pools.entries, ccpoolCapacityFor)
}

// writePoolCapacity renders each entry's live capacity as Prometheus
// exposition-format text to w: one gauge sample per Capacity dimension
// (max_sessions/live/preserved/counted/free), labeled role=<name>,
// pool=<dir>. Entries are sorted by name first for deterministic output —
// callers (and diffs of captured output in tests) must not depend on
// argv order.
//
// A pool whose capacity lookup fails (ccpool unreachable, pool dir
// unreadable, ...) is reported as a `#`-prefixed comment naming the
// failure and excluded from the metric series — one broken pool must not
// blank out the other pools' real numbers, and a comment line is inert to
// any Prometheus text-format parser (ignored, never a parse error).
// Returns conformance.ExitError iff at least one pool failed (so a caller's
// own log/journal still notices), conformance.ExitOK otherwise — either way
// every pool that COULD be queried was already written.
func writePoolCapacity(w io.Writer, pools []poolEntry, capacityFor poolCapacityFn) int {
	sorted := append([]poolEntry(nil), pools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })

	fmt.Fprintf(w, "# HELP %s ccpool pool capacity, per role/pool and dimension (bead pg2-mr0sl)\n", metricPoolCapacity)
	fmt.Fprintf(w, "# TYPE %s gauge\n", metricPoolCapacity)

	ctx := context.Background()
	failed := 0
	for _, p := range sorted {
		c, err := capacityFor(ctx, p.dir)
		if err != nil {
			failed++
			fmt.Fprintf(w, "# pool-capacity: role=%q pool=%q: %v\n", p.name, p.dir, err)
			continue
		}
		for _, dim := range []struct {
			name string
			val  int
		}{
			{"max_sessions", c.MaxSessions},
			{"live", c.Live},
			{"preserved", c.Preserved},
			{"counted", c.Counted},
			{"free", c.Free},
		} {
			fmt.Fprintf(w, "%s{role=%q,pool=%q,dim=%q} %d\n", metricPoolCapacity, p.name, p.dir, dim.name, dim.val)
		}
	}
	if failed > 0 {
		return conformance.ExitError
	}
	return conformance.ExitOK
}
