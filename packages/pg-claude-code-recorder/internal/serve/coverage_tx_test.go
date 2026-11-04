package serve

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

func TestCoverageReadsInOneTransactionAndAnswersAsBefore(t *testing.T) {
	c := countdriver.Register("coverage-tx-counting")
	e := newEnv(t, false)
	now := time.Now()
	for _, n := range []string{"a.jsonl", "b.jsonl", "c.jsonl"} {
		e.transcript(n, "line\n", now.Add(-time.Hour))
	}
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, StoreDriver: "coverage-tx-counting"})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}

	// The per-statement version: the same body over the read pool.
	c.Reset()
	want, err := d.computeCoverageFrom(context.Background(), &d.st.Reader, "files", true, statAll, 0)
	if err != nil {
		t.Fatal(err)
	}
	perStmtTx, perStmtQ := c.Transactions(), c.Queries()

	c.Reset()
	got, err := d.computeCoverage("files", true)
	if err != nil {
		t.Fatal(err)
	}
	batchedTx, batchedQ := c.Transactions(), c.Queries()
	t.Logf("coverage over %d files: per statement %d transactions for %d statements; one read transaction %d for %d",
		got.Files, perStmtTx, perStmtQ, batchedTx, batchedQ)

	if got.Files != 3 {
		t.Fatalf("fixture has %d files", got.Files)
	}
	// Backfill progress and timing fields are not store reads; compare the rest.
	if !reflect.DeepEqual(got.FileList, want.FileList) || got.Settled != want.Settled || got.Held != want.Held ||
		got.BadLines != want.BadLines || !reflect.DeepEqual(got.UnknownKeys, want.UnknownKeys) ||
		got.CopyLagBytesMax != want.CopyLagBytesMax || got.ParseLagBytesMax != want.ParseLagBytesMax {
		t.Fatalf("read transaction changed the answer:\nwant %+v\ngot  %+v", want, got)
	}
	if batchedTx != 1 || perStmtTx < 5 || batchedQ != perStmtQ {
		t.Fatalf("transactions: per statement %d (%d q), batched %d (%d q)", perStmtTx, perStmtQ, batchedTx, batchedQ)
	}
}

func TestCollectPoolMetricsPublishesBothPools(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	_, _ = d.st.AllFiles()
	reg := obs.NewRegistry()
	collectPoolMetrics(reg, d.st.PoolStats())
	for _, pool := range []string{"read", "write"} {
		l := map[string]string{"pool": pool}
		if g, ok := reg.Gauge(obs.MDBOpenConns, l); !ok || g < 1 {
			t.Errorf("%s open connections %v %v", pool, g, ok)
		}
		for _, n := range []string{obs.MDBWaitCount, obs.MDBWaitSeconds, obs.MDBMaxLifetimeClosed, obs.MDBMaxIdleClosed} {
			if _, ok := reg.Counter(n, l); !ok {
				t.Errorf("%s: %s missing", pool, n)
			}
		}
		for _, n := range []string{obs.MDBInUseConns, obs.MDBIdleConns} {
			if _, ok := reg.Gauge(n, l); !ok {
				t.Errorf("%s: %s missing", pool, n)
			}
		}
	}
	_ = store.DBName
}
