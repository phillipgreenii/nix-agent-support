package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of the `focus:` block of doctor and status against a real migrated
// store, a fixed clock (2026-10-10 12:00 UTC) and the focus tests' show
// fixture. The plan rows the fixture locks are selected 3 hours before the
// clock, so they are older than the default 30m pending gate.

// doctorFocusWorld is a show fixture wired for doctor: the doctor seams are
// stubbed healthy, the bead id pattern matches bd-*, and every PR is in one
// watch query.
type doctorFocusWorld struct {
	*showFixture
	watched []string
}

func newDoctorFocusWorld(t *testing.T) *doctorFocusWorld {
	t.Helper()
	f := newShowFixture(t)
	f.cfg.BeadIDPattern = `^bd-`
	f.cfg.Watch.PR.Queries = []string{"mine"}
	stubDoctorSeams(t, nil, nil, nil)
	return &doctorFocusWorld{showFixture: f}
}

// openPR stores an open PR owned by the operator and lists it in the watch
// set.
func (w *doctorFocusWorld) openPR(t *testing.T, n int) focus.Key {
	t.Helper()
	k := w.pr(t, n, "t", showAt)
	w.watch(t, k)
	return k
}

// terminalPR stores a merged PR owned by the operator.
func (w *doctorFocusWorld) terminalPR(t *testing.T, n int) focus.Key {
	t.Helper()
	id := fmt.Sprintf("o/r#%d", n)
	k := w.write(t, "pr", id, map[string]any{"pr_show": map[string]any{"state": "closed", "merged": true, "author": "x"}}, showAt)
	if err := w.st.UpsertInterpretation(store.Interpretation{
		Repo: showRepo, EntityType: "pr", EntityID: id, Ownership: "mine",
		Enrichment: "{}", Urgency: "{}", Dispositions: "{}", Approvals: "{}", MatchReasons: "[]", AsOf: showAt,
	}); err != nil {
		t.Fatal(err)
	}
	w.watch(t, k)
	return k
}

// watch adds k to the stored watch set of the PR query.
func (w *doctorFocusWorld) watch(t *testing.T, k focus.Key) {
	t.Helper()
	w.watched = append(w.watched, fmt.Sprintf("%q", k.ID))
	if err := w.st.SetMeta("change_flow.watchset.pr.mine", "["+strings.Join(w.watched, ",")+"]"); err != nil {
		t.Fatal(err)
	}
}

// bead stores a focus bead of source and links it.
func (w *doctorFocusWorld) bead(t *testing.T, id, state, hold string, source focus.Key) {
	t.Helper()
	md := map[string]string{"source_id": source.ID, "source_type": source.Type}
	if hold != "" {
		md["focus_hold"] = hold
	}
	k := w.write(t, "issue", id, map[string]any{"issue_show": map[string]any{"id": id, "state": state, "metadata": md}}, showAt)
	if err := w.st.ReplaceDerivedXrefs(showRepo, k.Type, k.ID, []store.XrefLink{{
		Repo: showRepo, FromType: k.Type, FromID: k.ID, ToType: source.Type, ToID: source.ID,
		Relation: "source", Origin: "derived:test", FirstSeen: showAt, LastConfirmed: showAt,
	}}); err != nil {
		t.Fatal(err)
	}
}

// annotate sets the focus_selected annotation of k.
func (w *doctorFocusWorld) annotate(t *testing.T, k focus.Key, value string) {
	t.Helper()
	if err := w.st.SetAnnotation(store.KVAnnotation{
		Repo: showRepo, EntityType: k.Type, EntityID: k.ID, Key: store.AnnotationFocusSelected,
		Value: value, Origin: "pg-desk", SetBy: "operator", SetAt: showAt,
	}); err != nil {
		t.Fatal(err)
	}
}

// selected locks the keys into today's plan AND annotates each, the state a
// completed select leaves.
func (w *doctorFocusWorld) selected(t *testing.T, keys ...focus.Key) {
	t.Helper()
	w.lock(t, showToday, 5, keys...)
	for _, k := range keys {
		w.annotate(t, k, showToday)
	}
}

// addRow adds k to today's existing plan at the position and annotates it.
func (w *doctorFocusWorld) addRow(t *testing.T, k focus.Key, pos int) {
	t.Helper()
	if err := w.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", showToday)
		if err != nil {
			return err
		}
		tier := "not_started"
		_, err = tx.InsertSelection(store.FocusSelection{
			FocusPeriodID: p.ID, Repo: showRepo, EntityType: k.Type, EntityID: k.ID,
			SelectedAt: "2026-10-10T09:00:00Z", RankPosition: pos, Tier: &tier,
		})
		return err
	}); err != nil {
		t.Fatalf("addRow: %v", err)
	}
	w.annotate(t, k, showToday)
}

func TestDoctorFocusBlock(t *testing.T) {
	t.Run("a clean migrated store prints the block with every line labelled", func(t *testing.T) {
		w := newDoctorFocusWorld(t)
		k := w.openPR(t, 1)
		w.selected(t, k)
		w.bead(t, "bd-1", "open", "", k)
		stdout, err := runDoctorCmd(t)
		if err != nil {
			t.Fatalf("doctor: %v\n%s", err, stdout)
		}
		block := focusBlockOf(t, stdout)
		for _, want := range []string{
			"[gate] unminted selections: 0",
			"[gate] row_without_annotation divergence: 0",
			"[gate] selected_bead_held divergence: 0",
			"[gate] focus view contract: ok",
			"[report] coverage backlog:",
			"[report] degraded or failed listings:",
			"[report] unwatched selections:",
			"[report] removed from the plan, bead still in play:",
			"[report] held bead whose item is selected again:",
			"[report] bead minted and never claimed:",
			"[report] reconcile consumer lag: n/a (no --router-config)",
			"[report] audit comments written today: not available (the stored issue snapshot carries no comments)",
			"[report] escalated focus rules:",
			"[report] operator identity list:",
			"[report] open beads but zero bead candidates:",
			"[report] stored draft older than its period:",
			"[report] jira status category:",
		} {
			if !strings.Contains(block, want) {
				t.Errorf("focus block lacks %q:\n%s", want, block)
			}
		}
		for _, line := range strings.Split(block, "\n") {
			if strings.Contains(line, "] ") && !strings.Contains(line, "remedy:") {
				t.Errorf("a line has no one-line remedy: %q", line)
			}
		}
	})

	t.Run("an unmigrated store prints focus: unmigrated and skips", func(t *testing.T) {
		withDoctorStore(t, "o/r") // old schema
		stubDoctorSeams(t, nil, nil, nil)
		stdout, err := runDoctorCmd(t)
		if err != nil {
			t.Fatalf("doctor: %v\n%s", err, stdout)
		}
		if !hasLine(stdout, "focus: unmigrated") {
			t.Errorf("stdout lacks 'focus: unmigrated':\n%s", stdout)
		}
		if strings.Contains(stdout, "[gate]") {
			t.Errorf("an unmigrated store printed a gate line:\n%s", stdout)
		}
	})

	t.Run("the view-contract line is ok and then missing", func(t *testing.T) {
		w := newDoctorFocusWorld(t)
		k := w.openPR(t, 1)
		w.selected(t, k)
		w.bead(t, "bd-1", "open", "", k)
		stdout, err := runDoctorCmd(t)
		if err != nil || !strings.Contains(stdout, "focus view contract: ok") {
			t.Fatalf("want ok, got err=%v\n%s", err, stdout)
		}
		orig := focusViewJSON
		t.Cleanup(func() { focusViewJSON = orig })
		var probed string
		focusViewJSON = func(cfg *config.Config, st *store.Store, repo, typ, id string) ([]byte, error) {
			probed = typ + " " + id
			return []byte(`{"contract":"pg-desk.view/v1","annotations":{"hidden":{"value":false}}}`), nil
		}
		stdout, err = runDoctorCmd(t)
		if err == nil || !strings.Contains(err.Error(), focusGateViewContract) {
			t.Fatalf("doctor err = %v, want the view-contract gate\n%s", err, stdout)
		}
		if !strings.Contains(stdout, "focus view contract: missing annotations.focus_selected") {
			t.Errorf("stdout lacks the missing line:\n%s", stdout)
		}
		if probed != "pr o/r#1" {
			t.Errorf("probed %q, want the first row of the newest plan (pr o/r#1)", probed)
		}
	})

	t.Run("the view contract is n/a and never a gate with no entity", func(t *testing.T) {
		line, err := focusViewContractLine(&config.Config{Repos: []config.RepoConfig{{Remote: showRepo}}}, store.OpenNewSchemaForTest(t))
		if err != nil {
			t.Fatal(err)
		}
		if line.Tripped || !strings.Contains(line.String(), "focus view contract: n/a (no entity in the store)") {
			t.Errorf("line = %+v", line)
		}
	})
}

// focusBlockOf returns the focus: block of a doctor or status output.
func focusBlockOf(t *testing.T, stdout string) string {
	t.Helper()
	i := strings.Index(stdout, "\nfocus:\n")
	if i < 0 {
		t.Fatalf("no focus: block in:\n%s", stdout)
	}
	block := stdout[i+1:]
	var lines []string
	for _, l := range strings.Split(block, "\n") {
		if l != "focus:" && !strings.HasPrefix(l, "  ") {
			break
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

// TestFocusLinesGateVersusReport trips every condition on a hand-built health
// and checks that exactly the four gates trip, with their failure names, and
// that no report line ever trips.
func TestFocusLinesGateVersusReport(t *testing.T) {
	cfg := &config.Config{}
	now := focusFixedNow
	healthy := focus.Health{Coverage: focus.CoverageState{PerType: map[string]focus.TypeCoverage{"pr": {}}}}
	healthy.Pending = map[string]int{}
	healthy.OldestPendingAge = map[string]time.Duration{}
	healthy.Divergence = map[string]int{}
	healthy.Candidates = map[string]int{}
	okContract := focusLine{Name: "focus view contract", Kind: focusLineGate, Count: -1, GateName: focusGateViewContract}
	lag := focusLine{Name: "reconcile consumer lag", Kind: focusLineReport, Count: -1}

	tripped := func(h focus.Health, contract focusLine) map[string]bool {
		out := map[string]bool{}
		for _, l := range focusLines(h, cfg, now, contract, lag) {
			if l.Kind != focusLineGate && l.Kind != focusLineReport {
				t.Errorf("line %q has kind %q", l.Name, l.Kind)
			}
			if l.Tripped {
				if l.Kind != focusLineGate {
					t.Errorf("report line %q tripped", l.Name)
				}
				out[l.GateName] = true
			}
		}
		return out
	}
	if got := tripped(healthy, okContract); len(got) != 0 {
		t.Fatalf("a healthy model tripped %v", got)
	}

	// Every report condition at once: still no gate.
	reports := healthy
	reports.UnwatchedSelected, reports.RemovedWithBeadInPlay, reports.HeldButReselected = 3, 2, 1
	reports.MintedNeverClaimed, reports.EscalatedFocusRules, reports.JiraCategoryAbsent = 4, 1, 5
	reports.IdentityListEmpty, reports.OpenBeads, reports.DraftPeriodKey = true, 7, "2026-10-01"
	reports.Coverage = focus.CoverageState{PerType: map[string]focus.TypeCoverage{"pr": {Active: 10, DueBacklog: 9}}}
	reports.Listings = []focus.TypedListing{{Type: "pr", Listing: focus.Listing{Query: "mine", Status: "failed"}}}
	if got := tripped(reports, okContract); len(got) != 0 {
		t.Fatalf("report conditions tripped gates %v", got)
	}
	var sawIdentity, sawNoBead, sawDraft bool
	for _, l := range focusLines(reports, cfg, now, okContract, lag) {
		switch l.Name {
		case "operator identity list":
			sawIdentity = l.Count == 1 && l.Kind == focusLineReport
		case "open beads but zero bead candidates":
			sawNoBead = l.Count == 7 && l.Kind == focusLineReport
		case "stored draft older than its period":
			sawDraft = l.Count == 1
		}
	}
	if !sawIdentity || !sawNoBead || !sawDraft {
		t.Errorf("identity=%v nobead=%v draft=%v", sawIdentity, sawNoBead, sawDraft)
	}

	cases := []struct {
		name string
		edit func(*focus.Health)
		gate string
	}{
		{"unminted older than the gate age", func(h *focus.Health) {
			h.Pending = map[string]int{focus.PendingUnminted: 1}
			h.OldestPendingAge = map[string]time.Duration{focus.PendingUnminted: 31 * time.Minute}
		}, focusGateUnminted},
		{"row_without_annotation", func(h *focus.Health) {
			h.Divergence = map[string]int{focus.DivergenceRowWithoutAnnotation: 1}
		}, focusGateRowNoAnnot},
		{"selected_bead_held", func(h *focus.Health) {
			h.Divergence = map[string]int{focus.DivergenceSelectedBeadHeld: 1}
		}, focusGateSelectedHeld},
	}
	for _, c := range cases {
		h := healthy
		c.edit(&h)
		got := tripped(h, okContract)
		if len(got) != 1 || !got[c.gate] {
			t.Errorf("%s tripped %v, want only %q", c.name, got, c.gate)
		}
	}
	// An unminted selection younger than the gate age does not trip.
	young := healthy
	young.Pending = map[string]int{focus.PendingUnminted: 1}
	young.OldestPendingAge = map[string]time.Duration{focus.PendingUnminted: 29 * time.Minute}
	if got := tripped(young, okContract); len(got) != 0 {
		t.Errorf("an unminted selection of 29m tripped %v", got)
	}
	// The gate age is focus.pending_gate_age.
	cfg.Focus.PendingGateAge = "5m"
	if got := tripped(young, okContract); !got[focusGateUnminted] {
		t.Errorf("an unminted selection of 29m did not trip a 5m gate: %v", got)
	}
	cfg.Focus.PendingGateAge = ""
	// The view-contract gate.
	missing := okContract
	missing.Tripped = true
	if got := tripped(healthy, missing); len(got) != 1 || !got[focusGateViewContract] {
		t.Errorf("a missing view contract tripped %v", got)
	}
}

// TestDoctorExitsNonZeroExactlyWhenAGateTrips builds a store state per gate
// and checks doctor fails, naming it, for each and passes for the clean one.
func TestDoctorExitsNonZeroExactlyWhenAGateTrips(t *testing.T) {
	cases := []struct {
		name string
		gate string // "" = doctor must pass
		set  func(t *testing.T, w *doctorFocusWorld)
	}{
		{"clean", "", func(t *testing.T, w *doctorFocusWorld) {
			k := w.openPR(t, 1)
			w.selected(t, k)
			w.bead(t, "bd-1", "open", "", k)
		}},
		{"unminted over the gate age", focusGateUnminted, func(t *testing.T, w *doctorFocusWorld) {
			w.selected(t, w.openPR(t, 1))
		}},
		{"row without annotation", focusGateRowNoAnnot, func(t *testing.T, w *doctorFocusWorld) {
			k := w.openPR(t, 1)
			w.lock(t, showToday, 5, k)
			w.bead(t, "bd-1", "open", "", k)
		}},
		{"selected bead held", focusGateSelectedHeld, func(t *testing.T, w *doctorFocusWorld) {
			k := w.openPR(t, 1)
			w.selected(t, k)
			w.bead(t, "bd-1", "deferred", "struck", k)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newDoctorFocusWorld(t)
			c.set(t, w)
			stdout, err := runDoctorCmd(t)
			switch {
			case c.gate == "" && err != nil:
				t.Fatalf("doctor failed on a clean store: %v\n%s", err, stdout)
			case c.gate != "" && (err == nil || !strings.Contains(err.Error(), c.gate)):
				t.Fatalf("doctor err = %v, want a failure naming %q\n%s", err, c.gate, stdout)
			}
			if c.gate != "" && !strings.Contains(focusBlockOf(t, stdout), " FAIL") {
				t.Errorf("the tripped gate line is not marked FAIL:\n%s", stdout)
			}
		})
	}
}

// TestDoctorPendingGateIsUnminted: an epic or bd task, a hidden source and a
// terminal source each have no bead to wait for and MUST NOT trip the gate,
// however old their rows are; a live unwatched-free source with no bead does.
func TestDoctorPendingGateIsUnminted(t *testing.T) {
	w := newDoctorFocusWorld(t)
	epic := w.write(t, "issue", "bd-9", map[string]any{"issue_show": map[string]any{"id": "bd-9", "state": "open", "issue_type": "epic"}}, showAt)
	hidden := w.openPR(t, 2)
	if err := w.st.SetAnnotation(store.KVAnnotation{
		Repo: showRepo, EntityType: "pr", EntityID: hidden.ID, Key: store.AnnotationHidden,
		Value: `{"value":true,"reason":null}`, Origin: "pg-desk", SetBy: "operator", SetAt: showAt,
	}); err != nil {
		t.Fatal(err)
	}
	terminal := w.terminalPR(t, 3)
	w.selected(t, epic, hidden, terminal)

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("an epic, a hidden source and a terminal source tripped doctor: %v\n%s", err, stdout)
	}
	if !strings.Contains(focusBlockOf(t, stdout), "[gate] unminted selections: 0") {
		t.Errorf("unminted count is not 0:\n%s", stdout)
	}

	live := w.openPR(t, 4)
	w.addRow(t, live, 4)
	stdout, err = runDoctorCmd(t)
	if err == nil || !strings.Contains(err.Error(), focusGateUnminted) {
		t.Fatalf("doctor err = %v, want the unminted gate for a live source with no bead\n%s", err, stdout)
	}
	if !strings.Contains(focusBlockOf(t, stdout), "[gate] unminted selections: 1") {
		t.Errorf("unminted count is not 1:\n%s", stdout)
	}
}

func TestDoctorEmptyIdentityListIsAReportNotAGate(t *testing.T) {
	w := newDoctorFocusWorld(t)
	w.cfg.Focus.OperatorIdentities = nil
	k := w.openPR(t, 1)
	w.selected(t, k)
	w.bead(t, "bd-1", "open", "", k)
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("an empty identity list is a report, never a gate: %v\n%s", err, stdout)
	}
	if !strings.Contains(focusBlockOf(t, stdout), "[report] operator identity list: 1 (empty: no Jira issue is a candidate)") {
		t.Errorf("the identity report is missing:\n%s", stdout)
	}
	w.cfg.Focus.OperatorIdentities = []string{"Pat Example"}
	stdout, err = runDoctorCmd(t)
	if err != nil || !strings.Contains(focusBlockOf(t, stdout), "[report] operator identity list: 0") {
		t.Errorf("a configured list must report 0 (err=%v):\n%s", err, stdout)
	}
}

func TestOpenBeadsButZeroBeadCandidatesIsADoctorReport(t *testing.T) {
	w := newDoctorFocusWorld(t)
	// An open bead with no pg-focus-planable label: open beads exist and no
	// bead is a candidate.
	w.write(t, "issue", "bd-5", map[string]any{"issue_show": map[string]any{"id": "bd-5", "state": "open"}}, showAt)
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("a report, not a gate: %v\n%s", err, stdout)
	}
	block := focusBlockOf(t, stdout)
	if !strings.Contains(block, "[report] open beads but zero bead candidates: 1 (1 open beads, 0 bead candidates)") {
		t.Errorf("the report is missing or wrong:\n%s", block)
	}
	// Labelling it a seed clears the report.
	w.write(t, "issue", "bd-5", map[string]any{"issue_show": map[string]any{"id": "bd-5", "state": "open", "labels": []string{focus.PlanableLabel}}}, showAt)
	stdout, err = runDoctorCmd(t)
	if err != nil || !strings.Contains(focusBlockOf(t, stdout), "[report] open beads but zero bead candidates: 0") {
		t.Errorf("a labelled bead must clear the report (err=%v):\n%s", err, stdout)
	}
}

func TestDoctorFocusReportsNeverFail(t *testing.T) {
	w := newDoctorFocusWorld(t)
	// A bead minted and never claimed (open, unassigned, first seen 3h ago is
	// not what ages it: its tracker created_at is), an item removed from the
	// plan with its bead open, and a stored draft of an older day.
	k := w.openPR(t, 1)
	w.annotate(t, k, store.FocusAnnotationNone)
	w.bead(t, "bd-1", "open", "", k)
	if _, err := w.st.FocusDraftReplace(store.FocusDraft{PeriodType: "day", PeriodKey: "2026-10-01", Cap: 5, MadeAt: showAt, BodyJSON: `{}`}); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("reports must never fail doctor: %v\n%s", err, stdout)
	}
	block := focusBlockOf(t, stdout)
	for _, want := range []string{
		"[report] removed from the plan, bead still in play: 1",
		"[report] stored draft older than its period: 1",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("focus block lacks %q:\n%s", want, block)
		}
	}
}

func TestDoctorFocusLagLineReadsTheRouterConsumer(t *testing.T) {
	w := newDoctorFocusWorld(t)
	w.selected(t, w.openPR(t, 1))
	seed := w.st
	if err := seed.RegisterConsumer("pg-router", "issue", doctorNow.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	stdout, _ := runDoctorWithRouter(t, doctorRouterFixture)
	if !strings.Contains(focusBlockOf(t, stdout), "[report] reconcile consumer lag: issue/pg-router lag=0") {
		t.Errorf("the lag line does not name the router's issue consumer:\n%s", stdout)
	}
	if !strings.Contains(stdout, "focus role: nothing binds issue.changed") {
		t.Errorf("the router-role check does not name the focus role:\n%s", stdout)
	}
}

func TestStatusPrintsTheFocusBlock(t *testing.T) {
	w := newDoctorFocusWorld(t)
	w.selected(t, w.openPR(t, 1)) // unminted past the gate: status prints it, never fails on it
	orig := statusRunPgConnectorLedgerShow
	t.Cleanup(func() { statusRunPgConnectorLedgerShow = orig })
	statusRunPgConnectorLedgerShow = func(_ context.Context) (string, error) { return "", nil }
	stdout, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	block := focusBlockOf(t, stdout)
	if !strings.Contains(block, "[gate] unminted selections: 1") || !strings.Contains(block, "FAIL") {
		t.Errorf("status focus block does not show the tripped gate:\n%s", block)
	}
	if !strings.Contains(block, "[report] unwatched selections:") {
		t.Errorf("status focus block lacks the reports:\n%s", block)
	}
}

func TestStatusFocusBlockOnAnUnmigratedStore(t *testing.T) {
	withDoctorStore(t, "o/r")
	orig := statusRunPgConnectorLedgerShow
	t.Cleanup(func() { statusRunPgConnectorLedgerShow = orig })
	statusRunPgConnectorLedgerShow = func(_ context.Context) (string, error) { return "", nil }
	stdout, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !hasLine(stdout, "focus: unmigrated") {
		t.Errorf("status lacks 'focus: unmigrated':\n%s", stdout)
	}
}
