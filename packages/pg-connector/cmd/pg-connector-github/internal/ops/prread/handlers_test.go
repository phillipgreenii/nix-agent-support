package prread

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/pending"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/prfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const wireID = "octo/widgets#42"

func TestHandlersProvidesTheFourOpsAtThePRSchemaVersion(t *testing.T) {
	hs := Handlers(Deps{Engine: &fakeEngine{}})
	if len(hs) != len(Ops) {
		t.Fatalf("handlers = %d, Ops = %d", len(hs), len(Ops))
	}
	for _, op := range Ops {
		h, ok := hs[op]
		if !ok {
			t.Errorf("no handler for %q", op)
			continue
		}
		if h.SchemaVersion != schema.PRSchemaVersion || h.Handle == nil {
			t.Errorf("%s: schema version %d (want %d), handle nil %t", op, h.SchemaVersion, schema.PRSchemaVersion, h.Handle == nil)
		}
	}
	want := []string{"show", "files", "commits", "review_pending"}
	if !slices.Equal(Ops, want) {
		t.Errorf("Ops = %v, want %v", Ops, want)
	}
}

// ---- read-through ------------------------------------------------------------

func filesGroup(t *testing.T, status groupsvc.Status, age, freshFor time.Duration) groupsvc.GroupResult {
	return groupsvc.GroupResult{
		Group:  groups.Files,
		Status: status,
		Row:    row(t, groups.Files, prfacts.Files{Files: []schema.PRFile{{Path: "a.go", Additions: 3, Deletions: 1}}}, age, freshFor),
	}
}

func TestEveryGroupFreshAnswersFromTheStoreWithoutAnOriginCall(t *testing.T) {
	eng := &fakeEngine{res: groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{
		filesGroup(t, groupsvc.Fresh, 30*time.Second, 5*time.Minute),
	}}}
	got, err := newHandlers(eng)["files"](context.Background(), args(wireID, false))
	if err != nil {
		t.Fatal(err)
	}
	m := asJSON(t, got)
	if m["served_from"] != "cache" || m["stale"] != false || m["age_seconds"] != float64(30) {
		t.Errorf("annotations = served_from %v stale %v age %v, want cache, false, 30", m["served_from"], m["stale"], m["age_seconds"])
	}
	if m["id"] != wireID {
		t.Errorf("id = %v, want the id as the caller wrote it", m["id"])
	}
	call := eng.last(t)
	if !slices.Equal(call.want, []string{groups.Files}) || call.opt.Fresh || call.id != wireID {
		t.Errorf("engine call = %+v", call)
	}
	g := m["groups"].([]any)
	if len(g) != 1 || g[0].(map[string]any)["group"] != "files" {
		t.Errorf("groups[] = %v, want the one files entry", g)
	}
}

func TestAGroupFetchedInTheCallIsServedFromOrigin(t *testing.T) {
	g := filesGroup(t, groupsvc.Fresh, 0, 5*time.Minute)
	g.FromOrigin = true
	eng := &fakeEngine{res: groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{g}}}
	got, err := newHandlers(eng)["files"](context.Background(), args(wireID, false))
	if err != nil {
		t.Fatal(err)
	}
	if m := asJSON(t, got); m["served_from"] != "origin" || m["stale"] != false {
		t.Errorf("served_from %v stale %v, want origin, false", m["served_from"], m["stale"])
	}
}

func TestAStaleGroupInsideExpiryIsServedStaleWithItsAge(t *testing.T) {
	g := filesGroup(t, groupsvc.Stale, 20*time.Minute, 5*time.Minute)
	g.Err = errors.New("the refresh did not finish")
	eng := &fakeEngine{res: groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{g}}}
	got, err := newHandlers(eng)["files"](context.Background(), args(wireID, false))
	if err != nil {
		t.Fatal(err)
	}
	m := asJSON(t, got)
	if m["served_from"] != "cache" || m["stale"] != true || m["age_seconds"] != float64(20*60) {
		t.Errorf("served_from %v stale %v age %v, want cache, true, 1200", m["served_from"], m["stale"], m["age_seconds"])
	}
}

func TestFreshTrueIsPassedThroughToTheEngine(t *testing.T) {
	for _, op := range []string{"show", "files", "commits", "review_pending"} {
		eng := &fakeEngine{res: showResult(t)}
		_, _ = newHandlers(eng)[op](context.Background(), args(wireID, true))
		if call := eng.last(t); !call.opt.Fresh {
			t.Errorf("%s: fresh: true was not passed to the engine: %+v", op, call)
		}
	}
}

func TestEachOpAsksForItsGroups(t *testing.T) {
	for op, want := range map[string][]string{
		"show":           {"summary", "detail", "conversation"},
		"files":          {"files"},
		"commits":        {"commits"},
		"review_pending": {"pending"},
	} {
		eng := &fakeEngine{res: showResult(t)}
		_, _ = newHandlers(eng)[op](context.Background(), args(wireID, false))
		if call := eng.last(t); !slices.Equal(call.want, want) {
			t.Errorf("%s asked for %v, want %v", op, call.want, want)
		}
	}
}

func TestShowRecordsTheAnnotationsOnTheRoutersCarrier(t *testing.T) {
	rt := router.New(router.Options{})
	var got router.Annotations
	for name, h := range Handlers(Deps{Engine: &fakeEngine{res: showResult(t)}, Clock: governor.NewManualClock(t0)}) {
		inner := h.Handle
		h.Handle = func(ctx context.Context, raw json.RawMessage) (any, error) {
			res, err := inner(ctx, raw)
			got = router.AnnotationsFrom(ctx)
			return res, err
		}
		rt.Register("pr", name, h)
	}
	resp := rt.Dispatch(context.Background(), router.Call{Instance: "pr", Request: scriptout.Request{Op: "show", Args: json.RawMessage(args(wireID, false))}})
	if resp.Error != nil {
		t.Fatalf("show failed: %+v", resp.Error)
	}
	if got.ServedFrom != "cache" || got.Stale || got.AgeSeconds != 30 {
		t.Errorf("carrier = %+v, want cache, not stale, 30 s old", got)
	}
}

// ---- errors --------------------------------------------------------------------

func code(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("no error")
	}
	return scriptout.CodeForError(err)
}

func TestMalformedIDsAreInvalidArgument(t *testing.T) {
	for _, op := range Ops {
		for _, a := range []string{
			`{"id":"not-an-id"}`, `{"id":"octo/widgets"}`, `{"id":"octo/widgets#0"}`, `{"id":"octo#4"}`,
			`{}`, `{"id":""}`, `{"id":42}`, `[1]`,
		} {
			eng := &fakeEngine{res: showResult(t)}
			_, err := newHandlers(eng)[op](context.Background(), a)
			if c := code(t, err); c != "invalid_argument" {
				t.Errorf("%s %s: code %q (%v), want invalid_argument", op, a, c, err)
			}
			if len(eng.calls) != 0 {
				t.Errorf("%s %s: the engine was asked for a malformed id", op, a)
			}
		}
	}
}

func TestArgumentsDecodeAsAPlainJSONDecode(t *testing.T) {
	for _, op := range Ops {
		eng := &fakeEngine{res: showResult(t)}
		_, err := newHandlers(eng)[op](context.Background(), `{"id":"octo/widgets#42","fresh":false,"unknown_extra":[1,2]}`)
		if err != nil && code(t, err) == "invalid_argument" {
			t.Errorf("%s: an unknown extra key made the arguments invalid: %v", op, err)
		}
	}
}

func TestTheIDMatchesCaseInsensitivelyBecauseTheEngineGetsItAsWritten(t *testing.T) {
	eng := &fakeEngine{res: showResult(t)}
	got, err := newHandlers(eng)["show"](context.Background(), args("Octo/WIDGETS#42", false))
	if err != nil {
		t.Fatal(err)
	}
	if call := eng.last(t); call.id != "Octo/WIDGETS#42" {
		t.Errorf("the engine was asked for %q, want the id as written", call.id)
	}
	if id := got.(*schema.PR).ID; id != "Octo/WIDGETS#42" {
		t.Errorf("show id = %q, want the caller's", id)
	}
}

func TestANonexistentPRIsNotFound(t *testing.T) {
	for _, op := range Ops {
		eng := &fakeEngine{err: groupsvc.ErrNoSuchPR}
		_, err := newHandlers(eng)[op](context.Background(), args(wireID, false))
		if c := code(t, err); c != "not_found" {
			t.Errorf("%s: code %q, want not_found", op, c)
		}
	}
	// A summary GitHub reports gone is not_found too.
	eng := &fakeEngine{res: showResult(t)}
	eng.res.Summary.Gone = true
	_, err := newHandlers(eng)["show"](context.Background(), args(wireID, false))
	if c := code(t, err); c != "not_found" {
		t.Errorf("gone summary: code %q, want not_found", c)
	}
}

func unavailable(group string, cause error) groupsvc.EnsureResult {
	return groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{
		{Group: group, Status: groupsvc.Unavailable, Err: cause},
	}}
}

func TestAnUnavailableGroupMakesTheAnswerUnavailableNamingTheCause(t *testing.T) {
	refusal := &governor.Refusal{Kind: governor.RefusedReserve, Reason: "the graphql reserve of 300 points would be breached"}
	cases := []struct {
		name     string
		cause    error
		wantCode string
		wantText string
	}{
		{"governor refusal", refusal, "unavailable", "reserve of 300"},
		{"origin failure", errors.New("gh: connection reset by peer"), "unavailable", "connection reset"},
		{"deadline", context.DeadlineExceeded, "unavailable", "deadline"},
		{"authentication", github.ErrGHAuthInvalid, "unauthenticated", ""},
		{"rate limit", errors.New("API rate limit exceeded"), "unavailable", "rate limit"},
		{"missing PR", errors.New("GraphQL: Could not resolve to a PullRequest"), "not_found", ""},
	}
	for _, c := range cases {
		for _, op := range []string{"files", "commits", "show"} {
			group := map[string]string{"files": groups.Files, "commits": groups.Commits, "show": groups.Detail}[op]
			eng := &fakeEngine{res: unavailable(group, c.cause)}
			_, err := newHandlers(eng)[op](context.Background(), args(wireID, false))
			if got := code(t, err); got != c.wantCode {
				t.Errorf("%s/%s: code %q (%v), want %q", c.name, op, got, err, c.wantCode)
			}
			if c.wantText != "" && !strings.Contains(strings.ToLower(err.Error()), c.wantText) {
				t.Errorf("%s/%s: %q does not name the cause %q", c.name, op, err, c.wantText)
			}
		}
	}
}

func TestAnUnauthenticatedFetchStillServesTheCachedRow(t *testing.T) {
	g := filesGroup(t, groupsvc.Stale, 20*time.Minute, 5*time.Minute)
	g.Err = github.ErrGHAuthInvalid
	eng := &fakeEngine{res: groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{g}}}
	got, err := newHandlers(eng)["files"](context.Background(), args(wireID, false))
	if err != nil {
		t.Fatalf("the cached row was refused: %v", err)
	}
	if m := asJSON(t, got); m["stale"] != true || m["age_seconds"] != float64(20*60) {
		t.Errorf("stale %v age %v, want true, 1200", m["stale"], m["age_seconds"])
	}
}

func TestNoErrorOutsideTheSevenValueTaxonomy(t *testing.T) {
	valid := map[string]bool{
		"not_found": true, "unauthenticated": true, "unavailable": true, "unknown_op": true,
		"version_mismatch": true, "invalid_argument": true, "query_not_recognized": true,
	}
	causes := []error{errors.New("boom"), context.Canceled, github.ErrGHAuthInvalid, &governor.Refusal{Kind: governor.RefusedCap, Reason: "cap spent"}}
	for _, cause := range causes {
		for _, op := range Ops {
			eng := &fakeEngine{err: cause}
			_, err := newHandlers(eng)[op](context.Background(), args(wireID, false))
			if c := code(t, err); !valid[c] {
				t.Errorf("%s/%v: code %q is outside the taxonomy", op, cause, c)
			}
		}
	}
}

func TestACorruptStoredGroupIsUnavailableNotAPanic(t *testing.T) {
	g := fresh(t, groups.Files, nil)
	g.Row.Content = []byte("not json")
	eng := &fakeEngine{res: groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{g}}}
	_, err := newHandlers(eng)["files"](context.Background(), args(wireID, false))
	if c := code(t, err); c != "unavailable" {
		t.Errorf("code %q, want unavailable", c)
	}
}

func TestANilEngineIsUnavailable(t *testing.T) {
	_, err := Handlers(Deps{})["show"].Handle(context.Background(), json.RawMessage(args(wireID, false)))
	if c := code(t, err); c != "unavailable" {
		t.Errorf("code %q, want unavailable", c)
	}
}

// ---- files and commits -----------------------------------------------------------

func TestFilesAndCommitsKeepTheOldResultShapes(t *testing.T) {
	commits := prfacts.Commits{Commits: []schema.PRCommit{{SHA: "abc", Author: "phillipgreenii", Message: "Add cache"}}}
	eng := &fakeEngine{res: groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{
		{Group: groups.Commits, Status: groupsvc.Fresh, Row: row(t, groups.Commits, commits, time.Minute, time.Hour)},
	}}}
	got, err := newHandlers(eng)["commits"](context.Background(), args(wireID, false))
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*schema.PRCommitsResult)
	if res.ID != wireID || len(res.Commits) != 1 || res.Commits[0].Author != "phillipgreenii" || res.Commits[0].SHA != "abc" {
		t.Errorf("commits = %+v", res)
	}

	empty := fresh(t, groups.Files, prfacts.Files{})
	eng = &fakeEngine{res: groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{empty}}}
	got, err = newHandlers(eng)["files"](context.Background(), args(wireID, false))
	if err != nil {
		t.Fatal(err)
	}
	if raw := string(mustJSON(t, got)); !strings.Contains(raw, `"files":[]`) {
		t.Errorf("a PR with no changed files must answer files: [] — %s", raw)
	}
}

// ---- show: freshness of its three groups ------------------------------------------

func TestShowAnnotationsCoverTheOldestGroup(t *testing.T) {
	res := showResult(t)
	res.Groups[1] = groupsvc.GroupResult{
		Group: groups.Detail, Status: groupsvc.Stale,
		Row: row(t, groups.Detail, cannedDetail(), 10*time.Minute, time.Minute),
	}
	eng := &fakeEngine{res: res}
	got, err := newHandlers(eng)["show"](context.Background(), args(wireID, false))
	if err != nil {
		t.Fatal(err)
	}
	p := got.(*schema.PR)
	if !p.Stale || p.AgeSeconds == nil || *p.AgeSeconds != 600 || p.ServedFrom != "cache" || len(p.Groups) != 3 {
		t.Errorf("stale %t age %v served_from %q groups %d, want true, 600, cache, 3", p.Stale, p.AgeSeconds, p.ServedFrom, len(p.Groups))
	}
	if want := t0.Add(-10 * time.Minute).UTC().Format(time.RFC3339); p.AsOf != want {
		t.Errorf("as_of = %q, want the oldest fetched_at %q", p.AsOf, want)
	}
}

// ---- review_pending ------------------------------------------------------------------

func storedPending(t *testing.T) []byte {
	t.Helper()
	data := &github.PendingReviewData{
		HeadSHA: "head0",
		Reviews: []github.PendingReviewNode{{
			ID: "PRR_low", DatabaseID: 11, URL: "https://example.invalid/r/11", CommitOID: "head0", Body: "low body",
			Comments: []github.PendingReviewComment{{ID: "c1", Path: "a.go", Line: 3, Body: "x", OriginalCommitOID: "head1"}},
		}},
	}
	return mustJSON(t, data)
}

func pendingResult(t *testing.T, sum summary.Summary, content []byte) groupsvc.EnsureResult {
	return groupsvc.EnsureResult{Summary: sum, Groups: []groupsvc.GroupResult{
		{Group: groups.Pending, Status: groupsvc.Fresh, Row: row(t, groups.Pending, content, 15*time.Second, time.Minute)},
	}}
}

func pendingHandler(eng *fakeEngine, side *pending.SidecarView) func(context.Context, string) (any, error) {
	h := Handlers(Deps{Engine: eng, Sidecar: side, Clock: governor.NewManualClock(t0)})["review_pending"]
	return func(ctx context.Context, a string) (any, error) { return h.Handle(ctx, json.RawMessage(a)) }
}

func TestReviewPendingEqualsComputeOverTheStoredData(t *testing.T) {
	content := storedPending(t)
	eng := &fakeEngine{res: pendingResult(t, cannedSummary(), content)}
	got, err := pendingHandler(eng, nil)(context.Background(), args(wireID, false))
	if err != nil {
		t.Fatal(err)
	}
	res := got.(pr.PendingReviewResult)

	data, err := pending.Decode(content)
	if err != nil {
		t.Fatal(err)
	}
	want, err := pending.Compute(data, cannedSummary().HeadRefOid, "octo/widgets", 42, t0.Add(-15*time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The live head is the stored summary's headRefOid, not the pending group's.
	if res.HeadSHA != cannedSummary().HeadRefOid || !res.Pending || res.Review == nil {
		t.Fatalf("result = %+v", res)
	}
	if res.Review.CommentsAtHead != 0 || !res.Review.Stale {
		t.Errorf("the comment is on head1, the live head is %s: comments_at_head %d stale %t", res.HeadSHA, res.Review.CommentsAtHead, res.Review.Stale)
	}
	res.ServedFrom, res.AgeSeconds, res.Stale, res.Groups = "", nil, nil, nil
	if !equalJSON(t, res, want) {
		t.Errorf("result differs from pending.Compute's:\n got %s\nwant %s", mustJSON(t, res), mustJSON(t, want))
	}
	m := asJSON(t, got)
	if m["served_from"] != "cache" || m["age_seconds"] != float64(15) || m["stale"] != false {
		t.Errorf("annotations = %v %v %v", m["served_from"], m["age_seconds"], m["stale"])
	}
	if call := eng.last(t); !slices.Equal(call.want, []string{groups.Pending}) {
		t.Errorf("asked for %v", call.want)
	}
}

func equalJSON(t *testing.T, a, b any) bool {
	t.Helper()
	return string(mustJSON(t, a)) == string(mustJSON(t, b))
}

func TestReviewPendingTakesLastAppendFromTheSidecarView(t *testing.T) {
	store := posted.Store{Dir: t.TempDir()}
	if err := store.Save("octo", "widgets", 42, posted.State{LastAppend: &posted.LastAppend{At: "2026-10-13T11:00:00Z", Added: 2, Head: "head0"}}); err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{res: pendingResult(t, cannedSummary(), storedPending(t))}
	got, err := pendingHandler(eng, &pending.SidecarView{Store: store})(context.Background(), args(wireID, false))
	if err != nil {
		t.Fatal(err)
	}
	last := got.(pr.PendingReviewResult).Review.LastAppend
	if last == nil || last.At != "2026-10-13T11:00:00Z" || last.Added != 2 || last.Head != "head0" {
		t.Errorf("last_append = %+v", last)
	}
	// Without a sidecar there is none.
	got, _ = pendingHandler(eng, nil)(context.Background(), args(wireID, false))
	if got.(pr.PendingReviewResult).Review.LastAppend != nil {
		t.Error("last_append without a sidecar")
	}
}

func TestReviewPendingFailuresAreMappedAndPrefixed(t *testing.T) {
	const prefix = "review_pending: detection_failed:"
	cases := []struct {
		name     string
		eng      *fakeEngine
		wantCode string
	}{
		{"unavailable group", &fakeEngine{res: unavailable(groups.Pending, errors.New("gh: timeout"))}, "unavailable"},
		{"governor refusal", &fakeEngine{res: unavailable(groups.Pending, &governor.Refusal{Kind: governor.RefusedCap, Reason: "the cap is spent"})}, "unavailable"},
		{"authentication", &fakeEngine{res: unavailable(groups.Pending, github.ErrGHAuthInvalid)}, "unauthenticated"},
		{"missing PR in the group", &fakeEngine{res: unavailable(groups.Pending, errors.New("Could not resolve to a PullRequest"))}, "not_found"},
		{"missing PR in the engine", &fakeEngine{err: groupsvc.ErrNoSuchPR}, "not_found"},
		{"engine failure", &fakeEngine{err: errors.New("store closed")}, "unavailable"},
		{"unavailable group with no cause", &fakeEngine{res: unavailable(groups.Pending, nil)}, "unavailable"},
		{"stale row after a failed fetch", &fakeEngine{res: groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{
			{
				Group: groups.Pending, Status: groupsvc.Stale, Err: errors.New("gh: timeout"),
				Row: row(t, groups.Pending, storedPending(t), 10*time.Minute, time.Minute),
			},
		}}}, "unavailable"},
		{"corrupt stored group", &fakeEngine{res: pendingResult(t, cannedSummary(), []byte("not json"))}, "unavailable"},
	}
	for _, c := range cases {
		_, err := pendingHandler(c.eng, nil)(context.Background(), args(wireID, false))
		if got := code(t, err); got != c.wantCode {
			t.Errorf("%s: code %q (%v), want %q", c.name, got, err, c.wantCode)
		}
		if !strings.Contains(err.Error(), prefix) {
			t.Errorf("%s: %q lacks the prefix %q", c.name, err, prefix)
		}
	}
}

func TestReviewPendingAStaleRowWithoutAFetchFailureIsServed(t *testing.T) {
	// A fetch still running at the deadline leaves the row stale with no error.
	eng := &fakeEngine{res: groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{
		{Group: groups.Pending, Status: groupsvc.Stale, Pending: true, Row: row(t, groups.Pending, storedPending(t), 10*time.Minute, time.Minute)},
	}}}
	got, err := pendingHandler(eng, nil)(context.Background(), args(wireID, false))
	if err != nil {
		t.Fatal(err)
	}
	if m := asJSON(t, got); m["stale"] != true || m["age_seconds"] != float64(600) {
		t.Errorf("stale %v age %v, want true, 600", m["stale"], m["age_seconds"])
	}
}

func TestReviewPendingWithoutAHeadIsFailClosed(t *testing.T) {
	// No live head (the summary has none) and a stored read without one.
	sum := cannedSummary()
	sum.HeadRefOid = ""
	headless := mustJSON(t, &github.PendingReviewData{})
	eng := &fakeEngine{res: pendingResult(t, sum, headless)}
	_, err := pendingHandler(eng, nil)(context.Background(), args(wireID, false))
	if c := code(t, err); c != "unavailable" || !strings.Contains(err.Error(), "review_pending: detection_failed:") {
		t.Errorf("code %q err %v, want unavailable with the detection_failed prefix", c, err)
	}
}

func TestReviewPendingNamesTheRepoOfTheIDWhenTheSummaryIsAbsent(t *testing.T) {
	// A summary-less result (zero) with a head-less stored read: the refusal names the id's repo and number.
	headless := mustJSON(t, &github.PendingReviewData{})
	eng := &fakeEngine{res: pendingResult(t, summary.Summary{}, headless)}
	_, err := pendingHandler(eng, nil)(context.Background(), args("Octo/Widgets#7", false))
	if err == nil || !strings.Contains(err.Error(), "Octo/Widgets#7") {
		t.Errorf("err = %v, want it to name Octo/Widgets#7", err)
	}
}
