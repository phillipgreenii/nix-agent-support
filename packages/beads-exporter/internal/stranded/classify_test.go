package stranded

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/failure"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
)

func TestLiveByTranscriptID(t *testing.T) {
	for _, assignee := range []string{sessA + "-drain", "drain-" + sessA, sessA, "x-" + sessA + "-unblock"} {
		t.Run(assignee, func(t *testing.T) {
			f := newFixture(t)
			f.write("-slug/"+sessA+".jsonl", time.Hour, textEvent("hello"))
			if got := f.classify(claimed("b1", assignee, "in_progress")); len(got) != 0 {
				t.Fatalf("claim %q with a fresh transcript by id is stranded: %v", assignee, got)
			}
		})
	}
}

func TestTranscriptByIDNeedsLowerCaseUUIDAndAnInWindowFile(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/"+sessA+".jsonl", time.Hour, textEvent("hello"))
	f.write("-slug/"+sessB+".jsonl", testWindow+time.Minute, textEvent("old"))
	upper := strings.ToUpper(sessA) + "-drain"
	got := f.classify(
		claimed("upper", upper, "in_progress"),
		claimed("stale", sessB+"-drain", "in_progress"),
		claimed("nouuid", "drain-"+sessA[:30], "in_progress"),
		claimed("other", "cccccccc-3333-4333-8333-cccccccccccc-drain", "in_progress"),
	)
	if want := []string{"nouuid", "other", "stale", "upper"}; !equalStrings(got, want) {
		t.Fatalf("stranded = %v, want %v", got, want)
	}
}

func TestLiveByClaimContext(t *testing.T) {
	const who = "worker-alpha"
	cases := []struct {
		name  string
		event map[string]any
	}{
		{"actor flag unquoted", commandEvent("bd update x1 --claim --actor " + who)},
		{"actor flag quoted", commandEvent(`bd update x1 --claim --actor "` + who + `"`)},
		{"actor flag single-quoted", commandEvent("bd update x1 --claim --actor '" + who + "'")},
		{"actor flag equals", commandEvent("bd update x1 --claim --actor=" + who)},
		{"BEADS_ACTOR assignment", commandEvent("BEADS_ACTOR=" + who + " bd update x1 --claim")},
		{"BEADS_ACTOR export quoted", commandEvent(`export BEADS_ACTOR="` + who + `"; bd ready`)},
		{"assignee JSON value, escaped inside a tool result", resultEvent(`{"id":"x1","assignee":"` + who + `"}`)},
		{"assignee JSON value, spaced, escaped", resultEvent(`{"id": "x1", "assignee": "` + who + `"}`)},
		{"assignee JSON value, raw object", rawEvent(map[string]any{"id": "x1", "assignee": who})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write("-slug/session-one.jsonl", time.Hour, textEvent("start"), tc.event)
			if got := f.classify(claimed("b1", who, "in_progress")); len(got) != 0 {
				t.Fatalf("claim context not recognised: %v", got)
			}
		})
	}
}

func TestLiveByClaimContextInSubagentTranscript(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/session-one/subagents/agent-1.jsonl", time.Hour, commandEvent("bd close x1 --actor worker-alpha"))
	if got := f.classify(claimed("b1", "worker-alpha", "in_progress")); len(got) != 0 {
		t.Fatalf("subagent transcript claim context not recognised: %v", got)
	}
}

func TestBareMentionIsNotLive(t *testing.T) {
	f := newFixture(t)
	f.write(
		"-slug/reviewer.jsonl", time.Hour,
		textEvent("who owns this? worker-alpha maybe"),
		resultEvent("Assignee: worker-alpha\nStatus: in_progress"),
		commandEvent("echo worker-alpha"),
		commandEvent("bd list --assignee worker-alpha"),
		resultEvent(`{"assignee": null, "owner": "worker-alpha"}`),
		commandEvent("bd update x --actors worker-alpha"),
	)
	if got := f.classify(claimed("b1", "worker-alpha", "in_progress")); !equalStrings(got, []string{"b1"}) {
		t.Fatalf("bare mention made the claim live: %v", got)
	}
}

func TestClaimValueMustMatchTheWholeAssignee(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/s.jsonl", time.Hour, commandEvent("bd update x --actor worker-alpha-extra"))
	got := f.classify(claimed("b1", "worker-alpha", "in_progress"), claimed("b2", "worker-alpha-extra", "in_progress"))
	if !equalStrings(got, []string{"b1"}) {
		t.Fatalf("stranded = %v, want only the claim whose full name was used", got)
	}
}

func TestStatusSidecarIsIgnored(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/"+sessA+".status.jsonl", time.Hour, commandEvent("bd update x --actor "+sessA+"-drain"))
	f.write("-slug/other.status.jsonl", time.Hour, commandEvent("bd update x --actor worker-alpha"))
	got := f.classify(claimed("b1", sessA+"-drain", "in_progress"), claimed("b2", "worker-alpha", "in_progress"))
	if !equalStrings(got, []string{"b1", "b2"}) {
		t.Fatalf("a statusline sidecar made a claim live: %v", got)
	}
}

func TestOutOfWindowClaimContextIsNotLive(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/old.jsonl", testWindow+time.Second, commandEvent("bd update x --actor worker-alpha"))
	if got := f.classify(claimed("b1", "worker-alpha", "in_progress")); !equalStrings(got, []string{"b1"}) {
		t.Fatalf("stranded = %v", got)
	}
}

func TestWindowBoundaryIsInclusive(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/"+sessA+".jsonl", testWindow, textEvent("edge"))
	f.write("-slug/"+sessB+".jsonl", testWindow+time.Second, textEvent("just outside"))
	got := f.classify(claimed("edge", sessA, "open"), claimed("outside", sessB, "open"))
	if !equalStrings(got, []string{"outside"}) {
		t.Fatalf("stranded = %v, want only the transcript older than the window", got)
	}
}

func TestActiveButIdleSessionWithinWindowIsLive(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/"+sessA+".jsonl", 5*time.Hour+50*time.Minute, textEvent("last message hours ago"))
	if got := f.classify(claimed("b1", sessA+"-drain", "in_progress")); len(got) != 0 {
		t.Fatalf("an idle but recently written session is stranded: %v", got)
	}
}

func TestOperatorNameClaimIsLiveOnlyWithinWindow(t *testing.T) {
	f := newFixture(t, "operator")
	// The operator name appears as a claim value in a fresh transcript; rule 2
	// must be skipped for it, so only the claim age counts.
	f.write("-slug/s.jsonl", time.Minute, commandEvent("bd update x --actor operator"))
	fresh := claimed("fresh", "operator", "in_progress")
	fresh.StartedAt = ptrTime(testNow.Add(-time.Hour))
	old := claimed("old", "operator", "in_progress")
	old.StartedAt = ptrTime(testNow.Add(-testWindow - time.Second))
	edge := claimed("edge", "operator", "open")
	edge.StartedAt = ptrTime(testNow.Add(-testWindow))
	updatedOnly := claimed("updated-fresh", "operator", "open")
	updatedOnly.UpdatedAt = ptrTime(testNow.Add(-time.Hour))
	updatedOld := claimed("updated-old", "operator", "open")
	updatedOld.UpdatedAt = ptrTime(testNow.Add(-24 * time.Hour))
	startedWinsOverUpdated := claimed("started-old-updated-fresh", "operator", "open")
	startedWinsOverUpdated.StartedAt = ptrTime(testNow.Add(-24 * time.Hour))
	startedWinsOverUpdated.UpdatedAt = ptrTime(testNow.Add(-time.Minute))
	untimed := claimed("untimed", "operator", "open")
	got := f.classify(fresh, old, edge, updatedOnly, updatedOld, startedWinsOverUpdated, untimed)
	want := []string{"old", "started-old-updated-fresh", "untimed", "updated-old"}
	if !equalStrings(got, want) {
		t.Fatalf("stranded = %v, want %v", got, want)
	}
}

func TestOperatorOnlyCandidatesNeedNoTranscripts(t *testing.T) {
	f := newFixture(t, "operator") // no projects directory exists at all
	c := claimed("b1", "operator", "in_progress")
	c.StartedAt = ptrTime(testNow.Add(-time.Hour))
	if got := f.classify(c); len(got) != 0 {
		t.Fatalf("stranded = %v", got)
	}
	if got := f.opener.openedPaths(); len(got) != 0 {
		t.Fatalf("opened %v", got)
	}
}

func TestStatusAndAssigneeScope(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/unrelated.jsonl", time.Hour, textEvent("nothing"))
	got := f.classify(
		claimed("b-open", "w1", "open"),
		claimed("b-prog", "w2", "in_progress"),
		claimed("b-hooked", "w3", "hooked"),
		claimed("b-blocked", "w4", "blocked"),
		claimed("b-deferred", "w5", "deferred"),
		claimed("b-closed", "w6", "closed"),
		claimed("b-unassigned", "", "in_progress"),
	)
	if want := []string{"b-hooked", "b-open", "b-prog"}; !equalStrings(got, want) {
		t.Fatalf("stranded = %v, want %v", got, want)
	}
}

func TestClaimRecordsBeadFieldsAndClaimTime(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/unrelated.jsonl", time.Hour, textEvent("nothing"))
	started := testNow.Add(-48 * time.Hour)
	updated := testNow.Add(-24 * time.Hour)
	b1 := claimed("b1", "w1", "in_progress")
	b1.StartedAt = &started
	b1.UpdatedAt = &updated
	b2 := claimed("b2", "w2", "open")
	b2.UpdatedAt = &updated
	res, err := f.cl.Classify(context.Background(), []bd.Bead{b2, b1}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []Claim{
		{BeadID: "b1", Assignee: "w1", Status: "in_progress", ClaimTime: started},
		{BeadID: "b2", Assignee: "w2", Status: "open", ClaimTime: updated},
	}
	if len(res.Claims) != 2 || res.Claims[0] != want[0] || res.Claims[1] != want[1] {
		t.Fatalf("claims = %+v, want %+v", res.Claims, want)
	}
}

func TestOutOfWindowFileIsNeverOpened(t *testing.T) {
	f := newFixture(t)
	old := f.write("-slug/old.jsonl", testWindow+time.Hour, commandEvent("bd update x --actor worker-alpha"))
	oldSub := f.write("-slug/old/subagents/a.jsonl", testWindow+time.Hour, textEvent("x"))
	fresh := f.write("-slug/fresh.jsonl", time.Hour, textEvent("x"))
	for _, p := range []string{old, oldSub} {
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
	}
	got := f.classify(claimed("b1", "worker-alpha", "in_progress"))
	if !equalStrings(got, []string{"b1"}) {
		t.Fatalf("stranded = %v", got)
	}
	if opened := f.opener.openedPaths(); !equalStrings(opened, []string{fresh}) {
		t.Fatalf("opened %v, want only %s", opened, fresh)
	}
}

func TestUnreadableInWindowFileFailsWithTranscriptError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	f := newFixture(t)
	p := f.write("-slug/fresh.jsonl", time.Hour, textEvent("x"))
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	_, err := f.cl.Classify(context.Background(), []bd.Bead{claimed("b1", "w", "open")}, testNow)
	if err == nil || failure.ReasonOf(err) != failure.TranscriptError {
		t.Fatalf("err = %v (reason %v), want a transcript_error", err, failure.ReasonOf(err))
	}
}

func TestMissingProjectsDirFailsWithTranscriptError(t *testing.T) {
	f := newFixture(t) // nothing written: <claudeDir>/projects does not exist
	_, err := f.cl.Classify(context.Background(), []bd.Bead{claimed("b1", "w", "open")}, testNow)
	if err == nil || failure.ReasonOf(err) != failure.TranscriptError {
		t.Fatalf("err = %v (reason %v), want a transcript_error", err, failure.ReasonOf(err))
	}
	var fe *failure.Error
	if !errors.As(err, &fe) || fe.Op != "stranded" {
		t.Fatalf("err = %#v", err)
	}
}

func TestNoCandidatesNeedNoTranscripts(t *testing.T) {
	f := newFixture(t)
	res, err := f.cl.Classify(context.Background(), []bd.Bead{claimed("b1", "", "open"), claimed("b2", "w", "closed")}, testNow)
	if err != nil || len(res.Claims) != 0 {
		t.Fatalf("got (%v, %v)", res.Claims, err)
	}
}

func TestCancelledContextStopsTheScan(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/a.jsonl", time.Hour, textEvent("x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.cl.Classify(ctx, []bd.Bead{claimed("b1", "w", "open")}, testNow)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestVanishedTranscriptIsSkipped(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/gone.jsonl", time.Hour, commandEvent("bd update x --actor worker-alpha"))
	keep := f.write("-slug/keep.jsonl", time.Hour, textEvent("x"))
	// The file is listed, then removed before it is opened.
	f.cl.stat = func(path string) (os.FileInfo, error) {
		info, err := os.Stat(path)
		if path == p {
			_ = os.Remove(p)
		}
		return info, err
	}
	got := f.classify(claimed("b1", "worker-alpha", "in_progress"))
	if !equalStrings(got, []string{"b1"}) {
		t.Fatalf("stranded = %v", got)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal(err)
	}
}

func TestStatErrorOtherThanNotExistFails(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/a.jsonl", time.Hour, textEvent("x"))
	f.cl.stat = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
	_, err := f.cl.Classify(context.Background(), []bd.Bead{claimed("b1", "w", "open")}, testNow)
	if err == nil || failure.ReasonOf(err) != failure.TranscriptError {
		t.Fatalf("err = %v", err)
	}
}

func TestStatNotExistIsSkipped(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/a.jsonl", time.Hour, textEvent("x"))
	f.cl.stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	got := f.classify(claimed("b1", "w", "open"))
	if !equalStrings(got, []string{"b1"}) {
		t.Fatalf("stranded = %v", got)
	}
}

func TestSessionTranscriptsOfEverySlugAreConsulted(t *testing.T) {
	f := newFixture(t)
	f.write("-slug-one/"+sessA+".jsonl", time.Hour, textEvent("x"))
	f.write("-slug-two/"+sessB+".jsonl", time.Hour, textEvent("x"))
	if got := f.classify(claimed("b1", sessA, "open"), claimed("b2", sessB, "open")); len(got) != 0 {
		t.Fatalf("stranded = %v", got)
	}
}

func TestSubagentTranscriptDoesNotStandInForTheSession(t *testing.T) {
	f := newFixture(t)
	f.write("-slug/other/subagents/"+sessA+".jsonl", time.Hour, textEvent("x"))
	if got := f.classify(claimed("b1", sessA, "open")); !equalStrings(got, []string{"b1"}) {
		t.Fatalf("stranded = %v", got)
	}
}

func TestSamplesZeroFillByStatusAndOldestClaim(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	t1 := time.Unix(1_700_000_500, 0)
	r := Result{Claims: []Claim{
		{BeadID: "a", Status: "open", ClaimTime: t1},
		{BeadID: "b", Status: "open", ClaimTime: t0},
		{BeadID: "c", Status: "hooked"},
	}}
	got := map[string]float64{}
	for _, s := range r.Samples("alpha") {
		key := s.Family
		for _, l := range s.Labels {
			key += "|" + l.Name + "=" + l.Value
		}
		got[key] = s.Value
	}
	want := map[string]float64{
		metrics.FamStrandedClaims + "|db=alpha|status=open":        2,
		metrics.FamStrandedClaims + "|db=alpha|status=in_progress": 0,
		metrics.FamStrandedClaims + "|db=alpha|status=hooked":      1,
		metrics.FamOldestStranded + "|db=alpha":                    1_700_000_000,
	}
	if len(got) != len(want) {
		t.Fatalf("samples = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("samples[%s] = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
}

func TestSamplesOmitOldestWhenEmptyOrUntimed(t *testing.T) {
	for name, r := range map[string]Result{
		"empty":   {},
		"untimed": {Claims: []Claim{{BeadID: "a", Status: "open"}}},
	} {
		for _, s := range r.Samples("alpha") {
			if s.Family == metrics.FamOldestStranded {
				t.Fatalf("%s: oldest series emitted: %+v", name, s)
			}
		}
	}
	if n := len((Result{}).Samples("alpha")); n != 3 {
		t.Fatalf("empty result has %d samples, want the three zero-filled statuses", n)
	}
}

func TestClassifierAccessesOnlyTheProjectsTree(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(f.dir, "elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "x.jsonl"), marshalLines(t, commandEvent("bd update x --actor worker-alpha")), 0o644); err != nil {
		t.Fatal(err)
	}
	f.write("-slug/a.jsonl", time.Hour, textEvent("x"))
	if got := f.classify(claimed("b1", "worker-alpha", "open")); !equalStrings(got, []string{"b1"}) {
		t.Fatalf("stranded = %v", got)
	}
}
