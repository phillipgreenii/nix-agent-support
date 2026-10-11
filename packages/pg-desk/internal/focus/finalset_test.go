package focus

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// --- builders ---------------------------------------------------------------

func fk(n string) Key { return prK(n) }

func jk(id string) Key { return Key{"issue", id} }

// drow is a draft row with a tier, proposed when plus.
func drow(k Key, plus bool) DraftRow {
	return DraftRow{Key: k, Kind: KindPR, Tier: "t1", Plus: plus}
}

func draftOf(rows ...DraftRow) *DraftDoc {
	return &DraftDoc{Contract: DraftContract, Mode: DraftMode, Rows: rows}
}

func prow(k Key, pos int) PlanRow {
	return PlanRow{Key: k, RankPosition: pos, SelectedAt: "2026-10-01T00:00:00Z"}
}

func reply(t *testing.T, s string) Reply {
	t.Helper()
	r, err := ParseReply(strings.NewReader(s))
	if err != nil {
		t.Fatalf("ParseReply(%q): %v", s, err)
	}
	return r
}

func candidates(keys ...Key) CandidateSet {
	var set CandidateSet
	for _, k := range keys {
		set.Candidates = append(set.Candidates, Candidate{Key: k, Kind: KindPR, Seed: true})
	}
	return set
}

func baseInput(t *testing.T, replyText string) FinalSetInput {
	t.Helper()
	return FinalSetInput{
		Reply:      reply(t, replyText),
		Facts:      LiveFacts{},
		DefaultCap: 6,
		Config:     keyCfg(),
	}
}

func mustFinal(t *testing.T, in FinalSetInput) FinalSet {
	t.Helper()
	fs, err := ComputeFinalSet(in)
	if err != nil {
		t.Fatalf("ComputeFinalSet: %v", err)
	}
	return fs
}

func insertKeys(fs FinalSet) []Key {
	var out []Key
	for _, i := range fs.Insert {
		out = append(out, i.Key)
	}
	return out
}

func deleted(fs FinalSet) map[Key]string {
	out := map[Key]string{}
	for _, d := range fs.Delete {
		out[d.Key] = d.Cause
	}
	return out
}

func skippedReason(fs FinalSet, k Key) string {
	for _, s := range fs.Skipped {
		if s.Key == k {
			return s.Reason
		}
	}
	return ""
}

func wantFinalKeys(t *testing.T, label string, got, want []Key) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func intIs(p *int, want int) bool { return p != nil && *p == want }

// planRows builds n plan rows for PRs 1..n at positions 1..n.
func planRows(n int) []PlanRow {
	var rows []PlanRow
	for i := 1; i <= n; i++ {
		rows = append(rows, prow(fk(strconv.Itoa(i)), i))
	}
	return rows
}

// replanDraft is a draft over a plan: other candidates, none proposed.
func replanDraft(keys ...Key) *DraftDoc {
	var rows []DraftRow
	for _, k := range keys {
		rows = append(rows, drow(k, false))
	}
	return draftOf(rows...)
}

// --- first lock -------------------------------------------------------------

func TestBareOkLocksTheDraftsProposalsAtFirstLock(t *testing.T) {
	in := baseInput(t, "ok")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), false))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1"), fk("2")})
	for _, i := range fs.Insert {
		if i.Source != SourceDraft || i.Tier == nil || *i.Tier != "t1" {
			t.Errorf("insertion %+v, want draft source with the draft's tier", i)
		}
	}
	if len(fs.Delete) != 0 || len(fs.Forced) != 0 || len(fs.HandAdded) != 0 || len(fs.Skipped) != 0 {
		t.Errorf("unexpected extras: %+v", fs)
	}
}

func TestPersistedCapIsReplyCapElseFirstLockDraftCapElseUnchanged(t *testing.T) {
	cases := []struct {
		name     string
		reply    string
		rows     bool
		periodCp *int
		draftCp  *int
		want     *int
	}{
		{"reply cap at first lock", "ok cap=4", false, intp(5), intp(3), intp(4)},
		{"reply cap over a plan", "cap=2", true, intp(5), intp(3), intp(2)},
		{"first lock draft cap", "ok", false, intp(5), intp(3), intp(3)},
		{"first lock period cap", "ok", false, intp(5), nil, intp(5)},
		{"first lock default", "ok", false, nil, nil, intp(6)},
		{"over a plan unchanged", "ok", true, intp(5), intp(3), nil},
		{"over a plan with a draft cap unchanged", "ok -bd-9", true, nil, intp(3), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput(t, tc.reply)
			in.PeriodCap, in.DraftCap = tc.periodCp, tc.draftCp
			in.PeriodHasRows = tc.rows
			in.AnyPeriodRows = map[Key]bool{jk("bd-9"): true}
			if tc.rows {
				in.Plan = planRows(2)
				in.Draft = replanDraft()
			} else {
				in.Draft = draftOf(drow(fk("1"), true))
			}
			fs := mustFinal(t, in)
			switch {
			case tc.want == nil && fs.PersistCap != nil:
				t.Errorf("PersistCap = %d, want unchanged (nil)", *fs.PersistCap)
			case tc.want != nil && !intIs(fs.PersistCap, *tc.want):
				t.Errorf("PersistCap = %v, want %d", fs.PersistCap, *tc.want)
			}
		})
	}
}

func TestCapPrecedenceReplyThenDraftThenPeriodThenSix(t *testing.T) {
	var rows []DraftRow
	for i := 1; i <= 9; i++ {
		rows = append(rows, drow(fk(strconv.Itoa(i)), i <= 7))
	}
	cases := []struct {
		name      string
		reply     string
		draftCap  *int
		periodCap *int
		defCap    int
		persist   int
		inserts   int // with a reply cap the window is that many rows; otherwise the + rows
	}{
		{"reply wins", "ok cap=2", intp(4), intp(5), 6, 2, 2},
		{"draft over period", "ok", intp(4), intp(5), 6, 4, 7},
		{"period over default", "ok", nil, intp(5), 6, 5, 7},
		{"default is six", "ok", nil, nil, 0, 6, 7},
		{"configured default", "ok", nil, nil, 8, 8, 7},
		{"reply over default", "cap=8", nil, nil, 0, 8, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput(t, tc.reply)
			in.Draft = draftOf(rows...)
			in.DraftCap, in.PeriodCap, in.DefaultCap = tc.draftCap, tc.periodCap, tc.defCap
			fs := mustFinal(t, in)
			if !intIs(fs.PersistCap, tc.persist) {
				t.Errorf("PersistCap = %v, want %d", fs.PersistCap, tc.persist)
			}
			if fs.EffectiveCap != tc.persist {
				t.Errorf("EffectiveCap = %d, want %d", fs.EffectiveCap, tc.persist)
			}
			if len(fs.Insert) != tc.inserts {
				t.Errorf("inserted %d rows, want %d", len(fs.Insert), tc.inserts)
			}
		})
	}
}

// Traces INV-FOCUS-20: the final set is computed from the draft's order.
func TestReplyCapRetakesTheTopOfTheSameDraftOrder(t *testing.T) {
	in := baseInput(t, "cap=3")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), false), drow(fk("4"), false), drow(fk("5"), false))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1"), fk("2"), fk("3")})
	if len(fs.Forced) != 0 {
		t.Errorf("a row inside the retaken top must not be forced: %v", fs.Forced)
	}
}

func TestFinishedDraftRowIsSkippedAtLock(t *testing.T) {
	in := baseInput(t, "ok")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), false))
	in.Facts[fk("1")] = Facts{Finished: true}
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("2")})
	if got := skippedReason(fs, fk("1")); got != SkipFinished {
		t.Errorf("skip of the finished row = %q, want %q", got, SkipFinished)
	}

	// A finished row does not count toward a reply cap: the window reaches
	// one row further.
	in = baseInput(t, "cap=2")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), false), drow(fk("4"), false))
	in.Facts[fk("1")] = Facts{Finished: true}
	fs = mustFinal(t, in)
	wantFinalKeys(t, "Insert under cap=2", insertKeys(fs), []Key{fk("2"), fk("3")})

	// A row the draft marked finished is finished when the live facts say
	// nothing about it.
	in = baseInput(t, "ok")
	r := drow(fk("1"), true)
	r.Finished = true
	in.Draft = draftOf(r, drow(fk("2"), true))
	fs = mustFinal(t, in)
	wantFinalKeys(t, "Insert with a marked row", insertKeys(fs), []Key{fk("2")})
	// ... but live facts win over the mark (the item reopened).
	in.Facts[fk("1")] = Facts{}
	fs = mustFinal(t, in)
	wantFinalKeys(t, "Insert with a reopened row", insertKeys(fs), []Key{fk("1"), fk("2")})
}

func TestLostProposalIsSkippedAndNotBackfilled(t *testing.T) {
	in := baseInput(t, "ok")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), false))
	in.Facts[fk("1")] = Facts{Hidden: true}
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("2")})
	if got := skippedReason(fs, fk("1")); got != SkipNoLongerCandidate {
		t.Errorf("skip = %q, want %q", got, SkipNoLongerCandidate)
	}
}

func TestStrikeOfNeverSelectedProposalWritesNoRow(t *testing.T) {
	in := baseInput(t, "ok -2")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), true), drow(fk("4"), false))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1"), fk("3")})
	if len(fs.Delete) != 0 {
		t.Errorf("a strike of a never-selected proposal wrote a deletion: %+v", fs.Delete)
	}
	// Nothing backfills the freed slot, even with a reply cap that would reach
	// row 4.
	in = baseInput(t, "-2 cap=3")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), true), drow(fk("4"), false))
	fs = mustFinal(t, in)
	wantFinalKeys(t, "Insert under cap=3", insertKeys(fs), []Key{fk("1"), fk("3")})
}

func TestStrikeOfALiveCandidateNotInTheDraftIsQuiet(t *testing.T) {
	in := baseInput(t, "-7")
	in.Draft = draftOf(drow(fk("1"), true))
	in.Candidates = candidates(fk("1"), fk("7"))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1")})
	if len(fs.Delete) != 0 || len(fs.Skipped) != 0 {
		t.Errorf("unexpected effects: %+v", fs)
	}
}

func TestMinusOfAnUnknownKeyIsAUsageError(t *testing.T) {
	in := baseInput(t, "-77")
	in.Draft = draftOf(drow(fk("1"), true))
	in.Candidates = candidates(fk("1"))
	fs, err := ComputeFinalSet(in)
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "no candidate and no row") {
		t.Fatalf("err = %v, want a usage error naming the missing candidate", err)
	}
	if !reflect.DeepEqual(fs, FinalSet{}) {
		t.Errorf("an error returned a non-zero FinalSet: %+v", fs)
	}
	// A key that exists only as a row of another period is accepted and
	// deletes it, in every period.
	in.AnyPeriodRows = map[Key]bool{fk("77"): true}
	fs = mustFinal(t, in)
	if got := deleted(fs)[fk("77")]; got != CauseOperator {
		t.Errorf("deletion cause = %q, want %q", got, CauseOperator)
	}
}

func TestUnresolvableKeyIsAUsageError(t *testing.T) {
	in := baseInput(t, "+nonsense")
	_, err := ComputeFinalSet(in)
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
	in = baseInput(t, "-nonsense")
	if _, err = ComputeFinalSet(in); !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

func TestConflictingFormsOfOneKeyAreAUsageError(t *testing.T) {
	in := baseInput(t, "-5 +acme/widgets#5")
	_, err := ComputeFinalSet(in)
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("err = %v, want a conflict", err)
	}
	// Two forms of one key on the SAME side collapse to one.
	in = baseInput(t, "+5 +acme/widgets#5")
	in.Candidates = candidates(fk("5"))
	in.Draft = draftOf(drow(fk("5"), false))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Forced", fs.Forced, []Key{fk("5")})
}

// --- plus -------------------------------------------------------------------

func TestPlusOfCandidateNewSinceDraftIsForced(t *testing.T) {
	in := baseInput(t, "ok +8")
	in.Draft = draftOf(drow(fk("1"), true))
	in.Candidates = candidates(fk("1"), fk("8"))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1"), fk("8")})
	wantFinalKeys(t, "Forced", fs.Forced, []Key{fk("8")})
	if len(fs.HandAdded) != 0 {
		t.Errorf("a live candidate is forced, not hand-added: %v", fs.HandAdded)
	}
	if got := fs.Insert[1]; got.Source != SourceForce || got.Tier != nil {
		t.Errorf("insertion %+v, want a force source with no tier", got)
	}
	if fs.EffectiveCap != 7 {
		t.Errorf("EffectiveCap = %d, want 7 (draft cap 6 raised by the force-pull)", fs.EffectiveCap)
	}
}

func TestPlusOfAnUnproposedDraftRowIsForcedWithItsTier(t *testing.T) {
	in := baseInput(t, "+3")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), false), drow(fk("3"), false))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1"), fk("3")})
	wantFinalKeys(t, "Forced", fs.Forced, []Key{fk("3")})
	if got := fs.Insert[1]; got.Tier == nil || *got.Tier != "t1" {
		t.Errorf("force-pull tier = %v, want the draft's", got.Tier)
	}
}

func TestPlusOfAProposedRowChangesNothing(t *testing.T) {
	in := baseInput(t, "+1")
	in.Draft = draftOf(drow(fk("1"), true))
	fs := mustFinal(t, in)
	if len(fs.Forced) != 0 || len(fs.Skipped) != 0 || len(fs.Insert) != 1 || fs.Insert[0].Source != SourceDraft {
		t.Errorf("+ of a proposal changed something: %+v", fs)
	}
	if fs.EffectiveCap != 6 {
		t.Errorf("EffectiveCap = %d, want 6 (a proposal does not raise it)", fs.EffectiveCap)
	}
}

func TestHandAddOfAnyKeyThatIsNoCandidate(t *testing.T) {
	in := baseInput(t, "ok +bd-zzz +PROJ-7")
	in.Draft = draftOf(drow(fk("1"), true))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "HandAdded", fs.HandAdded, []Key{jk("bd-zzz"), jk("PROJ-7")})
	for _, i := range fs.Insert[1:] {
		if i.Source != SourceHandAdd || i.Tier != nil {
			t.Errorf("insertion %+v, want a handadd source with no tier", i)
		}
	}
	if fs.EffectiveCap != 8 {
		t.Errorf("EffectiveCap = %d, want 8", fs.EffectiveCap)
	}
	// With no draft at all the hand-add still works.
	in = baseInput(t, "+bd-zzz")
	fs = mustFinal(t, in)
	wantFinalKeys(t, "Insert with no draft", insertKeys(fs), []Key{jk("bd-zzz")})
}

func TestHandAddOfClosedOrHiddenIsAUsageError(t *testing.T) {
	for name, f := range map[string]Facts{
		"finished": {Finished: true},
		"closed":   {Finished: true, Inactive: true},
		"hidden":   {Hidden: true},
	} {
		t.Run(name, func(t *testing.T) {
			in := baseInput(t, "ok +bd-zzz")
			in.Draft = draftOf(drow(fk("1"), true))
			in.Facts[jk("bd-zzz")] = f
			fs, err := ComputeFinalSet(in)
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("err = %v, want ErrUsage", err)
			}
			if !reflect.DeepEqual(fs, FinalSet{}) {
				t.Errorf("an error returned a non-zero FinalSet")
			}
		})
	}
	// An inactive, unfinished hand-add is accepted: hydration reactivates it.
	in := baseInput(t, "+bd-zzz")
	in.Facts[jk("bd-zzz")] = Facts{Inactive: true}
	if _, err := ComputeFinalSet(in); err != nil {
		t.Fatalf("an inactive hand-add: %v", err)
	}
}

func TestPlusEpicKeyWithOpenChildIsUsageError(t *testing.T) {
	epic, child := jk("bd-epic"), jk("bd-kid")
	open := func(k Key) (Key, bool) {
		if k == epic {
			return child, true
		}
		return Key{}, false
	}
	in := baseInput(t, "+bd-epic")
	in.OpenChild = open
	_, err := ComputeFinalSet(in)
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "bd-kid") {
		t.Fatalf("err = %v, want a usage error naming the child", err)
	}
	// The child itself is fine, and so is an epic with no open child.
	in = baseInput(t, "+bd-kid +bd-other")
	in.OpenChild = open
	if _, err := ComputeFinalSet(in); err != nil {
		t.Fatalf("err = %v", err)
	}
	// An epic that is already a plan row is a routine skip, not an error.
	in = baseInput(t, "+bd-epic")
	in.OpenChild = open
	in.PeriodHasRows = true
	in.Plan = []PlanRow{prow(epic, 1)}
	fs := mustFinal(t, in)
	if got := skippedReason(fs, epic); got != SkipAlreadySelected {
		t.Errorf("skip = %q, want %q", got, SkipAlreadySelected)
	}
}

func TestPlusAndMinusOnFinishedOrLostRow(t *testing.T) {
	plan := []PlanRow{prow(fk("1"), 1), prow(fk("2"), 2), prow(fk("3"), 3)}
	facts := LiveFacts{fk("1"): {Finished: true}, fk("2"): {Hidden: true}}

	in := baseInput(t, "+1 +2 +3")
	in.PeriodHasRows, in.Plan, in.Facts = true, plan, facts
	in.Draft = replanDraft()
	fs := mustFinal(t, in)
	if skippedReason(fs, fk("1")) != SkipFinished || skippedReason(fs, fk("2")) != SkipNoLongerCandidate ||
		skippedReason(fs, fk("3")) != SkipAlreadySelected {
		t.Errorf("skips = %+v", fs.Skipped)
	}
	if len(fs.Insert) != 0 {
		t.Errorf("a routine skip inserted %v", fs.Insert)
	}

	in = baseInput(t, "-1 -2 -3")
	in.PeriodHasRows, in.Plan, in.Facts = true, plan, facts
	in.Draft = replanDraft()
	fs = mustFinal(t, in)
	if skippedReason(fs, fk("1")) != SkipFinished || skippedReason(fs, fk("2")) != SkipNoLongerCandidate {
		t.Errorf("skips = %+v", fs.Skipped)
	}
	got := deleted(fs)
	if _, ok := got[fk("1")]; ok {
		t.Errorf("a finished row was deleted")
	}
	// The hidden row is still dropped by its own cause; the live one is struck.
	if got[fk("2")] != CauseDropped || got[fk("3")] != CauseOperator {
		t.Errorf("deletions = %v", got)
	}
	// A draft row finished or lost: -key is a no-op skip.
	in = baseInput(t, "-1 -2")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true))
	in.Facts = LiveFacts{fk("1"): {Finished: true}, fk("2"): {Inactive: true}}
	fs = mustFinal(t, in)
	if skippedReason(fs, fk("1")) != SkipFinished || skippedReason(fs, fk("2")) != SkipNoLongerCandidate {
		t.Errorf("draft-row skips = %+v", fs.Skipped)
	}
	if len(fs.Delete) != 0 || len(fs.Insert) != 0 {
		t.Errorf("a no-op minus had effects: %+v", fs)
	}
}

func TestPlusOfANeverSeenFinishedCandidateIsASkip(t *testing.T) {
	in := baseInput(t, "+8 +9")
	in.Candidates = candidates(fk("8"), fk("9"))
	in.Facts = LiveFacts{fk("8"): {Finished: true}, fk("9"): {Inactive: true}}
	fs := mustFinal(t, in)
	if skippedReason(fs, fk("8")) != SkipFinished || skippedReason(fs, fk("9")) != SkipNoLongerCandidate {
		t.Errorf("skips = %+v", fs.Skipped)
	}
	if len(fs.Insert) != 0 {
		t.Errorf("inserted %v", fs.Insert)
	}
}

// --- a plan exists ----------------------------------------------------------

// Traces INV-FOCUS-20: a bare ok over a plan never removes or adds by rank.
func TestBareOkOverPlanNeverRemovesOrAdds(t *testing.T) {
	in := baseInput(t, "ok")
	in.PeriodHasRows, in.Plan = true, planRows(4)
	in.PeriodCap = intp(4)
	in.Draft = replanDraft(fk("20"), fk("21"))
	fs := mustFinal(t, in)
	if len(fs.Insert) != 0 || len(fs.Delete) != 0 || len(fs.Forced) != 0 || len(fs.Skipped) != 0 {
		t.Errorf("a bare ok over a replan draft changed the plan: %+v", fs)
	}
	if fs.PersistCap != nil {
		t.Errorf("PersistCap = %d, want unchanged", *fs.PersistCap)
	}
	// Even with a cap=N above, a replan draft proposes nothing.
	in.Reply = reply(t, "cap=9")
	fs = mustFinal(t, in)
	if len(fs.Insert) != 0 || len(fs.Delete) != 0 {
		t.Errorf("cap=9 over a replan draft changed the plan: %+v", fs)
	}
	if !intIs(fs.PersistCap, 9) || fs.EffectiveCap != 9 {
		t.Errorf("cap=9: PersistCap %v EffectiveCap %d", fs.PersistCap, fs.EffectiveCap)
	}
}

// Traces INV-FOCUS-20: the final set does not recompute the rank.
func TestReplanCapThenBareOkNeverRemoves(t *testing.T) {
	// replan --cap 3 moved the displayed line only: the draft carries cap 3,
	// the period cap is still 6, the plan holds 5 rows, and a bare ok removes
	// nothing and persists nothing.
	in := baseInput(t, "ok")
	in.PeriodHasRows, in.Plan = true, planRows(5)
	in.PeriodCap, in.DraftCap = intp(6), intp(3)
	in.Draft = replanDraft(fk("20"))
	fs := mustFinal(t, in)
	if len(fs.Delete) != 0 || fs.PersistCap != nil || len(fs.Insert) != 0 {
		t.Errorf("replan --cap 3 then ok: %+v", fs)
	}
	if fs.EffectiveCap != 6 {
		t.Errorf("EffectiveCap = %d, want 6", fs.EffectiveCap)
	}
}

func TestBareOkLocksUnlockedProposalsOfAFirstDraftAfterAPull(t *testing.T) {
	in := baseInput(t, "ok")
	in.PeriodHasRows, in.Plan = true, []PlanRow{prow(fk("2"), 1)}
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), true), drow(fk("4"), false))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1"), fk("3")})
	if len(fs.Delete) != 0 {
		t.Errorf("deleted %+v", fs.Delete)
	}
	// With a reply cap the top N of the draft's order is the proposed set.
	in.Reply = reply(t, "cap=4")
	fs = mustFinal(t, in)
	wantFinalKeys(t, "Insert under cap=4", insertKeys(fs), []Key{fk("1"), fk("3"), fk("4")})
}

// Traces INV-FOCUS-21: a cap shrink deletes the highest positions only.
func TestCapShrinkRemovesHighestPositionsOnly(t *testing.T) {
	in := baseInput(t, "cap=2")
	in.PeriodHasRows, in.Plan = true, planRows(5)
	in.PeriodCap = intp(6)
	in.Draft = replanDraft()
	fs := mustFinal(t, in)
	want := []Deletion{{fk("5"), CauseCap}, {fk("4"), CauseCap}, {fk("3"), CauseCap}}
	if !reflect.DeepEqual(fs.Delete, want) {
		t.Errorf("Delete = %+v, want %+v", fs.Delete, want)
	}
	if !intIs(fs.PersistCap, 2) {
		t.Errorf("PersistCap = %v, want 2", fs.PersistCap)
	}
	if fs.EffectiveCap != 2 {
		t.Errorf("EffectiveCap = %d, want 2", fs.EffectiveCap)
	}
	// The order of the plan input does not matter: positions decide.
	in.Plan = []PlanRow{prow(fk("4"), 4), prow(fk("1"), 1), prow(fk("5"), 5), prow(fk("3"), 3), prow(fk("2"), 2)}
	fs = mustFinal(t, in)
	if !reflect.DeepEqual(fs.Delete, want) {
		t.Errorf("shuffled plan: Delete = %+v, want %+v", fs.Delete, want)
	}
}

func TestLowerCapDeletesTheHighestUnfinishedPositions(t *testing.T) {
	in := baseInput(t, "cap=2")
	in.PeriodHasRows = true
	in.Plan = planRows(5)
	in.PeriodCap = intp(6)
	in.Draft = replanDraft()
	// Row 2 finished: it does not count, so rows 1, 3 stay and 4, 5 go.
	in.Facts[fk("2")] = Facts{Finished: true}
	fs := mustFinal(t, in)
	got := deleted(fs)
	if len(got) != 2 || got[fk("4")] != CauseCap || got[fk("5")] != CauseCap {
		t.Errorf("deletions = %v, want 4 and 5 by cap", got)
	}
	if fs.EffectiveCap != 2 {
		t.Errorf("EffectiveCap = %d, want 2", fs.EffectiveCap)
	}
}

// Traces INV-FOCUS-21: a cap shrink never removes a finished row.
func TestCapShrinkNeverRemovesFinishedRow(t *testing.T) {
	in := baseInput(t, "cap=1")
	in.PeriodHasRows = true
	in.Plan = planRows(4)
	in.PeriodCap = intp(6)
	in.Draft = replanDraft()
	in.Facts[fk("4")] = Facts{Finished: true}
	in.Facts[fk("1")] = Facts{Finished: true}
	fs := mustFinal(t, in)
	got := deleted(fs)
	if _, ok := got[fk("4")]; ok {
		t.Errorf("a finished row was cap-removed")
	}
	if _, ok := got[fk("1")]; ok {
		t.Errorf("a finished row was cap-removed")
	}
	if len(got) != 1 || got[fk("3")] != CauseCap {
		t.Errorf("deletions = %v, want only 3 by cap", got)
	}
}

func TestCapRaiseOnlyMovesTheLine(t *testing.T) {
	in := baseInput(t, "cap=9")
	in.PeriodHasRows, in.Plan = true, planRows(4)
	in.PeriodCap = intp(6)
	in.Draft = replanDraft()
	fs := mustFinal(t, in)
	if len(fs.Delete) != 0 || len(fs.Insert) != 0 {
		t.Errorf("a cap raise changed rows: %+v", fs)
	}
	if !intIs(fs.PersistCap, 9) || fs.EffectiveCap != 9 {
		t.Errorf("PersistCap %v EffectiveCap %d, want 9 and 9", fs.PersistCap, fs.EffectiveCap)
	}
}

func TestCapRestatingCurrentCapKeepsPulledRows(t *testing.T) {
	// Eight rows over a persisted cap of 6 (pulled and force-added rows):
	// restating the current cap, or going above it, deletes nothing.
	for _, text := range []string{"cap=6", "cap=7", "ok cap=6"} {
		in := baseInput(t, text)
		in.PeriodHasRows, in.Plan = true, planRows(8)
		in.PeriodCap = intp(6)
		in.Draft = replanDraft()
		fs := mustFinal(t, in)
		if len(fs.Delete) != 0 {
			t.Errorf("%q deleted %+v", text, fs.Delete)
		}
		if fs.EffectiveCap != 8 {
			t.Errorf("%q: EffectiveCap = %d, want 8 so open slots never go negative", text, fs.EffectiveCap)
		}
	}
	// With no persisted cap the default (6) is the persisted cap.
	in := baseInput(t, "cap=6")
	in.PeriodHasRows, in.Plan = true, planRows(8)
	in.Draft = replanDraft()
	if fs := mustFinal(t, in); len(fs.Delete) != 0 {
		t.Errorf("default-cap restate deleted %+v", fs.Delete)
	}
}

// Traces INV-FOCUS-21: a cap shrink exempts rows the reply adds.
func TestReplyCapShrinkExemptsRowsAddedByThisReply(t *testing.T) {
	in := baseInput(t, "cap=2 +bd-new +20")
	in.PeriodHasRows, in.Plan = true, planRows(3)
	in.PeriodCap = intp(6)
	in.Draft = replanDraft(fk("20"))
	in.Candidates = candidates(fk("20"))
	fs := mustFinal(t, in)
	got := deleted(fs)
	if len(got) != 1 || got[fk("3")] != CauseCap {
		t.Errorf("deletions = %v, want only the third plan row by cap", got)
	}
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{jk("bd-new"), fk("20")})
	// 2 surviving rows + 2 added by the reply.
	if fs.EffectiveCap != 4 {
		t.Errorf("EffectiveCap = %d, want 4", fs.EffectiveCap)
	}
}

func TestShrinkExemptsTheDraftsOwnProposals(t *testing.T) {
	in := baseInput(t, "cap=2")
	in.PeriodHasRows, in.Plan = true, planRows(3)
	in.PeriodCap = intp(6)
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), true), drow(fk("20"), false), drow(fk("21"), false))
	fs := mustFinal(t, in)
	got := deleted(fs)
	if len(got) != 1 || got[fk("3")] != CauseCap {
		t.Errorf("deletions = %v, want only the third plan row", got)
	}
	// The window is the top 2 of the draft, both already in the plan: no insert.
	if len(fs.Insert) != 0 {
		t.Errorf("inserted %v", insertKeys(fs))
	}
}

func TestHiddenOrInactiveWithoutTerminalIsDroppedAndShownInChanges(t *testing.T) {
	cases := map[string]Facts{
		"hidden":   {Hidden: true},
		"inactive": {Inactive: true},
		"both":     {Hidden: true, Inactive: true},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			in := baseInput(t, "ok")
			in.PeriodHasRows, in.Plan = true, planRows(3)
			in.Draft = replanDraft()
			in.Facts[fk("2")] = f
			fs := mustFinal(t, in)
			if !reflect.DeepEqual(fs.Delete, []Deletion{{fk("2"), CauseDropped}}) {
				t.Errorf("Delete = %+v, want 2 dropped", fs.Delete)
			}
		})
	}
}

func TestHandAddInactiveByAgeSweepIsDroppedAtNextLock(t *testing.T) {
	in := baseInput(t, "ok")
	in.PeriodHasRows = true
	in.Plan = []PlanRow{prow(fk("1"), 1), prow(jk("bd-hand"), 2)}
	in.Draft = replanDraft()
	in.Facts[jk("bd-hand")] = Facts{Inactive: true}
	fs := mustFinal(t, in)
	if !reflect.DeepEqual(fs.Delete, []Deletion{{jk("bd-hand"), CauseDropped}}) {
		t.Errorf("Delete = %+v", fs.Delete)
	}
	// A dropped row does not count toward a later cap shrink.
	in.Reply = reply(t, "cap=1")
	in.PeriodCap = intp(6)
	fs = mustFinal(t, in)
	if got := deleted(fs); len(got) != 1 || got[jk("bd-hand")] != CauseDropped {
		t.Errorf("deletions = %v, want only the dropped row", got)
	}
}

func TestFinishedIsEvaluatedBeforeActive(t *testing.T) {
	// A closed entity is also deactivated: finished AND inactive stays.
	in := baseInput(t, "ok")
	in.PeriodHasRows, in.Plan = true, planRows(2)
	in.Draft = replanDraft()
	in.Facts[fk("1")] = Facts{Finished: true, Inactive: true}
	in.Facts[fk("2")] = Facts{Finished: true, Hidden: true}
	fs := mustFinal(t, in)
	if len(fs.Delete) != 0 {
		t.Errorf("a finished row was removed: %+v", fs.Delete)
	}
	// The same at a first lock: a finished+inactive proposal is a finished
	// skip, not a lost one.
	in = baseInput(t, "ok")
	in.Draft = draftOf(drow(fk("1"), true))
	in.Facts[fk("1")] = Facts{Finished: true, Inactive: true}
	fs = mustFinal(t, in)
	if got := skippedReason(fs, fk("1")); got != SkipFinished {
		t.Errorf("skip = %q, want %q", got, SkipFinished)
	}
}

// Traces INV-FOCUS-22: a slot, a group or a merge never removes a row.
func TestSlotGroupAndMergeSuppressionNeverRemove(t *testing.T) {
	covering := fk("9")
	in := baseInput(t, "ok")
	in.PeriodHasRows, in.Plan = true, planRows(3)
	in.PeriodCap = intp(6)
	in.Draft = replanDraft()
	in.Facts[fk("1")] = Facts{CoveredBy: &covering}
	in.Facts[fk("2")] = Facts{Claimant: "someone", Deferred: true}
	// Row 3 is absorbed into the key of row 1 by a merge of this run.
	in.Merges = []Merge{{Kept: fk("1"), Absorbed: fk("3")}}
	fs := mustFinal(t, in)
	if len(fs.Delete) != 0 {
		t.Errorf("suppression removed rows: %+v", fs.Delete)
	}
	if !reflect.DeepEqual(fs.Absorbed, []Merge{{Kept: fk("1"), Absorbed: fk("3")}}) {
		t.Errorf("Absorbed = %+v", fs.Absorbed)
	}
}

func TestForcedAndHandaddedRowsSurviveRerun(t *testing.T) {
	// A second ok over a plan that holds force-pulled and hand-added rows
	// (an ordinary rerun) removes none of them and re-adds none.
	plan := []PlanRow{prow(fk("1"), 1), prow(fk("20"), 2), prow(jk("bd-hand"), 3)}
	in := baseInput(t, "ok +20 +bd-hand")
	in.PeriodHasRows, in.Plan = true, plan
	in.PeriodCap = intp(1)
	in.Draft = replanDraft()
	fs := mustFinal(t, in)
	if len(fs.Delete) != 0 || len(fs.Insert) != 0 {
		t.Errorf("rerun changed rows: %+v", fs)
	}
	if skippedReason(fs, fk("20")) != SkipAlreadySelected || skippedReason(fs, jk("bd-hand")) != SkipAlreadySelected {
		t.Errorf("skips = %+v", fs.Skipped)
	}
	if fs.EffectiveCap != 3 {
		t.Errorf("EffectiveCap = %d, want 3 (the rows held)", fs.EffectiveCap)
	}
}

// Traces INV-FOCUS-22: a reassignment never removes a row.
func TestReassignedItemStaysInPlan(t *testing.T) {
	// Reassignment is not a loss of candidacy: the row stays whoever holds it
	// now, until a new-period lock or a -key.
	in := baseInput(t, "ok")
	in.PeriodHasRows, in.Plan = true, planRows(2)
	in.Draft = replanDraft()
	in.Facts[fk("1")] = Facts{Claimant: "another-actor"}
	fs := mustFinal(t, in)
	if len(fs.Delete) != 0 {
		t.Errorf("a reassigned row was removed: %+v", fs.Delete)
	}
}

func TestReassignedJiraIssueStaysInPlanUntilNewPeriodLockOrStrike(t *testing.T) {
	jira := jk("PROJ-12")
	plan := []PlanRow{prow(fk("1"), 1), prow(jira, 2)}
	facts := LiveFacts{jira: {Title: "reassigned away", Claimant: "someone-else"}}

	in := baseInput(t, "ok")
	in.PeriodHasRows, in.Plan, in.Facts = true, plan, facts
	in.Draft = replanDraft()
	if fs := mustFinal(t, in); len(fs.Delete) != 0 {
		t.Fatalf("a reassigned issue was removed by a plain lock: %+v", fs.Delete)
	}
	in.Reply = reply(t, "-PROJ-12")
	fs := mustFinal(t, in)
	if !reflect.DeepEqual(fs.Delete, []Deletion{{jira, CauseOperator}}) {
		t.Errorf("a strike: Delete = %+v", fs.Delete)
	}
}

// --- merges -----------------------------------------------------------------

func TestMergeDropsTheAbsorbedProposalWithoutBackfill(t *testing.T) {
	in := baseInput(t, "ok")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), false))
	in.Merges = []Merge{{Kept: fk("1"), Absorbed: fk("2")}}
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1")})
	if !reflect.DeepEqual(fs.Absorbed, in.Merges) {
		t.Errorf("Absorbed = %+v", fs.Absorbed)
	}
}

func TestPlusOfAnAbsorbedKeyReversesTheMerge(t *testing.T) {
	// A recorded merge: +absorbed names it in Unmerge and adds the key as an
	// ordinary +key (a hand-add: it is no live candidate while absorbed).
	in := baseInput(t, "+PROJ-5")
	in.PersistedMerges = []Merge{{Kept: fk("400"), Absorbed: jk("PROJ-5")}}
	fs := mustFinal(t, in)
	if !reflect.DeepEqual(fs.Unmerge, in.PersistedMerges) {
		t.Errorf("Unmerge = %+v", fs.Unmerge)
	}
	wantFinalKeys(t, "HandAdded", fs.HandAdded, []Key{jk("PROJ-5")})

	// Without the +key the merge stays and nothing is reversed.
	in = baseInput(t, "ok")
	in.Draft = draftOf(drow(fk("1"), true), drow(jk("PROJ-5"), true))
	in.PersistedMerges = []Merge{{Kept: fk("400"), Absorbed: jk("PROJ-5")}}
	fs = mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1")})
	if len(fs.Unmerge) != 0 {
		t.Errorf("Unmerge = %+v", fs.Unmerge)
	}

	// A merge of THIS run is cancelled by the +key: nothing to remove, nothing
	// recorded.
	in = baseInput(t, "ok +PROJ-5")
	in.Draft = draftOf(drow(fk("1"), true), drow(jk("PROJ-5"), false))
	in.Merges = []Merge{{Kept: fk("1"), Absorbed: jk("PROJ-5")}}
	fs = mustFinal(t, in)
	if len(fs.Absorbed) != 0 || len(fs.Unmerge) != 0 {
		t.Errorf("a cancelled merge left %+v / %+v", fs.Absorbed, fs.Unmerge)
	}
	wantFinalKeys(t, "Forced", fs.Forced, []Key{jk("PROJ-5")})
}

func TestMergeValidation(t *testing.T) {
	in := baseInput(t, "ok")
	in.Merges = []Merge{{Kept: fk("1"), Absorbed: fk("1")}}
	if _, err := ComputeFinalSet(in); !errors.Is(err, ErrUsage) {
		t.Errorf("self merge err = %v", err)
	}
	in.Merges = []Merge{{Kept: fk("1"), Absorbed: fk("2")}, {Kept: fk("3"), Absorbed: fk("2")}}
	if _, err := ComputeFinalSet(in); !errors.Is(err, ErrUsage) {
		t.Errorf("double absorb err = %v", err)
	}
	in.Merges = []Merge{{Kept: fk("1"), Absorbed: fk("2")}, {Kept: fk("1"), Absorbed: fk("2")}}
	fs, err := ComputeFinalSet(in)
	if err != nil || len(fs.Absorbed) != 1 {
		t.Errorf("a repeated merge: %+v, %v", fs.Absorbed, err)
	}
}

func TestMinusOfAnAbsorbedKeyIsAQuietSkip(t *testing.T) {
	in := baseInput(t, "-PROJ-5")
	in.PersistedMerges = []Merge{{Kept: fk("400"), Absorbed: jk("PROJ-5")}}
	fs := mustFinal(t, in)
	if got := skippedReason(fs, jk("PROJ-5")); got != SkipNotACandidate {
		t.Errorf("skip = %q, want %q", got, SkipNotACandidate)
	}
}

// --- misc -------------------------------------------------------------------

func TestSkipsAreReportedOncePerKey(t *testing.T) {
	in := baseInput(t, "ok +1")
	in.Draft = draftOf(drow(fk("1"), true))
	in.Facts[fk("1")] = Facts{Finished: true}
	fs := mustFinal(t, in)
	if len(fs.Skipped) != 1 {
		t.Errorf("Skipped = %+v, want one entry", fs.Skipped)
	}
}

func TestNilDraftAtFirstLockProposesNothing(t *testing.T) {
	in := baseInput(t, "ok")
	fs := mustFinal(t, in)
	if len(fs.Insert) != 0 || !intIs(fs.PersistCap, 6) || fs.EffectiveCap != 6 {
		t.Errorf("empty preview: %+v", fs)
	}
}

func TestPlanRowsWithoutPeriodFlagAreAPlan(t *testing.T) {
	// PeriodHasRows false with rows supplied is read as a plan: nothing is
	// proposed by a replan draft and PersistCap is unchanged.
	in := baseInput(t, "ok")
	in.Plan = planRows(2)
	in.Draft = replanDraft(fk("9"))
	fs := mustFinal(t, in)
	if len(fs.Insert) != 0 || fs.PersistCap != nil {
		t.Errorf("fs = %+v", fs)
	}
}

func TestNonPositiveCapInputsAreIgnored(t *testing.T) {
	in := baseInput(t, "ok")
	in.DefaultCap = -3
	in.PeriodCap, in.DraftCap = intp(0), intp(-1)
	in.Draft = draftOf(drow(fk("1"), true))
	fs := mustFinal(t, in)
	if !intIs(fs.PersistCap, DefaultCap) {
		t.Errorf("PersistCap = %v, want the package default %d", fs.PersistCap, DefaultCap)
	}
}

func TestInsertionOrderIsDraftThenReply(t *testing.T) {
	in := baseInput(t, "+bd-b +bd-a +4")
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("4"), false))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1"), fk("2"), jk("bd-b"), jk("bd-a"), fk("4")})
	wantFinalKeys(t, "Forced", fs.Forced, []Key{fk("4")})
	wantFinalKeys(t, "HandAdded", fs.HandAdded, []Key{jk("bd-b"), jk("bd-a")})
}

func TestDeletionOrderIsOperatorThenDroppedThenCap(t *testing.T) {
	in := baseInput(t, "cap=1 -4")
	in.PeriodHasRows, in.Plan = true, planRows(5)
	in.PeriodCap = intp(6)
	in.Draft = replanDraft()
	in.Facts[fk("2")] = Facts{Hidden: true}
	fs := mustFinal(t, in)
	want := []Deletion{{fk("4"), CauseOperator}, {fk("2"), CauseDropped}, {fk("5"), CauseCap}, {fk("3"), CauseCap}}
	if !reflect.DeepEqual(fs.Delete, want) {
		t.Errorf("Delete = %+v, want %+v", fs.Delete, want)
	}
}

// --- mutation-driven additions ------------------------------------------------

func TestLostIsFalseForAFinishedKeyWhateverElseIsTrue(t *testing.T) {
	c := &finalCtx{in: FinalSetInput{Facts: LiveFacts{
		fk("1"): {Finished: true, Hidden: true},
		fk("2"): {Finished: true, Inactive: true},
		fk("3"): {Hidden: true},
		fk("4"): {Inactive: true},
		fk("5"): {Claimant: "x", Deferred: true},
	}}}
	want := map[Key]bool{fk("1"): false, fk("2"): false, fk("3"): true, fk("4"): true, fk("5"): false, fk("6"): false}
	for k, w := range want {
		if got := c.lost(k); got != w {
			t.Errorf("lost(%v) = %v, want %v", k, got, w)
		}
	}
}

func TestReplyCapOfZeroOrLessIsIgnoredWhenBuiltByHand(t *testing.T) {
	for _, n := range []int{0, -1} {
		in := baseInput(t, "ok")
		in.Reply.Cap = intp(n)
		in.PeriodHasRows, in.Plan = true, planRows(3)
		in.PeriodCap = intp(5)
		in.Draft = replanDraft()
		fs := mustFinal(t, in)
		if fs.PersistCap != nil || len(fs.Delete) != 0 || fs.EffectiveCap != 5 {
			t.Errorf("cap %d was honoured: %+v", n, fs)
		}
	}
}

func TestPlanOrderIsByPositionThenKey(t *testing.T) {
	// Keys and positions disagree: positions decide.
	in := baseInput(t, "cap=1")
	in.PeriodHasRows = true
	in.Plan = []PlanRow{prow(fk("1"), 3), prow(fk("2"), 1), prow(fk("3"), 2)}
	in.PeriodCap = intp(6)
	in.Draft = replanDraft()
	fs := mustFinal(t, in)
	want := []Deletion{{fk("1"), CauseCap}, {fk("3"), CauseCap}}
	if !reflect.DeepEqual(fs.Delete, want) {
		t.Errorf("Delete = %+v, want %+v", fs.Delete, want)
	}
	// Equal positions fall back to the key: 2 is kept, 3 is cut.
	in.Plan = []PlanRow{prow(fk("3"), 1), prow(fk("2"), 1)}
	fs = mustFinal(t, in)
	if !reflect.DeepEqual(fs.Delete, []Deletion{{fk("3"), CauseCap}}) {
		t.Errorf("tie: Delete = %+v", fs.Delete)
	}
	// Type breaks a tie on position and key text too.
	in.Plan = []PlanRow{prow(jk("a"), 1), prow(Key{"pr", "a"}, 1)}
	fs = mustFinal(t, in)
	if !reflect.DeepEqual(fs.Delete, []Deletion{{Key{"pr", "a"}, CauseCap}}) {
		t.Errorf("type tie: Delete = %+v", fs.Delete)
	}
}

func TestTwoFormsOfOneStruckKeyDeleteOnce(t *testing.T) {
	in := baseInput(t, "-5 -acme/widgets#5")
	in.PeriodHasRows, in.Plan = true, planRows(5)
	in.Draft = replanDraft()
	fs := mustFinal(t, in)
	if !reflect.DeepEqual(fs.Delete, []Deletion{{fk("5"), CauseOperator}}) {
		t.Errorf("Delete = %+v, want one deletion", fs.Delete)
	}
}

func TestProposalsOfAnInitialDraftWhosePullsWereOtherRows(t *testing.T) {
	// The plan was created by a pull of a row the draft does not hold: the
	// draft is still an initial draft (it marks + rows) and ok locks them.
	in := baseInput(t, "ok")
	in.PeriodHasRows, in.Plan = true, []PlanRow{prow(fk("50"), 1)}
	in.Draft = draftOf(drow(fk("1"), true), drow(fk("2"), true), drow(fk("3"), false))
	fs := mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("1"), fk("2")})

	// A draft that holds a plan row but marks none is also initial (its
	// proposals were all pulled): with a reply cap it re-takes the top.
	in = baseInput(t, "cap=3")
	in.PeriodHasRows, in.Plan = true, []PlanRow{prow(fk("1"), 1)}
	in.PeriodCap = intp(3)
	in.Draft = draftOf(drow(fk("1"), false), drow(fk("2"), false), drow(fk("3"), false), drow(fk("4"), false))
	fs = mustFinal(t, in)
	wantFinalKeys(t, "Insert", insertKeys(fs), []Key{fk("2"), fk("3")})
}

func TestMinusOfAnUnresolvableKeyNamesTheKey(t *testing.T) {
	_, err := ComputeFinalSet(baseInput(t, "-nonsense"))
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "cannot resolve key") {
		t.Errorf("err = %v", err)
	}
	_, err = ComputeFinalSet(baseInput(t, "+nonsense"))
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "cannot resolve key") {
		t.Errorf("err = %v", err)
	}
}

func TestPlanOrderLaterLargerPositionWithSmallerKeyStaysLater(t *testing.T) {
	in := baseInput(t, "cap=1")
	in.PeriodHasRows = true
	in.Plan = []PlanRow{prow(fk("2"), 1), prow(fk("1"), 2)}
	in.PeriodCap = intp(6)
	in.Draft = replanDraft()
	fs := mustFinal(t, in)
	if !reflect.DeepEqual(fs.Delete, []Deletion{{fk("1"), CauseCap}}) {
		t.Errorf("Delete = %+v, want the row at the higher position", fs.Delete)
	}
}
