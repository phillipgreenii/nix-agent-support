package corpus

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// This file is the generator of the committed corpus (testdata/*.json). It is
// the readable source the fixtures were written from; the committed files are
// what every golden test reads, and TestCorpusMatchesGenerator fails when the two
// disagree. Run it with PG_DESK_UPDATE_GOLDEN=1 to rewrite the files, then run
// the formatter (nix fmt) over them.

type obj = map[string]any

// prShow builds a `pr show` result for PR number n, open and clean, by author.
func prShow(n int, author string, mod func(obj)) obj {
	p := obj{
		"repo": Repo, "number": n, "title": "Corpus change", "state": "open",
		"branch": "feature-" + itoa(n), "base": "main", "head_sha": "h2", "author": author,
		"url":   "https://example.invalid/acme/widgets/pull/" + itoa(n),
		"draft": false, "merged": false, "mergeable": "MERGEABLE", "merge_state_status": "CLEAN",
		"checks_rollup": "success", "additions": 10, "deletions": 2,
	}
	if mod != nil {
		mod(p)
	}
	return p
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func review(id, author, state, commit string) obj {
	return obj{"id": id, "author": author, "state": state, "commit_oid": commit, "submitted_at": "2026-09-15T10:00:00Z"}
}

func runs(conclusion string) obj {
	r := obj{"id": "1", "name": "build", "status": "completed", "conclusion": conclusion, "head_sha": "h2", "attempt": 1}
	if conclusion == "" {
		r = obj{"id": "1", "name": "build", "status": "in_progress", "head_sha": "h2", "attempt": 1}
	}
	return obj{"runs": []obj{r}, "sources": []string{}}
}

// facts wraps a pr show (and optional extras) in a gather.Facts payload.
func facts(pr obj, extra obj) obj {
	f := obj{"pr_show": pr, "ci": runs("success"), "as_of": "2026-09-16T00:00:00Z", "head_sha": pr["head_sha"]}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

func with(pr obj, k string, v any) obj { pr[k] = v; return pr }

func generated() map[string]obj {
	me, mate, mateB, mateC, bot := "alice-dev", "teammate", "teammate-b", "teammate-c", "review-bot"
	thread := func(author string) []obj {
		return []obj{{"id": "c1", "author": author, "body": "Please look", "thread_id": "c1", "review_thread_id": "t1", "resolved": false}}
	}
	own := func(n int, mod func(obj)) obj { return prShow(n, me, mod) }
	team := func(n int, mod func(obj)) obj { return prShow(n, mate, mod) }
	f := map[string]obj{}
	set := func(name string, o obj) { f[name] = o }

	// The own-PR reason table (OwnReasons), one fixture per reason code.
	set("pr-own-ci-failing", facts(own(101, nil), obj{"ci": runs("failure")}))
	set("pr-own-bot-disapproved", facts(own(102, func(p obj) { p["reviews"] = []obj{review("r1", bot, "CHANGES_REQUESTED", "h2")} }), nil))
	set("pr-own-changes-requested", facts(own(103, func(p obj) { p["reviews"] = []obj{review("r1", mateB, "CHANGES_REQUESTED", "h2")} }), nil))
	set("pr-own-conflict", facts(own(104, func(p obj) { p["mergeable"] = "CONFLICTING"; p["merge_state_status"] = "DIRTY" }), nil))
	set("pr-own-open-thread", facts(own(105, func(p obj) { p["comments"] = thread(mate) }), nil))
	set("pr-own-approved-ready", facts(own(106, func(p obj) { p["reviews"] = []obj{review("r1", mateB, "APPROVED", "h2")} }), nil))
	set("pr-own-own-draft", facts(own(107, func(p obj) { p["draft"] = true }), nil))
	set("pr-own-awaiting-review", facts(own(108, nil), nil))

	// The team-PR reason table (TeamReasons), one fixture per reason code.
	set("pr-team-ci-failing", facts(team(201, nil), obj{"ci": runs("failure")}))
	set("pr-team-conflict", facts(team(202, func(p obj) { p["mergeable"] = "CONFLICTING"; p["merge_state_status"] = "DIRTY" }), nil))
	set("pr-team-changes-requested", facts(team(203, func(p obj) { p["reviews"] = []obj{review("r1", mateB, "CHANGES_REQUESTED", "h2")} }), nil))
	set("pr-team-stale-review", facts(team(204, func(p obj) {
		p["reviews"] = []obj{review("r1", me, "COMMENTED", "h1"), review("r2", mateB, "APPROVED", "h2")}
	}), nil))
	set("pr-team-review-requested", facts(team(205, func(p obj) { p["review_requests"] = []string{me} }), nil))
	set("pr-team-acting-reviewer", facts(team(206, func(p obj) { p["reviews"] = []obj{review("r1", me, "COMMENTED", "h2")} }), nil))
	set("pr-team-bot-disapproved", facts(team(207, func(p obj) { p["reviews"] = []obj{review("r1", bot, "CHANGES_REQUESTED", "h2")} }), nil))
	set("pr-team-approved-merge-blocked", facts(team(208, func(p obj) {
		p["merge_state_status"] = "BLOCKED"
		p["reviews"] = []obj{review("r1", mateB, "APPROVED", "h2")}
	}), nil))
	set("pr-team-approved-ci-unsettled", facts(team(209, func(p obj) {
		p["merge_state_status"] = "BLOCKED"
		p["reviews"] = []obj{review("r1", mateB, "APPROVED", "h2")}
	}), obj{"ci": runs("")}))
	set("pr-team-approved-awaiting-requested", facts(team(210, func(p obj) {
		p["merge_state_status"] = "UNKNOWN"
		p["review_requests"] = []string{mateC}
		p["reviews"] = []obj{review("r1", mateB, "APPROVED", "h2")}
	}), nil))
	set("pr-team-approved-ready-to-merge", facts(team(211, func(p obj) { p["reviews"] = []obj{review("r1", mateB, "APPROVED", "h2")} }), nil))
	set("pr-team-unanswered-thread", facts(team(212, func(p obj) { p["comments"] = thread(me) }), nil))
	set("pr-team-awaiting-review", facts(team(213, nil), nil))

	// Approval, CI, draft, conflict, stack, state and annotation-adjacent states.
	set("pr-approval-human-approved", facts(team(301, func(p obj) {
		p["reviews"] = []obj{review("r1", mateB, "APPROVED", "h2"), review("r2", me, "APPROVED", "h2")}
	}), nil))
	set("pr-approval-commented-only", facts(team(302, func(p obj) { p["reviews"] = []obj{review("r1", mateB, "COMMENTED", "h2")} }), nil))
	set("pr-approval-bot-approved", facts(team(303, func(p obj) { p["reviews"] = []obj{review("r1", bot, "APPROVED", "h2")} }), nil))
	set("pr-ci-pending", facts(own(310, nil), obj{"ci": runs("")}))
	set("pr-ci-none", facts(own(311, func(p obj) { p["checks_rollup"] = "none" }), obj{"ci": obj{"runs": []obj{}, "sources": []string{}}}))
	set("pr-ci-unknown", facts(own(312, func(p obj) { delete(p, "checks_rollup") }), obj{"ci": obj{"runs": []obj{}, "sources": []string{}}}))
	set("pr-draft-team", facts(team(320, func(p obj) { p["draft"] = true; p["review_requests"] = []string{me} }), nil))
	set("pr-team-unmatched", facts(prShow(321, mateC, nil), nil))
	set("pr-stack-parent-conflict", facts(team(330, func(p obj) {
		p["branch"], p["base"] = "child", "parent"
		p["reviews"] = []obj{review("r1", me, "APPROVED", "h1")}
	}), obj{"stack_parents": []obj{{"id": Repo + "#329", "branch": "parent", "base": "main", "state": "open", "mergeable": "CONFLICTING", "merge_state_status": "DIRTY"}}}))
	set("pr-state-closed", facts(with(own(340, nil), "state", "closed"), nil))
	set("pr-state-merged", facts(with(with(team(341, nil), "state", "merged"), "merged", true), nil))
	set("pr-degraded-gather", facts(own(350, nil), obj{"degraded": "ci list failed"}))
	set("pr-degraded-truncated", facts(own(351, func(p obj) {
		p["connections"] = obj{"threads": obj{"truncated": true}, "comments": obj{"truncated": false}, "reviews": obj{"truncated": false}}
	}), nil))
	set("pr-empty-facts", obj{"as_of": "2026-09-16T00:00:00Z", "degraded": "pr show failed"})
	set("pr-waiting-on-me-deps", facts(own(360, nil), obj{
		"work_beads": obj{"entities": []obj{{"id": "bd-1"}}, "present_ids": []string{"bd-1"}, "sources": []string{}},
		"deps":       obj{"ids": []string{"bd-2"}, "entities": []obj{{"id": "bd-2", "state": "open", "owner": me, "labels": []string{"human"}}}},
	}))
	set("pr-rich-enrichment", facts(own(370, func(p obj) {
		p["title"] = "Fix urgent crash in parser"
		p["labels"] = []string{"bug", "urgent"}
		p["body"] = "Fixes the parser crash. Tracks PROJ-12."
		p["additions"], p["deletions"] = 480, 120
	}), obj{
		"pr_files":   obj{"files": []obj{{"path": "internal/parser/parse.go"}, {"path": "docs/notes.md"}, {"path": "web/app.ts"}}},
		"pr_commits": obj{"commits": []obj{{"sha": "c1", "author": me, "message": "fix: crash on empty input"}}},
	}))

	// Issues and threads (the generic entity types).
	set("issue-owned-by-me", obj{"issue_show": obj{"id": "PROJ-1", "owner": me, "assignee": me, "issue_type": "task", "state": "open"}})
	set("issue-owned-by-other", obj{"issue_show": obj{"id": "PROJ-2", "owner": mate, "assignee": mateB, "issue_type": "bug", "state": "open"}})
	set("issue-with-deps", obj{
		"issue_show": obj{"id": "PROJ-3", "owner": me, "assignee": "", "issue_type": "feature", "state": "in_progress"},
		"issue_deps": obj{"ids": []string{"PROJ-1"}, "entities": []obj{}},
	})
	set("issue-no-show", obj{})
	set("thread-basic", obj{"thread_show": obj{"id": "C1/1.5", "channel": "C1", "participants": []string{me, mate}}})
	set("thread-empty", obj{})
	return f
}

func TestCorpusMatchesGenerator(t *testing.T) {
	want := generated()
	update := os.Getenv(UpdateEnv) == "1"
	if update {
		old, _ := filepath.Glob("testdata/*.json")
		for _, p := range old {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}
		for name, o := range want {
			b, err := json.MarshalIndent(o, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join("testdata", name+".json"), append(b, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, fx := range got {
		names = append(names, fx.Name)
		o, ok := want[fx.Name]
		if !ok {
			t.Errorf("committed fixture %s has no generator entry", fx.Name)
			continue
		}
		b, _ := json.Marshal(o)
		var c bytes.Buffer
		_ = json.Compact(&c, b)
		// Compare decoded, key order is not the point here.
		var a, bb any
		_ = json.Unmarshal(fx.Payload, &a)
		_ = json.Unmarshal(c.Bytes(), &bb)
		if !jsonEqual(a, bb) {
			t.Errorf("fixture %s differs from its generator entry (rewrite with %s=1)", fx.Name, UpdateEnv)
		}
	}
	sort.Strings(names)
	if len(names) != len(want) && !update {
		t.Errorf("%d committed fixtures, generator makes %d", len(names), len(want))
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
