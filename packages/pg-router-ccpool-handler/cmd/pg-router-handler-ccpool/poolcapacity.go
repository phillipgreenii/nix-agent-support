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

// poolEmitFn asks ONE pool to emit its capacity gauge — the seam
// runPoolCapacity's production wiring (ccpoolEmitFor) and this file's tests
// (a fake, zero real processes) both satisfy. It receives only the pool
// directory: the name half of --pool name=dir is a display label and is
// never passed to ccpool, so it cannot influence the metric's pool attribute.
type poolEmitFn func(ctx context.Context, dir string) error

// ccpoolEmitFor is the production poolEmitFn: a real
// ccpool.NewCLIRunnerForPool scoped to dir (CCPOOL_POOL override) running
// `ccpool capacity --emit-metrics`. ccpool resolves the pool root through
// symlinks and emits ccpool_pool_capacity{pool=basename(root),dim=...} over
// OTLP using the OTEL_* env this process inherited (obs.mkEmitterEnv on the
// LaunchAgent). config.Default() is safe: no launch-flag fields are read.
func ccpoolEmitFor(ctx context.Context, dir string) error {
	return ccpool.NewCLIRunnerForPool(config.Default(), dir).EmitCapacity(ctx)
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
	return emitPoolCapacity(os.Stderr, pools.entries, ccpoolEmitFor)
}

// emitPoolCapacity triggers capacity emission for every entry, sorted by name
// for deterministic ordering. A failing pool is reported on w as a
// `pool-capacity:` line and does not stop the other pools; the result is
// conformance.ExitError iff at least one pool failed. Nothing is written for
// healthy pools: the data goes out over OTLP, not stdout (the former
// pool-capacity.prom exposition text and its temp-file writer are retired).
func emitPoolCapacity(w io.Writer, pools []poolEntry, emit poolEmitFn) int {
	sorted := append([]poolEntry(nil), pools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })

	ctx := context.Background()
	failed := 0
	for _, p := range sorted {
		if err := emit(ctx, p.dir); err != nil {
			failed++
			fmt.Fprintf(w, "pool-capacity: role=%q dir=%q: %v\n", p.name, p.dir, err)
		}
	}
	if failed > 0 {
		return conformance.ExitError
	}
	return conformance.ExitOK
}
