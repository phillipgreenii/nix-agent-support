package focus

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
)

// prunedWorld stores a mix of entities that exercises every key the focus
// decoders read: PRs (open, merged, with review requests), Jira-like issues
// with and without a status category, minted beads in several states with
// metadata, labels and created_at, and a plain bead.
func prunedWorld(t *testing.T) *healthWorld {
	t.Helper()
	w := newHealthWorld(t)
	w.cfg.Watch.Issue.Queries = []string{"mine"}
	today := fixtureNow.Format("2006-01-02")
	old := fixtureNow.Add(-5 * time.Hour).Format(time.RFC3339)
	p1 := w.pr("1", prSpec{ownership: "mine"})
	w.row(today, p1, 1, fixtureAt)
	w.pr("2", prSpec{ownership: "team"})
	w.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example"})
	w.issue("PROJ-2", issueSpec{state: "Done", category: "done", assignee: "Pat Example"})
	w.issue("PROJ-3", issueSpec{state: "In Progress", category: "indeterminate", assignee: "Pat Example"})
	w.issue("bd-1", issueSpec{state: "open", labels: []string{PlanableLabel}})
	w.issue("bd-plain", issueSpec{state: "open", createdAt: old})
	w.bead("bd-a", p1, beadSpec{state: "open", createdAt: old})
	w.bead("bd-c", w.pr("3", prSpec{ownership: "mine"}), beadSpec{state: "in_progress", assignee: "worker", createdAt: old})
	w.bead("bd-d", w.pr("4", prSpec{ownership: "mine"}), beadSpec{state: "deferred", hold: "struck", createdAt: old})
	w.bead("bd-g", w.pr("5", prSpec{ownership: "mine"}), beadSpec{state: "closed", createdAt: old})
	w.bead("bd-h", w.pr("6", prSpec{ownership: "mine"}), beadSpec{state: "open", createdAt: old, labels: []string{"human"}})
	w.inputs() // flush the fixture's derived links
	return w
}

// focus.Health from the full facts and from the pruned facts is identical.
func TestHealthFromPrunedFactsEqualsHealthFromFullFacts(t *testing.T) {
	w := prunedWorld(t)
	full, err := computeHealth(context.Background(), w.st, w.cfg, fixtureNow, Load)
	if err != nil {
		t.Fatal(err)
	}
	pruned, err := computeHealth(context.Background(), w.st, w.cfg, fixtureNow, LoadForHealth)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(full, pruned) {
		t.Fatalf("health differs:\nfull   %+v\npruned %+v", full, pruned)
	}
	if full.Beads["open"] == 0 || full.Candidates["jira"] == 0 {
		t.Fatalf("fixture too thin to prove anything: %+v", full)
	}
}

// Every decoder the health path runs over an entity's facts answers the same
// on the pruned document as on the full facts.
func TestEveryFocusDecoderAgreesOnPrunedFacts(t *testing.T) {
	w := prunedWorld(t)
	full, err := Load(context.Background(), w.st, w.cfg, testRepo, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	pruned, err := LoadForHealth(context.Background(), w.st, w.cfg, testRepo, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Entities) == 0 || len(full.Entities) != len(pruned.Entities) {
		t.Fatalf("%d full entities, %d pruned", len(full.Entities), len(pruned.Entities))
	}
	for k, fe := range full.Entities {
		pe := pruned.Entities[k]
		if !reflect.DeepEqual(decodeEntityFacts(fe), decodeEntityFacts(pe)) {
			t.Errorf("%v: decodeEntityFacts differ:\nfull   %+v\npruned %+v", k, decodeEntityFacts(fe), decodeEntityFacts(pe))
		}
		var fv, pv view
		fv.key, pv.key = k, k
		decodeFacts(fe.Facts, &fv)
		decodeFacts(pe.Facts, &pv)
		if !reflect.DeepEqual(fv, pv) {
			t.Errorf("%v: decodeFacts differ:\nfull   %+v\npruned %+v", k, fv, pv)
		}
		for _, now := range []time.Time{fixtureNow} {
			if a, b := classify.SourceTerminal(k.Type, json.RawMessage(fe.Facts), now, 0), classify.SourceTerminal(k.Type, json.RawMessage(pe.Facts), now, 0); a != b {
				t.Errorf("%v: SourceTerminal full %v pruned %v", k, a, b)
			}
		}
		fk, fc := classify.IssueStatusKind(json.RawMessage(fe.Facts))
		pk, pc := classify.IssueStatusKind(json.RawMessage(pe.Facts))
		if fk != pk || fc != pc {
			t.Errorf("%v: IssueStatusKind full (%v,%v) pruned (%v,%v)", k, fk, fc, pk, pc)
		}
		// Everything but the facts is the same.
		fe.Facts, pe.Facts = "", ""
		if !reflect.DeepEqual(fe, pe) {
			t.Errorf("%v: non-facts fields differ:\n%+v\n%+v", k, fe, pe)
		}
	}
}
