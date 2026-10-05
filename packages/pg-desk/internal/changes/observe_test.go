package changes

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

var observeNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// seedObserve fills a new-schema store with a small, fully known change flow:
// three pr change_log records (seq 1-3), two active prs (one fresh, one due),
// two consumers at different cursors, and a hydration history for "pr".
func seedObserve(t *testing.T) *store.Store {
	t.Helper()
	st := store.OpenNewSchemaForTest(t)
	write := func(id string, expected int64, hydratedAt string, kinds []string, origin string) {
		t.Helper()
		e := store.Entity{Repo: "o/r", EntityType: "pr", EntityID: id, Facts: `{}`, AsOf: "x"}
		if _, err := st.WriteEntityStateWithLog(e, expected, hydratedAt, true, kinds, origin, "2026-10-01T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	write("o/r#1", 0, "2026-10-01T11:00:00Z", []string{"created"}, "poll")
	write("o/r#2", 0, "2026-09-30T00:00:00Z", []string{"created", "labels"}, "poll")
	write("o/r#1", 1, "2026-10-01T11:00:00Z", []string{"reconcile"}, "sweep")

	for name, cursor := range map[string]int64{"alpha": 1, "beta": 3} {
		if err := st.RegisterConsumer(name, "pr", observeNow); err != nil {
			t.Fatal(err)
		}
		if err := st.AdvanceCursor(name, "pr", cursor); err != nil {
			t.Fatal(err)
		}
	}

	RecordHydration(st, "pr", "o/r#1", pipeline.EntityChangeResult{Retries: 2}, nil)
	RecordHydration(st, "pr", "o/r#2", pipeline.EntityChangeResult{Degraded: "ci"}, nil)
	RecordHydration(st, "pr", "o/r#2", pipeline.EntityChangeResult{}, errors.New("boom"))
	return st
}

func TestObserveReportsEveryChangeFlowFamily(t *testing.T) {
	st := seedObserve(t)
	flow, err := Observe(st, observeNow, 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !flow.Migrated {
		t.Fatal("Migrated = false on a new-schema store")
	}

	var pr *TypeFlow
	for i := range flow.Types {
		if flow.Types[i].Type == "pr" {
			pr = &flow.Types[i]
		}
	}
	if pr == nil {
		t.Fatalf("no pr type in %+v", flow.Types)
	}
	wantRecords := []RecordCount{
		{Kind: "created", Origin: "poll", Count: 2},
		{Kind: "labels", Origin: "poll", Count: 1},
		{Kind: "reconcile", Origin: "sweep", Count: 1},
	}
	if !reflect.DeepEqual(pr.Records, wantRecords) {
		t.Errorf("Records = %+v, want %+v", pr.Records, wantRecords)
	}
	if pr.MaxSeq != 3 || pr.Active != 2 || pr.Due != 1 {
		t.Errorf("MaxSeq/Active/Due = %d/%d/%d, want 3/2/1", pr.MaxSeq, pr.Active, pr.Due)
	}
	if pr.Stats.Hydrations != 3 || pr.Stats.Failures != 2 || pr.Stats.OCCRetries != 2 {
		t.Errorf("Stats = %+v, want 3 hydrations, 2 failures, 2 retries", pr.Stats)
	}
	if len(pr.Repeated) != 1 || pr.Repeated["o/r#2"].Count != 2 {
		t.Errorf("Repeated = %+v, want o/r#2 x2", pr.Repeated)
	}

	lag := map[string]int64{}
	for _, c := range flow.Consumers {
		lag[c.Name] = c.Lag
		if c.SeenAt == "" {
			t.Errorf("consumer %s has no seen_at", c.Name)
		}
	}
	if !reflect.DeepEqual(lag, map[string]int64{"alpha": 2, "beta": 0}) {
		t.Errorf("lag = %+v, want alpha 2 (3-1), beta 0 (3-3)", lag)
	}

	// The other known types are reported, empty.
	if len(flow.Types) != len(FlowEntityTypes) {
		t.Errorf("types = %d, want %d", len(flow.Types), len(FlowEntityTypes))
	}
}

func TestObserveDegradesOnAnOldSchemaStore(t *testing.T) {
	st := store.OpenForTest(t) // schema 1: no change_log or consumer table
	flow, err := Observe(st, observeNow, 6*time.Hour)
	if err != nil {
		t.Fatalf("Observe on an old-schema store must degrade, not fail: %v", err)
	}
	if flow.Migrated || len(flow.Types) != 0 || len(flow.Consumers) != 0 {
		t.Errorf("flow = %+v, want zero", flow)
	}
}
