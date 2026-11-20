package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/lock"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// defaultRetentionDays is the retention window `prune --days` documents and the
// pg-disk-reclaimer registry item passes. 45 days (operator ruling, 2026-11-19) is several
// times the widest window any analysis skill (improvement-retro,
// tool-error-waste-review) looks back over.
const defaultRetentionDays = 45

// freeBytes reports the space available to an unprivileged writer on the volume
// holding dir (Bavail x Bsize; on APFS the shared container's free space without
// purgeable space, which is conservative). A variable so tests can inject it.
var freeBytes = func(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec,unconvert // platform-dependent field types
}

// pruneReport is the JSON shape: the store result plus the free-space verdict.
type pruneReport struct {
	store.PruneResult
	NeededBytes    int64 `json:"needed_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
	Refused        bool  `json:"refused"`
}

func gibs(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/float64(1<<30)) }

// sameVolume reports whether two paths are on the same device.
func sameVolume(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return true
	}
	return sa.Dev == sb.Dev
}

func cmdPrune(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "database path")
	days := fs.Int("days", defaultRetentionDays, "remove indexed files whose newest event is older than this many days")
	yes := fs.Bool("yes", false, "actually delete and VACUUM; without it prune is a dry run")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	lockPath := fs.String("lock", "", "advisory lock path (default: alongside the database)")
	force := fs.Bool("force-low-space", false, "run even when the free-space check says VACUUM may not fit")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit prune — drop indexed history older than a retention window

Removes every indexed file (a recorder file generation) whose NEWEST event is
older than --days (default %d), with all its rows, then runs VACUUM so the
disk space comes back. A file is removed whole or not at all. The feed cursors
are kept, so ingest does not re-read what was pruned. The gold set file is never
touched.

Without --yes this is a dry run: it counts and deletes nothing, and prints the
free space VACUUM needs, the space available and an OK or WOULD-REFUSE verdict.
VACUUM needs up to about 2 x the database size plus the WAL plus a margin
(max(10 percent of the database, 1 GiB)) free on the database volume. When that
is not available, --yes refuses (exit 1) before deleting anything; pass
--force-low-space to run anyway. The WAL is checkpointed before VACUUM.
With --yes it takes the ingest lock (a running ingest makes it skip, exit 0,
nothing done) and VACUUM holds that lock and the SQLite write lock for the whole
run, so recorder writes wait.

FLAGS
`, defaultRetentionDays)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *days <= 0 {
		return fmt.Errorf("--days must be positive")
	}
	resolvedDB, err := resolveDB(*dbPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(resolvedDB); err != nil {
		return fmt.Errorf("index not found at %s: %w", resolvedDB, err)
	}
	if *yes {
		lp := *lockPath
		if lp == "" {
			lp = lock.DefaultPath(resolvedDB)
		}
		handle, err := lock.TryAcquire(lp)
		if err != nil {
			fmt.Fprintf(stdout, "prune skipped: another ingest or prune holds %s\n", lp)
			return nil
		}
		defer func() { _ = handle.Release() }()
	}
	db, err := store.Open(ctx, resolvedDB, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	cutoff := time.Now().AddDate(0, 0, -*days)
	// Count first (a dry run), so the free-space guard runs before the delete,
	// which already grows the WAL.
	res, err := store.Prune(ctx, db, cutoff, true)
	if err != nil {
		return err
	}
	plan, err := store.PlanVacuum(resolvedDB)
	if err != nil {
		return err
	}
	dir := filepath.Dir(resolvedDB)
	avail, err := freeBytes(dir)
	if err != nil {
		return fmt.Errorf("free space on %s: %w", dir, err)
	}
	refused := res.Files > 0 && avail < plan.NeededBytes
	where := dir
	if tmp := os.TempDir(); tmp != "" && !sameVolume(tmp, dir) {
		// The VACUUM temp copy lands in TMPDIR on another volume: it needs about
		// the database size plus the margin there.
		if tavail, terr := freeBytes(tmp); terr == nil && tavail < plan.DBBytes+plan.MarginBytes {
			refused = res.Files > 0
			avail = tavail
			plan.NeededBytes = plan.DBBytes + plan.MarginBytes
			where = tmp
		}
	}
	report := pruneReport{PruneResult: res, NeededBytes: plan.NeededBytes, AvailableBytes: avail, Refused: refused}
	refusal := fmt.Errorf("refusing to prune: VACUUM needs about %s free (2 x %s db + %s WAL + %s margin) but only %s is available on the volume holding %s; free space or pass --force-low-space",
		gibs(plan.NeededBytes), gibs(plan.DBBytes), gibs(plan.WALBytes), gibs(plan.MarginBytes), gibs(avail), where)
	blocked := *yes && refused && !*force

	if *yes && !blocked {
		res, err = store.Prune(ctx, db, cutoff, false)
		if err != nil {
			return err
		}
		report.PruneResult = res
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		if blocked {
			return refusal
		}
		return nil
	}
	verb := "would remove"
	if !res.DryRun {
		verb = "removed"
	}
	fmt.Fprintf(stdout, "cutoff: %s (%d days)\n%s %d files\n", res.Cutoff, *days, verb, res.Files)
	names := make([]string, 0, len(res.Rows))
	for t := range res.Rows {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		fmt.Fprintf(stdout, "  %-15s %d rows\n", t, res.Rows[t])
	}
	verdict := "OK"
	if refused {
		verdict = "WOULD-REFUSE"
	}
	fmt.Fprintf(stdout, "vacuum space: needed %d bytes (%s), available %d bytes (%s): %s\n",
		plan.NeededBytes, gibs(plan.NeededBytes), avail, gibs(avail), verdict)
	if blocked {
		return refusal
	}
	if !*yes {
		fmt.Fprintln(stdout, "dry run: pass --yes to delete")
		return nil
	}
	if res.Files > 0 && !res.WALTruncated {
		fmt.Fprintln(stdout, "WAL not truncated (reader active)")
	}
	if res.Vacuumed {
		fmt.Fprintln(stdout, "vacuumed")
	}
	return nil
}
