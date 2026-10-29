package backfill

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// applyRec is an apply-mode record whose rules each applied the given counts.
func applyRec(at time.Time, pr string, acts []Planned, applied map[string]int) recLine {
	return recLine{at: at, pr: pr, mode: ModeApply, actions: acts, applied: applied, residual: intp(0)}
}

func TestTheByTimestampListingIncludesClosesReopensUpdatesAndCreates(t *testing.T) {
	t0 := baseTime
	p := tmpRecord(t)
	writeLog(
		t, p,
		applyRec(t0.Add(3*time.Minute), "o/r#3", []Planned{{Rule: "closure", Kind: "anchor", Op: "close"}}, map[string]int{"closure": 1}).json(t),
		applyRec(t0.Add(1*time.Minute), "o/r#1", []Planned{{Rule: "anchor.lazy", Kind: "anchor", Op: "create"}, {Rule: "review.head-advanced", Kind: "review-pr", Op: "create"}}, map[string]int{"anchor.lazy": 1, "review.head-advanced": 1}).json(t),
		applyRec(t0.Add(2*time.Minute), "o/r#2", []Planned{{Rule: "review.reopen", Kind: "review-pr", Op: "reopen"}, {Rule: "anchor.head", Kind: "anchor", Op: "update"}}, map[string]int{"review.reopen": 1, "anchor.head": 1}).json(t),
		// A plan-mode record, a deduped action and a failed one are writes of nothing.
		recLine{at: t0.Add(time.Minute), pr: "o/r#9", mode: ModePlan, actions: []Planned{{Rule: "x", Kind: "anchor", Op: "create"}}}.json(t),
		applyRec(t0.Add(4*time.Minute), "o/r#4", []Planned{{Rule: "anchor.lazy", Kind: "anchor", Op: "create"}}, map[string]int{"anchor.lazy": 0}).json(t),
	)
	if _, err := WriteFlipMarker(context.Background(), FlipMarkerOptions{RunRecord: p, Time: t0, Now: func() time.Time { return t0.Add(10 * time.Minute) }}); err != nil {
		t.Fatal(err)
	}
	lg, err := ReadLog(p)
	if err != nil {
		t.Fatal(err)
	}
	ws, bound, ok := WritesSinceFlip(lg)
	if !ok || !bound.Equal(t0) {
		t.Fatalf("bound %v %v", bound, ok)
	}
	var got []string
	for _, w := range ws {
		got = append(got, w.PR+" "+w.Op+" "+w.Kind)
	}
	want := []string{"o/r#1 create anchor", "o/r#1 create review-pr", "o/r#2 reopen review-pr", "o/r#2 update anchor", "o/r#3 close anchor"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("writes %v, want %v", got, want)
	}
	var out bytes.Buffer
	if n, ok := PrintWrites(&out, lg); !ok || n != 5 {
		t.Fatalf("%d %v", n, ok)
	}
	if !strings.Contains(out.String(), "writes total=5 close=1 create=2 reopen=1 update=1") {
		t.Errorf("output:\n%s", out.String())
	}
	// Created, per kind, for the enumerated set only.
	c := CreatesSinceFlip(lg, map[string]bool{"o/r#1": true, "o/r#3": true})
	if c["anchor"] != 1 || c["review-pr"] != 1 || len(c) != 2 {
		t.Fatalf("creates %v", c)
	}
}

func TestTheListingIsBoundedByTheEarlierOfTheMarkerAndTheFirstApplyRecord(t *testing.T) {
	t0 := baseTime
	p := tmpRecord(t)
	// A live pr-change apply run wrote a bead BEFORE the marker was written (the
	// deployment apply that changed the role's mode came first).
	writeLog(
		t, p,
		recLine{at: t0.Add(-time.Hour), pr: "o/r#0", mode: ModePlan, actions: []Planned{{Rule: "x", Kind: "anchor", Op: "create"}}}.json(t),
		applyRec(t0, "o/r#1", []Planned{{Rule: "anchor.lazy", Kind: "anchor", Op: "create"}}, map[string]int{"anchor.lazy": 1}).json(t),
	)
	if m, err := WriteFlipMarker(context.Background(), FlipMarkerOptions{RunRecord: p, Now: func() time.Time { return t0.Add(5 * time.Minute) }}); err != nil || m.Time != t0.Add(5*time.Minute).Format(time.RFC3339) {
		t.Fatalf("marker %+v %v", m, err)
	}
	if err := AppendLine(p, json.RawMessage(applyRec(t0.Add(6*time.Minute), "o/r#2", []Planned{{Rule: "closure", Kind: "anchor", Op: "close"}}, map[string]int{"closure": 1}).json(t))); err != nil {
		t.Fatal(err)
	}
	lg, _ := ReadLog(p)
	ws, bound, ok := WritesSinceFlip(lg)
	if !ok || !bound.Equal(t0) {
		t.Fatalf("bound %v, want the first apply-mode record at %v", bound, t0)
	}
	if len(ws) != 2 || ws[0].PR != "o/r#1" {
		t.Fatalf("the write that predates the marker is missing: %+v", ws)
	}
	// With the marker EARLIER than the first apply record, the marker bounds.
	p2 := tmpRecord(t)
	writeLog(t, p2, applyRec(t0.Add(time.Hour), "o/r#1", []Planned{{Rule: "anchor.lazy", Kind: "anchor", Op: "create"}}, map[string]int{"anchor.lazy": 1}).json(t))
	if _, err := WriteFlipMarker(context.Background(), FlipMarkerOptions{RunRecord: p2, Time: t0.Add(30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	lg2, _ := ReadLog(p2)
	if _, b, ok := WritesSinceFlip(lg2); !ok || !b.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("bound %v %v", b, ok)
	}
}

func TestTheFlipMarkerAcceptsAnExplicitTimeEarlierThanItsWritingTime(t *testing.T) {
	t0 := baseTime
	p := tmpRecord(t)
	m, err := WriteFlipMarker(context.Background(), FlipMarkerOptions{RunRecord: p, Time: t0.Add(-10 * time.Minute), Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	if m.Time != t0.Add(-10*time.Minute).Format(time.RFC3339) || m.At != t0.Format(time.RFC3339) || m.Contract != FlipMarkerContract || m.Mode != ModeApply {
		t.Fatalf("%+v", m)
	}
	lg, _ := ReadLog(p)
	if len(lg.FlipMarkers) != 1 || !lg.FlipMarkers[0].T.Equal(t0.Add(-10*time.Minute)) {
		t.Fatalf("%+v", lg.FlipMarkers)
	}
}

func TestTheFlipMarkerCommentsOnTheEpicBeadAndTheRunRecordIsAuthoritative(t *testing.T) {
	p := tmpRecord(t)
	var calls []Call
	run := func(_ context.Context, c Call) Result { calls = append(calls, c); return Result{} }
	_, err := WriteFlipMarker(context.Background(), FlipMarkerOptions{RunRecord: p, EpicBead: "pg2-6mo45", Backend: "pg2", BeadsDir: "/w/pg2", Runner: run})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || strings.Join(calls[0].Args[:3], " ") != "issue comment pg2-6mo45" || !containsStr(calls[0].Args, "--backend") || calls[0].Env[len(calls[0].Env)-1] != "PG_CONNECTOR_ISSUE_BEADS_DIR=/w/pg2" {
		t.Fatalf("%+v", calls)
	}
	// A comment that fails does not undo the marker.
	p2 := tmpRecord(t)
	bad := func(context.Context, Call) Result { return Result{Code: 3, Stderr: []byte("tracker down")} }
	_, err = WriteFlipMarker(context.Background(), FlipMarkerOptions{RunRecord: p2, EpicBead: "pg2-6mo45", Runner: bad})
	if err == nil || !strings.Contains(err.Error(), ErrEpicComment.Error()) {
		t.Fatalf("err %v", err)
	}
	if lg, _ := ReadLog(p2); len(lg.FlipMarkers) != 1 {
		t.Fatal("the marker was not recorded")
	}
}

func TestPrintWritesWithNothingToListSaysSo(t *testing.T) {
	lg, _ := ReadLog(tmpRecord(t))
	var out bytes.Buffer
	if _, ok := PrintWrites(&out, lg); ok || !strings.Contains(out.String(), "nothing to list") {
		t.Fatalf("%v %q", ok, out.String())
	}
}

func TestACreateLimitCountsOnlyTheWritesSinceTheFlipOfTheSet(t *testing.T) {
	over, n, limit, ok := OverLimit(map[string]int{"anchor": 3, "fix-ci": 1}, map[string]int{"anchor": 3})
	if !ok || over != "fix-ci" || n != 1 || limit != 0 {
		t.Fatalf("%s %d %d %v", over, n, limit, ok)
	}
	if _, _, _, ok := OverLimit(map[string]int{"anchor": 3}, map[string]int{"anchor": 3}); ok {
		t.Fatal("meeting the limit is not exceeding it")
	}
}

func TestCreatesInPassCountsOnlyTheDriversOwnRecordsAfterThePassStart(t *testing.T) {
	p := tmpRecord(t)
	mk := func(at time.Time, pr, event string) []byte {
		l := createRec("anchor")(ModeApply)
		l.at, l.pr, l.mode, l.eventID = at, pr, ModeApply, event
		return l.json(t)
	}
	start, _ := json.Marshal(Marker{Contract: PassStartContract, Kind: "pass-start", At: baseTime.Format(time.RFC3339), Mode: ModeApply})
	writeLog(
		t, p,
		mk(baseTime, "o/r#1", "o/r#1"),              // an earlier pass of the driver
		mk(baseTime, "o/r#2", "o/r#2@0123456789ab"), // a router run before the pass
		start,
		mk(baseTime, "o/r#3", "o/r#3"), // the pass's own create
		mk(baseTime, "o/r#4", "o/r#4@0123456789ab"), // a router run during the pass
		mk(baseTime, "o/r#9", "o/r#9"),              // a PR outside the set
	)
	lg, err := ReadLog(p)
	if err != nil {
		t.Fatal(err)
	}
	since, _ := lg.PassStart(ModeApply, "")
	c := CreatesInPass(lg, map[string]bool{"o/r#1": true, "o/r#2": true, "o/r#3": true, "o/r#4": true}, since)
	if c["anchor"] != 1 || len(c) != 1 {
		t.Fatalf("creates %v", c)
	}
}
