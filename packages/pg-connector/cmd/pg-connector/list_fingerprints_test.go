package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// issueListReply builds a fake issue backend's "list" reply carrying one
// entity whose status and metadata.last_checked_at are parameterized, plus an
// optional fingerprint_excludes declaration and truncated flag.
func issueListReply(status, lastChecked string, excludes []string, truncated bool) string {
	exc := "null"
	if excludes != nil {
		b, _ := json.Marshal(excludes)
		exc = string(b)
	}
	return fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"tp-1","title":"t","state":%q,"as_of":"2026-10-05T00:00:00Z","stale":false,"metadata":{"last_checked_at":%q,"owner":"x"}}],"present_ids":["tp-1"],"cursor":null,"truncated":%t,"fingerprint_excludes":%s}}`,
		status, lastChecked, truncated, exc)
}

func listIssueFingerprints(t *testing.T, backend, reply string) (issueListOutcome, string) {
	t.Helper()
	writeOpAwareFakeBackend(t, backend, map[string]string{"list": reply}, `{}`)
	writeIssueConfigFor(t, backend)
	stdout, _, code := executePr(t, []string{"issue", "list", "--query", "mine", "--fingerprints", "--output", "json"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var outcome issueListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	return outcome, stdout
}

func TestRun_PrList_Fingerprints_CoverEveryEntityAndAreStable(t *testing.T) {
	reply := `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[` +
		`{"id":"<owner>/<repo>#1","repo":"<owner>/<repo>","number":1,"title":"a","state":"open","as_of":"2026-10-05T00:00:00Z"},` +
		`{"id":"<owner>/<repo>#2","repo":"<owner>/<repo>","number":2,"title":"b","state":"open","as_of":"2026-10-05T00:00:00Z"}],` +
		`"present_ids":["<owner>/<repo>#1","<owner>/<repo>#2"],"cursor":null,"truncated":false}}`
	writeOpAwareFakeBackend(t, "backend-pr-fp", map[string]string{"list": reply}, `{}`)
	writeConfigFor(t, "backend-pr-fp")

	run := func(args ...string) (prListOutcome, string) {
		stdout, _, code := executePr(t, append([]string{"pr", "list", "--query", "mine", "--output", "json"}, args...))
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
		}
		var o prListOutcome
		if err := json.Unmarshal([]byte(stdout), &o); err != nil {
			t.Fatalf("decode: %v (stdout=%s)", err, stdout)
		}
		return o, stdout
	}

	first, _ := run("--fingerprints")
	if len(first.Entities) != 2 || len(first.Fingerprints) != 2 {
		t.Fatalf("entities=%d fingerprints=%v, want a fingerprint for each of 2 entities", len(first.Entities), first.Fingerprints)
	}
	for _, e := range first.Entities {
		if first.Fingerprints[e.ID] == "" {
			t.Fatalf("no fingerprint for entity %q: %v", e.ID, first.Fingerprints)
		}
	}
	if first.Fingerprints["<owner>/<repo>#1"] == first.Fingerprints["<owner>/<repo>#2"] {
		t.Fatalf("distinct entities share a fingerprint: %v", first.Fingerprints)
	}
	second, _ := run("--fingerprints")
	for id, fp := range first.Fingerprints {
		if second.Fingerprints[id] != fp {
			t.Fatalf("fingerprint of unchanged entity %q differs across calls: %q vs %q", id, fp, second.Fingerprints[id])
		}
	}

	// Without the flag the key is absent and the outcome still decodes.
	_, raw := run()
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["fingerprints"]; ok {
		t.Fatalf("fingerprints key present without --fingerprints: %s", raw)
	}
}

func TestRun_IssueList_Fingerprints_CoverEveryEntity(t *testing.T) {
	out, _ := listIssueFingerprints(t, "backend-issue-fp", issueListReply("open", "t1", nil, false))
	if len(out.Entities) != 1 || out.Fingerprints["tp-1"] == "" || len(out.Fingerprints) != 1 {
		t.Fatalf("entities=%+v fingerprints=%v, want one fingerprint keyed tp-1", out.Entities, out.Fingerprints)
	}
}

// TestRun_IssueList_Fingerprints_HonorsBackendDeclaredExcludes: a change to
// metadata.last_checked_at leaves the fingerprint equal when the backend
// declares it volatile (the beads backend does) and a status change does not;
// a backend that declares nothing sees last_checked_at as content.
func TestRun_IssueList_Fingerprints_HonorsBackendDeclaredExcludes(t *testing.T) {
	exc := []string{"metadata.last_checked_at"}
	a, _ := listIssueFingerprints(t, "backend-issue-fp-a", issueListReply("open", "2026-10-05T00:00:00Z", exc, false))
	b, _ := listIssueFingerprints(t, "backend-issue-fp-b", issueListReply("open", "2026-10-06T09:09:09Z", exc, false))
	c, _ := listIssueFingerprints(t, "backend-issue-fp-c", issueListReply("closed", "2026-10-05T00:00:00Z", exc, false))
	if a.Fingerprints["tp-1"] != b.Fingerprints["tp-1"] {
		t.Fatalf("last_checked_at change moved the fingerprint: %q vs %q", a.Fingerprints["tp-1"], b.Fingerprints["tp-1"])
	}
	if a.Fingerprints["tp-1"] == c.Fingerprints["tp-1"] {
		t.Fatalf("status change left the fingerprint equal: %q", a.Fingerprints["tp-1"])
	}

	d, _ := listIssueFingerprints(t, "backend-issue-fp-d", issueListReply("open", "2026-10-05T00:00:00Z", nil, false))
	e, _ := listIssueFingerprints(t, "backend-issue-fp-e", issueListReply("open", "2026-10-06T09:09:09Z", nil, false))
	if d.Fingerprints["tp-1"] == e.Fingerprints["tp-1"] {
		t.Fatalf("with no declared excludes a last_checked_at change must change the fingerprint")
	}
}

// TestRun_IssueList_Fingerprints_StatusCategoryChangesFingerprint (bead
// pg2-mj0jv): status_category is ordinary entity content, not a volatile
// field, so an issue that gains or changes a category fingerprints
// differently. This is why the first poll after the Jira connector starts
// carrying it re-hydrates every Jira issue at once.
func TestRun_IssueList_Fingerprints_StatusCategoryChangesFingerprint(t *testing.T) {
	reply := func(category string) string {
		field := ""
		if category != "" {
			field = `"status_category":"` + category + `",`
		}
		return `{"protocolVersion":1,"schemaVersion":8,"result":{"entities":[{"id":"tp-1","title":"t","state":"Complete",` +
			field + `"as_of":"2026-10-05T00:00:00Z","stale":false}],"present_ids":["tp-1"],"cursor":null,"truncated":false}}`
	}
	none, _ := listIssueFingerprints(t, "backend-issue-fp-cat-none", reply(""))
	done, _ := listIssueFingerprints(t, "backend-issue-fp-cat-done", reply("done"))
	done2, _ := listIssueFingerprints(t, "backend-issue-fp-cat-done2", reply("done"))
	started, _ := listIssueFingerprints(t, "backend-issue-fp-cat-started", reply("indeterminate"))
	if none.Fingerprints["tp-1"] == done.Fingerprints["tp-1"] {
		t.Fatalf("gaining a status category left the fingerprint equal: %q", none.Fingerprints["tp-1"])
	}
	if done.Fingerprints["tp-1"] == started.Fingerprints["tp-1"] {
		t.Fatalf("changing the status category left the fingerprint equal: %q", done.Fingerprints["tp-1"])
	}
	if done.Fingerprints["tp-1"] != done2.Fingerprints["tp-1"] {
		t.Fatalf("an unchanged category must fingerprint identically: %q vs %q",
			done.Fingerprints["tp-1"], done2.Fingerprints["tp-1"])
	}
}

func TestRun_IssueList_Truncated_PropagatesFromBackend(t *testing.T) {
	yes, raw := listIssueFingerprints(t, "backend-issue-trunc-yes", issueListReply("open", "t", nil, true))
	if !yes.Truncated {
		t.Fatalf("truncated = false, want true when the backend result is truncated: %s", raw)
	}
	no, raw := listIssueFingerprints(t, "backend-issue-trunc-no", issueListReply("open", "t", nil, false))
	if no.Truncated {
		t.Fatalf("truncated = true, want false: %s", raw)
	}
}

func TestRun_PrList_Truncated_PropagatesFromBackend(t *testing.T) {
	for _, want := range []bool{true, false} {
		reply := fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":["o/r#1"],"cursor":null,"truncated":%t}}`, want)
		name := fmt.Sprintf("backend-pr-trunc-%t", want)
		writeOpAwareFakeBackend(t, name, map[string]string{"list": reply}, `{}`)
		writeConfigFor(t, name)
		stdout, _, code := executePr(t, []string{"pr", "list", "--query", "mine", "--ids-only", "--output", "json"})
		if code != 0 {
			t.Fatalf("exit code = %d; stdout=%s", code, stdout)
		}
		var o prListOutcome
		if err := json.Unmarshal([]byte(stdout), &o); err != nil {
			t.Fatal(err)
		}
		if o.Truncated != want {
			t.Fatalf("truncated = %v, want %v (stdout=%s)", o.Truncated, want, stdout)
		}
	}
}

func TestCanonicalHashExcluding_DottedPaths(t *testing.T) {
	h := func(js string, excludes ...string) string {
		s, err := canonicalHashExcluding(json.RawMessage(js), excludes)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	ex := "metadata.last_checked_at"
	if h(`{"id":"a","metadata":{"last_checked_at":"1","k":"v"}}`, ex) != h(`{"id":"a","metadata":{"last_checked_at":"2","k":"v"}}`, ex) {
		t.Error("excluded nested key must not affect the hash")
	}
	if h(`{"id":"a","metadata":{"last_checked_at":"1","k":"v"}}`, ex) == h(`{"id":"a","metadata":{"last_checked_at":"1","k":"w"}}`, ex) {
		t.Error("a sibling of the excluded key must still affect the hash")
	}
	if h(`{"id":"a","metadata":{"last_checked_at":"1"}}`, ex) != h(`{"id":"a"}`, ex) {
		t.Error("a parent emptied by the exclusion must hash like an absent parent")
	}
	if h(`{"id":"a","title":"x"}`, ex) != h(`{"id":"a","title":"x"}`) {
		t.Error("an absent excluded path must be a no-op")
	}
	if h(`{"id":"a","metadata":"scalar"}`, ex) != h(`{"id":"a","metadata":"scalar"}`) {
		t.Error("a non-object on the path must be left unchanged")
	}
	if h(`{"id":"a","as_of":"1","stale":true}`) != h(`{"id":"a"}`) {
		t.Error("as_of and stale stay excluded")
	}
}

func TestAddListFingerprint_BeadsStatusVsLastChecked(t *testing.T) {
	exc := []string{"metadata.last_checked_at"}
	fp := func(status, lc string) string {
		m := map[string]string{}
		addListFingerprint(m, "tp-1", schema.Issue{ID: "tp-1", Title: "t", State: status, Metadata: map[string]string{"last_checked_at": lc}}, exc)
		return m["tp-1"]
	}
	if fp("open", "a") == "" {
		t.Fatal("no fingerprint recorded")
	}
	if fp("open", "a") != fp("open", "b") {
		t.Error("last_checked_at change must not change the beads fingerprint")
	}
	if fp("open", "a") == fp("closed", "a") {
		t.Error("status change must change the beads fingerprint")
	}
}
