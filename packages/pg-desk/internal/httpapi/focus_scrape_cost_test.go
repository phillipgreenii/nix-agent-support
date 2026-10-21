package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Bead pg2-yer0k: every /metrics scrape ran focus.ComputeHealth (a full store
// Load + Rank) synchronously, 3-30 s on a ~1000-entity store against a 10 s
// scrape_timeout. These tests pin that the scrape path serves a cached Health
// and that the cost no longer scales with the store.

// seedLargeFocusStore writes n entities (a third are beads linked to a PR
// source) in the shape seedFocusStore uses, plus a locked plan of a few rows.
func seedLargeFocusStore(t testing.TB, n int) *store.Store {
	t.Helper()
	s := store.OpenNewSchemaForTest(t)
	write := func(typ, id, facts string) {
		t.Helper()
		e := store.Entity{Repo: focusScrapeRepo, EntityType: typ, EntityID: id, Facts: facts, AsOf: focusAt, ContentHash: "h" + id}
		if _, err := s.WriteEntityStateWithLog(e, 0, focusAt, true, []string{"created"}, "sync", focusAt); err != nil {
			t.Fatal(err)
		}
	}
	prs := n * 2 / 3
	for i := 0; i < prs; i++ {
		write("pr", fmt.Sprint(i), `{"pr_show":{"state":"open","author":"someone"}}`)
	}
	for i := 0; i < n-prs; i++ {
		id := fmt.Sprintf("bd-%d", i)
		src := fmt.Sprint(i % prs)
		write("issue", id, fmt.Sprintf(`{"issue_show":{"id":%q,"state":"open","created_at":"2026-10-10T10:00:00Z","metadata":{"source_id":%q,"source_type":"pr"}}}`, id, src))
		if err := s.ReplaceDerivedXrefs(focusScrapeRepo, "issue", id, []store.XrefLink{{
			Repo: focusScrapeRepo, FromType: "issue", FromID: id, ToType: "pr", ToID: src,
			Relation: "source", Origin: "derived:test", FirstSeen: focusAt, LastConfirmed: focusAt,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertInterpretation(store.Interpretation{
		Repo: focusScrapeRepo, EntityType: "pr", EntityID: "0", Panel: PanelMineAwaitingMe, AsOf: focusAt,
	}); err != nil {
		t.Fatal(err)
	}
	err := s.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", "2026-10-10")
		if err != nil {
			return err
		}
		for i := 0; i < 6; i++ {
			if _, err := tx.InsertSelection(store.FocusSelection{
				FocusPeriodID: p.ID, Repo: focusScrapeRepo, EntityType: "pr", EntityID: fmt.Sprint(i),
				SelectedAt: "2026-10-10T10:30:00Z", RankPosition: i + 1,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// addFocusPlanRow stores a PR and selects it into the current plan: a
// recomputed Health reports one more selected row than a cached one.
func addFocusPlanRow(t *testing.T, s *store.Store, id string, pos int) {
	t.Helper()
	e := store.Entity{Repo: focusScrapeRepo, EntityType: "pr", EntityID: id, Facts: `{"pr_show":{"state":"open"}}`, AsOf: focusAt, ContentHash: "h" + id}
	if _, err := s.WriteEntityStateWithLog(e, 0, focusAt, true, []string{"created"}, "sync", focusAt); err != nil {
		t.Fatal(err)
	}
	err := s.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", "2026-10-10")
		if err != nil {
			return err
		}
		_, err = tx.InsertSelection(store.FocusSelection{
			FocusPeriodID: p.ID, Repo: focusScrapeRepo, EntityType: "pr", EntityID: id,
			SelectedAt: "2026-10-10T10:31:00Z", RankPosition: pos,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func scrapeBody(t testing.TB, h http.Handler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

// TestFocusScrapeDoesNotRecomputeHealthWithinTheTTL pins the serve wiring: a
// change to the store inside the TTL is not visible to the next scrape (the
// cached Health is served), and once the TTL has passed a scrape serves the
// old value at once and the following one the refreshed value.
func TestFocusScrapeDoesNotRecomputeHealthWithinTheTTL(t *testing.T) {
	s := seedFocusStore(t)
	h, err := NewHandler(s, focusConfig())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	const selected3, selected4 = "pg_desk_focus_selected 3", "pg_desk_focus_selected 4"
	if body := scrapeBody(t, h); !strings.Contains(body, selected3) {
		t.Fatalf("first scrape lacks %q:\n%s", selected3, focusOnly(body))
	}

	// Add a fourth plan row: a recomputation would now report 4.
	addFocusPlanRow(t, s, "extra", 4)
	for i := 0; i < 3; i++ {
		if body := scrapeBody(t, h); !strings.Contains(body, selected3) || strings.Contains(body, selected4) {
			t.Fatalf("scrape %d inside the TTL recomputed the health:\n%s", i, focusOnly(body))
		}
	}
}

func TestFocusScrapeRefreshesHealthAfterTheTTL(t *testing.T) {
	old := focusHealthTTL
	focusHealthTTL = 200 * time.Millisecond
	t.Cleanup(func() { focusHealthTTL = old })

	s := seedFocusStore(t)
	h, err := NewHandler(s, focusConfig())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if body := scrapeBody(t, h); !strings.Contains(body, "pg_desk_focus_selected 3") {
		t.Fatalf("first scrape:\n%s", focusOnly(body))
	}
	addFocusPlanRow(t, s, "extra", 4)
	// Each scrape past the TTL starts (or joins) one background refresh and is
	// served the previous value; a later scrape sees the refreshed one. The
	// polling gap is far below the TTL, so the scrape that sees the refreshed
	// value finds it fresh and starts no refresh that could outlive the test.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if body := scrapeBody(t, h); strings.Contains(body, "pg_desk_focus_selected 4") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the focus health never refreshed after the TTL")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFocusScrapeIsCheapOnALargeStore compares the compute path with the
// scrape path on a realistic ~1000-entity store. The cached scrape must not
// pay for ComputeHealth; the bound is generous (a loose factor plus an
// absolute ceiling far below the 10 s Prometheus scrape_timeout) so it does
// not flake under host load.
func TestFocusScrapeIsCheapOnALargeStore(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a ~1000-entity store")
	}
	s := seedLargeFocusStore(t, 1000)
	setClock(t, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	cfg := focusConfig()

	start := time.Now()
	hl, err := focus.ComputeHealth(s, cfg, nowUTC())
	compute := time.Since(start)
	if err != nil || !hl.Migrated() || hl.Selected != 6 {
		t.Fatalf("ComputeHealth = migrated %v, selected %d, %v", hl.Migrated(), hl.Selected, err)
	}

	h, err := NewHandler(s, cfg)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if body := scrapeBody(t, h); !strings.Contains(body, "pg_desk_focus_selected 6") { // warms the cache
		t.Fatalf("first scrape:\n%s", focusOnly(body))
	}
	var best time.Duration
	for i := 0; i < 5; i++ {
		start = time.Now()
		scrapeBody(t, h)
		if d := time.Since(start); i == 0 || d < best {
			best = d
		}
	}
	t.Logf("ComputeHealth on 1000 entities: %v; cached scrape (best of 5): %v", compute, best)
	if best > compute/2 {
		t.Errorf("cached scrape %v is not well under the compute path %v", best, compute)
	}
	if best > 2*time.Second {
		t.Errorf("cached scrape %v approaches the 10 s scrape_timeout", best)
	}
}

// BenchmarkFocusScrapeCached is the scrape path against the compute path on a
// ~1000-entity store: run with -bench FocusScrape to compare the two.
func BenchmarkFocusScrapeCached(b *testing.B) {
	s := seedLargeFocusStore(b, 1000)
	h, err := NewHandler(s, focusConfig())
	if err != nil {
		b.Fatal(err)
	}
	scrapeBody(b, h) // warm the cache
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scrapeBody(b, h)
	}
}

func BenchmarkFocusComputeHealth(b *testing.B) {
	s := seedLargeFocusStore(b, 1000)
	cfg := focusConfig()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := focus.ComputeHealth(s, cfg, now); err != nil {
			b.Fatal(err)
		}
	}
}
