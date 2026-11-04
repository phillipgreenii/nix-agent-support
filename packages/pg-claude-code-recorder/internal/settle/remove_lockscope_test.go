package settle

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// brokenFixture seeds a settled, old Conversation whose transcript has bad lines,
// with a holdRate that lets it through (so a later, lower rate must hold it).
func brokenFixture(t *testing.T) *env {
	t.Helper()
	e := newRemoveEnv(t, false)
	e.write(e.transcript(), line(1)+"{broken\n{broken\n{broken\n", e.clk.Now().Add(-removeAge-time.Hour), false)
	e.ingest(e.transcript())
	e.parseAll()
	e.tickSettleOnly()
	if e.stage() != StageSettled {
		t.Fatalf("stage %s", e.stage())
	}
	e.sett.cfg.HoldRate = 0.9
	return e
}

// A Remove whose format-hold scan is slow must not make the settle tick wait:
// the scan runs before the run lock is requested.
func TestSlowRemoveFormatScanDoesNotBlockTheTick(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	e.sett.cfg.Hooks.InFormatScan = func(string) {
		if first.CompareAndSwap(false, true) { // only the first scan (Remove's) is slow
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.sett.Remove(RemoveOptions{Sessions: []string{sess}})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the remove never reached its format scan")
	}
	tickDone := make(chan error, 1)
	go func() {
		_, err := e.sett.Tick()
		tickDone <- err
	}()
	select {
	case err := <-tickDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		close(release)
		t.Fatal("the tick waited behind a slow remove scan")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n, ok := e.obs.Reg.Counter(obs.MSettleRunLockWaits, map[string]string{"op": "tick"}); !ok || n < 1 {
		t.Fatalf("the tick's run lock wait is not counted: %v %v", n, ok)
	}
	if _, ok := e.obs.Reg.Counter(obs.MSettleRunLockWaitSeconds, map[string]string{"op": "remove"}); !ok {
		t.Fatal("the remove's run lock wait is not counted")
	}
}

// The scan before the lock may be stale: a rate that rose after it, before the
// removal, must still keep the files (the scan is repeated under the lock).
func TestRemoveRechecksTheFormatHoldUnderTheLock(t *testing.T) {
	e := brokenFixture(t)
	e.sett.cfg.Hooks.AfterFormatPrescan = func() { e.sett.cfg.HoldRate = 0.01 }
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), BlockFormat) {
		t.Fatalf("a source that became held after the early scan was removed: %+v", rep)
	}
	if !e.exists(e.transcript()) {
		t.Fatal("the transcript is gone")
	}
	// Negative control: the same fixture with the rate left high removes it.
	e.sett.cfg.Hooks.AfterFormatPrescan = nil
	e.sett.cfg.HoldRate = 0.9
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("control: %+v", rep)
	}
}

// One pass scans each source once, however many Conversations it covers.
func TestFormatScanRunsOncePerSourcePerPass(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	var n atomic.Int32
	e.sett.cfg.Hooks.InFormatScan = func(string) { n.Add(1) }
	if _, err := e.sett.Remove(RemoveOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	groups, err := e.sett.groups()
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]bool{}
	for _, c := range groups {
		for _, f := range c.files {
			sources[f.Source] = true
		}
	}
	if got := int(n.Load()); got > len(sources) || got == 0 {
		t.Fatalf("%d scans for %d sources", got, len(sources))
	}
}

// The rewritten counts answer exactly what the earlier shape of the queries did.
func TestFormatQueriesAgreeWithTheDriveFromRecordsShape(t *testing.T) {
	e := brokenFixture(t)
	const oldLines = `SELECT COUNT(DISTINCT r.file_id||'-'||r.gen||'-'||r.off) FROM records r JOIN files f ON f.id=r.file_id
JOIN commits c ON c.file_id=r.file_id AND c.gen=r.gen AND c.from_off<=r.off AND r.off<c.to_off
WHERE f.source=? AND c.seen_ns>=?`
	cutoff := e.clk.Now().Add(-24 * time.Hour).UnixNano()
	// Widen the window so the fixture's commits fall inside it.
	for _, cut := range []int64{cutoff, 0} {
		var a, b int64
		if err := e.st.Read().QueryRow(oldLines, "claude-transcript", cut).Scan(&a); err != nil {
			t.Fatal(err)
		}
		if err := e.st.Read().QueryRow(formatLinesQuery, "claude-transcript", cut).Scan(&b); err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatalf("cutoff %d: old %d, new %d", cut, a, b)
		}
		if cut == 0 && a == 0 {
			t.Fatal("the fixture has no lines to compare")
		}
		const oldBad = `SELECT COUNT(*) FROM bad_lines b JOIN files f ON f.id=b.file_id
JOIN commits c ON c.file_id=b.file_id AND c.gen=b.gen AND c.from_off<=b.off AND b.off<c.to_off
WHERE f.source=? AND c.seen_ns>=?`
		if err := e.st.Read().QueryRow(oldBad, "claude-transcript", cut).Scan(&a); err != nil {
			t.Fatal(err)
		}
		if err := e.st.Read().QueryRow(formatBadQuery, "claude-transcript", cut).Scan(&b); err != nil {
			t.Fatal(err)
		}
		if a != b || (cut == 0 && a == 0) {
			t.Fatalf("bad lines, cutoff %d: old %d, new %d", cut, a, b)
		}
	}
}

// None of the format-hold counts may scan records: they reach it through
// records_ident from the commits of the source's files.
func TestFormatHeldQueryPlansUseIndexes(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("sqlite", filepath.Join(st.Dir(), store.DBName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	scan := regexp.MustCompile(`^SCAN (r|c|b|records|commits|bad_lines)\b`)
	for name, q := range map[string]string{
		"lines": formatLinesQuery, "bad": formatBadQuery, "unknown": formatUnknownQuery,
	} {
		t.Run(name, func(t *testing.T) {
			rows, err := db.Query("EXPLAIN QUERY PLAN "+q, "claude-transcript", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var d string
				if err := rows.Scan(&id, &parent, &unused, &d); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, d)
				if scan.MatchString(d) {
					t.Errorf("plan scans: %q", d)
				}
			}
			if name == "lines" && !strings.Contains(strings.Join(plan, "\n"), "records_ident") {
				t.Errorf("the lines count does not use records_ident: %v", plan)
			}
		})
	}
}
