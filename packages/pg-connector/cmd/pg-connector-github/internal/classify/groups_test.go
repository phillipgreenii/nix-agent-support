package classify

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/conversation"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/prfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func mustGroup(t *testing.T, group string, prev, next []byte, bodyCap int) Result {
	t.Helper()
	res, err := Group(group, prev, next, bodyCap)
	if err != nil {
		t.Fatalf("Group(%s): %v", group, err)
	}
	return res
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// CI changes: with the rollup held at failure on both sides, each of these is
// one ci_changed carrying the changed run or job fields.
func TestRunsCIChanges(t *testing.T) {
	for _, tc := range []struct {
		name, prev, next string
		fields           []string
	}{
		{"second job failing", "runs_failed.json", "runs_second_job_failed.json", []string{"job:100/lint"}},
		{"job results arriving for a failed run", "runs_failed_nojobs.json", "runs_failed.json", []string{"job:100/build", "job:100/lint"}},
		{"a newer cancelled run superseding an older failure", "runs_failed.json", "runs_superseded.json", []string{"run:102"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The rollup stays at failure, so the summary adds no ci_changed of its own.
			prevSum, nextSum := richSummary(), richSummary()
			if res := Summary(&prevSum, storedOf(nextSum), 100); !res.Empty() {
				t.Fatalf("held rollup produced %+v", res)
			}
			res := mustGroup(t, groups.Runs, fixture(t, tc.prev), fixture(t, tc.next), 100)
			wantResult(t, res, []string{KindCIChanged}, tc.fields)
			golden(t, "runs_"+strings.ReplaceAll(tc.name, " ", "_"), res)
		})
	}
}

func TestRunsTimestampOnlyAndLogURLOnlyChangesAreNoSignal(t *testing.T) {
	base := fixture(t, "runs_failed.json")
	for _, name := range []string{"runs_timestamp_only.json", "runs_url_only.json"} {
		next := fixture(t, name)
		if res := mustGroup(t, groups.Runs, base, next, 100); !res.Empty() {
			t.Errorf("%s: %+v", name, res)
		}
		// The classifier agrees with the fingerprint, which already leaves
		// exactly those fields out.
		if fp(t, base) != fp(t, next) {
			t.Errorf("%s: the fingerprint moved, so the classifier and it disagree", name)
		}
	}
	for _, name := range []string{"runs_second_job_failed.json", "runs_superseded.json", "runs_failed_nojobs.json"} {
		if fp(t, base) == fp(t, fixture(t, name)) {
			t.Errorf("%s: the fingerprint did not move", name)
		}
	}
}

func fp(t *testing.T, content []byte) string {
	t.Helper()
	c, err := runs.Decode(content)
	if err != nil {
		t.Fatal(err)
	}
	return runs.Fingerprint(c)
}

func TestRunsFieldNaming(t *testing.T) {
	res := mustGroup(t, groups.Runs, fixture(t, "runs_failed.json"), fixture(t, "runs_superseded.json"), 100)
	if len(res.Fields) != 1 || res.Fields[0].Before != nil ||
		string(res.Fields[0].After) != `{"attempt":1,"workflow":"ci","head_sha":"aaa111","status":"completed","conclusion":"cancelled"}` {
		t.Fatalf("added run field = %+v", res.Fields)
	}
	// A re-run keeps the run id and moves the attempt: one field, both sides.
	prev := runs.Content{Runs: []schema.CIRun{{ID: "5", Name: "ci", Attempt: 1, Status: "completed", Conclusion: "failure"}}}
	next := runs.Content{Runs: []schema.CIRun{{ID: "5", Name: "ci", Attempt: 2, Status: "in_progress"}}}
	res = mustGroup(t, groups.Runs, mustJSON(t, prev), mustJSON(t, next), 100)
	wantResult(t, res, []string{KindCIChanged}, []string{"run:5"})
	if res.Fields[0].Before == nil || res.Fields[0].After == nil {
		t.Fatalf("a re-run field lost a side: %+v", res.Fields[0])
	}
	// Repeated job names within one run stay separate fields.
	dup := func(c1, c2 string) []byte {
		return mustJSON(t, runs.Content{Runs: []schema.CIRun{{ID: "6", Jobs: []schema.CIJob{
			{Name: "shard", Status: "completed", Conclusion: c1}, {Name: "shard", Status: "completed", Conclusion: c2},
		}}}})
	}
	res = mustGroup(t, groups.Runs, dup("failure", "success"), dup("failure", "failure"), 100)
	wantResult(t, res, []string{KindCIChanged}, []string{"job:6/shard#2"})
	// A reordered run list alone is not a change.
	a := runs.Content{Runs: []schema.CIRun{{ID: "1", Name: "x"}, {ID: "2", Name: "y"}}}
	b := runs.Content{Runs: []schema.CIRun{{ID: "2", Name: "y"}, {ID: "1", Name: "x"}}}
	if res := mustGroup(t, groups.Runs, mustJSON(t, a), mustJSON(t, b), 100); !res.Empty() {
		t.Fatalf("reorder: %+v", res)
	}
}

func TestConversationChanges(t *testing.T) {
	before := fixture(t, "conversation_before.json")
	for _, tc := range []struct {
		name, next string
		kinds      []string
		fields     []string
	}{
		{"edited comment body", "conversation_after_edit.json", nil, []string{"comment:c1"}},
		{"resolved inline comment", "conversation_after_resolved.json", []string{KindFeedbackChanged}, []string{"comment_resolved:c3"}},
		{"review state", "conversation_after_approved.json", []string{KindReviewChanged}, []string{"review:r1"}},
		{"only fields that are not signals", "conversation_after_inert.json", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := mustGroup(t, groups.Conversation, before, fixture(t, tc.next), 100)
			wantResult(t, res, tc.kinds, tc.fields)
			golden(t, "conversation_"+strings.ReplaceAll(tc.name, " ", "_"), res)
		})
	}
}

func TestConversationAddedAndRemovedComments(t *testing.T) {
	c := func(ids ...string) []byte {
		var cs []schema.PRComment
		for _, id := range ids {
			cs = append(cs, schema.PRComment{ID: id, Body: "body " + id})
		}
		return mustJSON(t, conversation.Content{Comments: cs})
	}
	res := mustGroup(t, groups.Conversation, c("a"), c("a", "b"), 100)
	wantResult(t, res, []string{KindFeedbackChanged}, []string{"comment:b"})
	if res.Fields[0].Before != nil || res.Fields[0].After == nil {
		t.Errorf("added comment = %+v", res.Fields[0])
	}
	res = mustGroup(t, groups.Conversation, c("a", "b"), c("a"), 100)
	wantResult(t, res, []string{KindFeedbackChanged}, []string{"comment:b"})
	if res.Fields[0].Before == nil || res.Fields[0].After != nil {
		t.Errorf("removed comment = %+v", res.Fields[0])
	}
}

// A conversation body change records the comment id, each body capped at the
// cap and a hash of the full text, never the whole conversation.
func TestConversationBodyCap(t *testing.T) {
	const bodyCap = 8
	conv := func(body string) []byte {
		return mustJSON(t, conversation.Content{
			Comments: []schema.PRComment{{ID: "c9", Body: body}, {ID: "other", Body: strings.Repeat("z", 5000)}},
		})
	}
	cut := strings.Repeat("a", bodyCap+1) // bodyCap+1 bytes: cut
	whole := strings.Repeat("b", bodyCap) // bodyCap bytes: not cut
	res := mustGroup(t, groups.Conversation, conv(cut), conv(whole), bodyCap)
	wantResult(t, res, nil, []string{"comment:c9"})
	type side struct{ Body, Hash string }
	var b, a side
	if err := json.Unmarshal(res.Fields[0].Before, &b); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(res.Fields[0].After, &a); err != nil {
		t.Fatal(err)
	}
	if b.Body != strings.Repeat("a", bodyCap) || b.Hash != textHash(cut) {
		t.Errorf("before = %+v", b)
	}
	if a.Body != whole || a.Hash != textHash(whole) {
		t.Errorf("after = %+v", a)
	}
	if enc, _ := json.Marshal(res); strings.Contains(string(enc), "zzzzz") {
		t.Error("the unchanged comment leaked into the row")
	}
}

func TestDetailChanges(t *testing.T) {
	base := detail.Detail{MergeStateStatus: "CLEAN", ReviewRequests: []string{"a", "b"}, Additions: 1, Deletions: 2, ChangedFiles: 3}
	mod := func(f func(*detail.Detail)) []byte { d := base; f(&d); return mustJSON(t, d) }
	prev := mustJSON(t, base)
	for _, tc := range []struct {
		name   string
		next   []byte
		kinds  []string
		fields []string
	}{
		{"merged", mod(func(d *detail.Detail) { d.Merged = true }), []string{KindMerged}, []string{"merged"}},
		{"requested reviewers", mod(func(d *detail.Detail) { d.ReviewRequests = []string{"a", "c"} }), nil, []string{"review_requests"}},
		{"reordered reviewers", mod(func(d *detail.Detail) { d.ReviewRequests = []string{"b", "a"} }), nil, nil},
		{"size", mod(func(d *detail.Detail) { d.Additions, d.Deletions, d.ChangedFiles = 5, 6, 7 }), nil, []string{"additions", "changed_files", "deletions"}},
		{"merge state status is not a signal", mod(func(d *detail.Detail) { d.MergeStateStatus = "BEHIND" }), nil, nil},
		{"unchanged", prev, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantResult(t, mustGroup(t, groups.Detail, prev, tc.next, 100), tc.kinds, tc.fields)
		})
	}
	// merged going false again is a field, not a merged kind.
	merged := mod(func(d *detail.Detail) { d.Merged = true })
	wantResult(t, mustGroup(t, groups.Detail, merged, prev, 100), nil, []string{"merged"})
}

// Files and commits are recorded in a bounded form: a PR with thousands of
// files never puts a whole list into a row.
func TestFilesAndCommitsAreBounded(t *testing.T) {
	files := func(n int, tweak bool) []byte {
		out := prfacts.Files{Files: make([]schema.PRFile, 0, n)}
		for i := range n {
			out.Files = append(out.Files, schema.PRFile{Path: fmt.Sprintf("dir/file-%04d.go", i), Additions: 1, Deletions: 1})
		}
		if tweak {
			out.Files[n-1].Additions = 9
		}
		return mustJSON(t, out)
	}
	res := mustGroup(t, groups.Files, files(3000, false), files(3000, true), 100)
	wantResult(t, res, nil, []string{"files"})
	if len(res.Fields[0].Before) > 200 || len(res.Fields[0].After) > 200 {
		t.Fatalf("files field is not bounded: %d and %d bytes", len(res.Fields[0].Before), len(res.Fields[0].After))
	}
	if string(res.Fields[0].Before[:12]) != `{"count":300` {
		t.Errorf("files before = %s", res.Fields[0].Before)
	}
	if res := mustGroup(t, groups.Files, files(5, false), files(5, false), 100); !res.Empty() {
		t.Errorf("identical files: %+v", res)
	}

	commits := func(n int, last string) []byte {
		out := prfacts.Commits{Commits: make([]schema.PRCommit, 0, n)}
		for i := range n {
			out.Commits = append(out.Commits, schema.PRCommit{SHA: fmt.Sprintf("sha%03d", i), Author: "phillipgreenii", Message: "m"})
		}
		out.Commits[n-1].SHA = last
		return mustJSON(t, out)
	}
	res = mustGroup(t, groups.Commits, commits(900, "sha899"), commits(901, "newtip"), 100)
	wantResult(t, res, nil, []string{"commits"})
	if len(res.Fields[0].After) > 200 || !strings.Contains(string(res.Fields[0].After), `"last_sha":"newtip"`) {
		t.Fatalf("commits after = %s", res.Fields[0].After)
	}
}

func TestFirstFetchIsTheGroupsBaseline(t *testing.T) {
	rich := map[string][]byte{
		groups.Detail:       mustJSON(t, detail.Detail{Merged: true, Additions: 40}),
		groups.Conversation: fixture(t, "conversation_after_edit.json"),
		groups.Files:        mustJSON(t, prfacts.Files{Files: []schema.PRFile{{Path: "a.go", Additions: 1}}}),
		groups.Commits:      mustJSON(t, prfacts.Commits{Commits: []schema.PRCommit{{SHA: "abc"}}}),
		groups.Runs:         fixture(t, "runs_failed.json"),
	}
	for group, next := range rich {
		for _, prev := range [][]byte{nil, {}, []byte("  ")} {
			if res := mustGroup(t, group, prev, next, 100); !res.Empty() {
				t.Errorf("%s: first fetch (prev %q) yielded %+v", group, prev, res)
			}
		}
	}
}

func TestPendingGroupIsNeverClassified(t *testing.T) {
	if res := mustGroup(t, groups.Pending, []byte(`{"a":1}`), []byte(`{"a":2}`), 100); !res.Empty() {
		t.Errorf("pending: %+v", res)
	}
	if res := mustGroup(t, groups.Pending, nil, []byte(`not json`), 100); !res.Empty() {
		t.Errorf("pending: %+v", res)
	}
}

func TestGroupErrors(t *testing.T) {
	good := fixture(t, "runs_failed.json")
	for _, tc := range []struct {
		name, group string
		prev, next  []byte
	}{
		{"unknown group", "nonsense", good, good},
		{"unknown group on a first fetch", "nonsense", nil, good},
		{"the summary group has its own function", groups.Summary, good, good},
		{"undecodable previous content", groups.Runs, []byte(`{`), good},
		{"undecodable next content", groups.Runs, good, []byte(`{`)},
		{"empty next content", groups.Detail, []byte(`{"merged":false}`), nil},
	} {
		if _, err := Group(tc.group, tc.prev, tc.next, 100); err == nil {
			t.Errorf("%s: no error", tc.name)
		}
	}
}

func storedOf(s summary.Summary) summary.Stored { return summary.Stored{Summary: s} }
