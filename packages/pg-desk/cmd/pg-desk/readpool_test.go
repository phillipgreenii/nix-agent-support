package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/httpapi"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// seedReadPoolStore builds one store at the default path (under a temp
// XDG_STATE_HOME) with a known change flow: two active prs, two consumers at
// different cursors and a degraded-hydration history.
func seedReadPoolStore(t *testing.T) {
	t.Helper()
	store.SetSynchronousForTests("OFF")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st, err := store.Open(store.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	write := func(id string, expected int64, kinds []string) {
		t.Helper()
		e := store.Entity{Repo: "o/r", EntityType: "pr", EntityID: id, Facts: `{}`, AsOf: "x"}
		if _, err := st.WriteEntityStateWithLog(context.Background(), e, expected, "2026-10-01T11:00:00Z", true, kinds, "poll", "2026-10-01T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	write("o/r#1", 0, []string{"created"})
	write("o/r#2", 0, []string{"created"})
	write("o/r#1", 1, []string{"labels"})
	for name, cursor := range map[string]int64{"alpha": 1, "beta": 3} {
		if err := st.RegisterConsumer(context.Background(), name, "pr", time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		if err := st.AdvanceCursor(context.Background(), name, "pr", cursor); err != nil {
			t.Fatal(err)
		}
	}
	changes.RecordHydration(context.Background(), st, "pr", "o/r#1", pipeline.EntityChangeResult{Retries: 2}, nil)
	changes.RecordHydration(context.Background(), st, "pr", "o/r#2", pipeline.EntityChangeResult{}, errors.New("boom"))
	// One interpretation row so /metrics' ready gate answers.
	if err := st.UpsertInterpretation(context.Background(), store.Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: httpapi.PanelMineAwaitingMe, AsOf: "2026-10-01T11:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
}

// TestStatusDoctorAndMetricsAgreeOverOneReadPoolStore runs status, doctor and
// /metrics against ONE store, each through the read-only pool, and checks the
// change-flow values they all derive from changes.Observe agree.
func TestStatusDoctorAndMetricsAgreeOverOneReadPoolStore(t *testing.T) {
	seedReadPoolStore(t)
	withOpenSeams(t, openTestConfig("o/r"), func() (*store.Store, error) { return store.Open(store.DefaultPath()) })
	// Restore the production seam: the point is the default wiring.
	deskStoreOpenReadPool = func() (*store.Store, error) {
		return store.OpenReadPool(store.DefaultPath(), 3)
	}
	stubDoctorSeams(t, nil, nil, nil)

	statusText, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	// The seeded consumers' liveness stamps are old, so doctor may report them
	// as stalled and exit non-zero; its report is what is compared here.
	doctorText, _ := runDoctorCmd(t)
	if strings.Contains(doctorText, "FAIL (open store") || strings.Contains(doctorText, "observe: FAIL") {
		t.Fatalf("doctor could not read the store:\n%s", doctorText)
	}

	pool, err := store.OpenReadPool(store.DefaultPath(), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close() }()
	h, err := httpapi.NewHandler(context.Background(), pool, openTestConfig("o/r"))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil))
	metricsText := rec.Body.String()

	for _, w := range []struct{ name, text, want string }{
		{"status", statusText, "interpretations: 1\n"},
		{"status", statusText, "    active: 2\n"},
		{"status", statusText, "    hydrations: 2\n"},
		{"status", statusText, "    hydration_failures: 1\n"},
		{"status", statusText, "    occ_retries: 2\n"},
		{"status", statusText, "alpha cursor=1 lag=2 "},
		{"status", statusText, "beta cursor=3 lag=0 "},
		{"doctor", doctorText, "pr/alpha:"},
		{"doctor", doctorText, "pr/beta:"},
		{"metrics", metricsText, `pg_desk_consumer_lag{consumer="alpha",type="pr"} 2`},
		{"metrics", metricsText, `pg_desk_consumer_lag{consumer="beta",type="pr"} 0`},
		{"metrics", metricsText, `pg_desk_hydrations_total{type="pr"} 2`},
		{"metrics", metricsText, `pg_desk_hydration_failures_total{type="pr"} 1`},
		{"metrics", metricsText, `pg_desk_occ_retries_total{type="pr"} 2`},
	} {
		if !strings.Contains(w.text, w.want) {
			t.Errorf("%s lacks %q:\n%s", w.name, w.want, w.text)
		}
	}
}

// TestDefaultStatusDoctorSeamIsReadOnly pins the production wiring: with the
// seam at its default and the store present, the handle rejects writes, so
// status and doctor cannot hold the writer's pragmas or connection.
func TestDefaultStatusDoctorSeamIsReadOnly(t *testing.T) {
	seedReadPoolStore(t)
	st, err := deskStoreOpenReadPool()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.SetMeta(context.Background(), "probe", "x"); err == nil {
		t.Fatal("status/doctor seam handle accepted a write; it must be the read-only pool")
	}
}

// TestStatusAndDoctorDoNotOpenWithOpenRaw fails if status or doctor reach for
// store.OpenRaw (or the pre-move seam) again: the only OpenRaw in the command
// package's production code is the missing-file fallback in desk.go.
func TestStatusAndDoctorDoNotOpenWithOpenRaw(t *testing.T) {
	for _, f := range []string{"status.go", "doctor.go", "doctor_changeflow.go", "doctor_focus.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "OpenRaw") {
			t.Errorf("%s references OpenRaw; status and doctor must open through deskStoreOpenReadPool", f)
		}
	}
}
