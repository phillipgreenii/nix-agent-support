package changes

import (
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func TestReadHydrationStatsMissingKeysReadAsZero(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	got, err := ReadHydrationStats(st, "pr")
	if err != nil {
		t.Fatal(err)
	}
	if got.Hydrations != 0 || got.Failures != 0 || got.OCCRetries != 0 || got.Degraded == nil || len(got.Degraded) != 0 {
		t.Errorf("stats = %+v", got)
	}
}

func TestRecordHydrationPersistsTotalsUnderThePinnedKeys(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	RecordHydration(st, "pr", "o/r#1", pipeline.EntityChangeResult{Written: true, Retries: 2}, nil)
	RecordHydration(st, "pr", "o/r#2", pipeline.EntityChangeResult{Degraded: "ci list"}, nil)
	RecordHydration(st, "pr", "o/r#3", pipeline.EntityChangeResult{}, errors.New("boom"))
	RecordHydration(st, "issue", "bd-1", pipeline.EntityChangeResult{Retries: 1}, nil)

	for key, want := range map[string]string{
		"change_flow.hydrations.pr":         "3",
		"change_flow.hydration_failures.pr": "2",
		"change_flow.occ_retries.pr":        "2",
		"change_flow.hydrations.issue":      "1",
		"change_flow.occ_retries.issue":     "1",
	} {
		got, _, err := st.GetMeta(key)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if _, found, _ := st.GetMeta("change_flow.hydration_failures.issue"); found {
		t.Error("a clean type must not get a failure key")
	}

	pr, err := ReadHydrationStats(st, "pr")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Hydrations != 3 || pr.Failures != 2 || pr.OCCRetries != 2 {
		t.Errorf("pr stats = %+v", pr)
	}
	if len(pr.Degraded) != 2 || pr.Degraded["o/r#2"].Count != 1 || pr.Degraded["o/r#3"].Count != 1 {
		t.Errorf("degraded = %+v", pr.Degraded)
	}
}

func TestRecordHydrationDegradedRunGrowsAndClearsOnSuccess(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	orig := recorderNow
	t.Cleanup(func() { recorderNow = orig })
	first := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	recorderNow = func() time.Time { return first }

	RecordHydration(st, "pr", "o/r#1", pipeline.EntityChangeResult{Degraded: "ci list"}, nil)
	recorderNow = func() time.Time { return first.Add(time.Hour) }
	RecordHydration(st, "pr", "o/r#1", pipeline.EntityChangeResult{}, errors.New("boom"))

	stats, _ := ReadHydrationStats(st, "pr")
	e := stats.Degraded["o/r#1"]
	if e.Count != 2 || e.Since != "2026-10-01T08:00:00Z" {
		t.Errorf("degraded run = %+v, want count 2 since the FIRST failure", e)
	}
	if rep := RepeatedDegraded(stats); len(rep) != 1 || rep["o/r#1"].Count != 2 {
		t.Errorf("RepeatedDegraded = %+v", rep)
	}

	RecordHydration(st, "pr", "o/r#1", pipeline.EntityChangeResult{Written: true}, nil)
	stats, _ = ReadHydrationStats(st, "pr")
	if len(stats.Degraded) != 0 {
		t.Errorf("a successful hydration must clear the entity, got %+v", stats.Degraded)
	}
	if stats.Hydrations != 3 || stats.Failures != 2 {
		t.Errorf("stats = %+v", stats)
	}
	// A new failure after the clear starts a fresh run.
	recorderNow = func() time.Time { return first.Add(2 * time.Hour) }
	RecordHydration(st, "pr", "o/r#1", pipeline.EntityChangeResult{}, errors.New("again"))
	stats, _ = ReadHydrationStats(st, "pr")
	if e := stats.Degraded["o/r#1"]; e.Count != 1 || e.Since != "2026-10-01T10:00:00Z" {
		t.Errorf("fresh run = %+v", e)
	}
}

func TestRepeatedDegradedUsesThresholdTwo(t *testing.T) {
	if RepeatedDegradedThreshold != 2 {
		t.Fatalf("threshold = %d", RepeatedDegradedThreshold)
	}
	stats := HydrationStats{Degraded: map[string]DegradedEntity{
		"a": {Count: 1, Since: "x"}, "b": {Count: 2, Since: "y"}, "c": {Count: 5, Since: "z"},
	}}
	got := RepeatedDegraded(stats)
	if len(got) != 2 || got["b"].Since != "y" || got["c"].Count != 5 {
		t.Errorf("RepeatedDegraded = %+v", got)
	}
	if got := RepeatedDegraded(HydrationStats{}); got == nil || len(got) != 0 {
		t.Errorf("empty stats = %+v", got)
	}
}
