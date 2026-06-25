package query

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// recordingCmd is a Commander that captures the argv it was called with and returns
// canned output, so tests can assert both the built command line and the mapping.
type recordingCmd struct {
	argv []string
	out  []byte
	err  error
}

func (c *recordingCmd) Run(_ context.Context, argv []string) ([]byte, error) {
	c.argv = argv
	return c.out, c.err
}

func argvHasPair(argv []string, flag, val string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return true
		}
	}
	return false
}

// --- github-issues ---

func TestGitHubIssues_mapsResultsAndBuildsArgs(t *testing.T) {
	cmd := &recordingCmd{out: []byte(`[
	  {"number":12,"title":"Fix the thing","url":"https://github.com/o/r/issues/12","labels":[{"name":"bug"},{"name":"worker-ready"}]},
	  {"number":7,"title":"Another","url":"https://github.com/o/r/issues/7","labels":[]}
	]`)}
	q := GitHubIssues{Repo: "o/r", Labels: []string{"worker-ready"}}
	items, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(items), items)
	}
	got := items[0]
	if got.ID != "o/r#12" || got.Type != "github-issue" || got.Title != "Fix the thing" {
		t.Errorf("item[0] mapped wrong: %+v", got)
	}
	if got.Metadata["repo"] != "o/r" || got.Metadata["number"] != 12 || got.Metadata["url"] != "https://github.com/o/r/issues/12" {
		t.Errorf("item[0] metadata wrong: %+v", got.Metadata)
	}
	labels, _ := got.Metadata["labels"].([]string)
	if len(labels) != 2 || labels[0] != "bug" || labels[1] != "worker-ready" {
		t.Errorf("item[0] labels wrong: %+v", got.Metadata["labels"])
	}
	// argv: targets the repo, filters to open, and passes each filter label as --label.
	if !argvHasPair(cmd.argv, "--repo", "o/r") || !argvHasPair(cmd.argv, "--state", "open") || !argvHasPair(cmd.argv, "--label", "worker-ready") {
		t.Errorf("argv missing expected flags: %v", cmd.argv)
	}
}

func TestGitHubIssues_validateRequiresRepo(t *testing.T) {
	if err := (GitHubIssues{}).Validate(); err == nil {
		t.Fatal("missing repo must fail Validate")
	}
	if err := (GitHubIssues{Repo: "o/r"}).Validate(); err != nil {
		t.Fatalf("repo set must pass Validate: %v", err)
	}
}

func TestGitHubIssues_emptyStdoutIsZeroItems(t *testing.T) {
	items, err := GitHubIssues{Repo: "o/r"}.Run(context.Background(), Env{Cmd: &recordingCmd{out: []byte("")}})
	if err != nil || len(items) != 0 {
		t.Fatalf("empty stdout = zero items, no error; got items=%v err=%v", items, err)
	}
}

func TestGitHubIssues_nonZeroExitPropagates(t *testing.T) {
	_, err := GitHubIssues{Repo: "o/r"}.Run(context.Background(), Env{Cmd: &recordingCmd{err: errors.New("gh: not authenticated")}})
	if err == nil {
		t.Fatal("gh failure must propagate as error, not empty items")
	}
}

func TestGitHubIssues_malformedIsError(t *testing.T) {
	_, err := GitHubIssues{Repo: "o/r"}.Run(context.Background(), Env{Cmd: &recordingCmd{out: []byte("{not json}")}})
	if err == nil {
		t.Fatal("malformed gh output must error")
	}
}

// --- jira-issues ---

func TestJiraIssues_mapsEnvelopeAndBuildsArgs(t *testing.T) {
	cmd := &recordingCmd{out: []byte(`{"items":[
	  {"key":"PROJ-1","summary":"Do X","status":"To Do","issuetype":"Bug","labels":["a","b"],"url":"https://x/browse/PROJ-1"},
	  {"key":"PROJ-2","summary":"Do Y","status":"In Progress","issuetype":"Task","labels":[],"url":"https://x/browse/PROJ-2"}
	],"truncated":false}`)}
	q := JiraIssues{Project: "PROJ", Labels: []string{"worker-ready"}}
	items, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(items), items)
	}
	got := items[0]
	if got.ID != "PROJ-1" || got.Type != "jira-issue" || got.Title != "Do X" {
		t.Errorf("item[0] mapped wrong: %+v", got)
	}
	if got.Metadata["key"] != "PROJ-1" || got.Metadata["project"] != "PROJ" ||
		got.Metadata["issuetype"] != "Bug" || got.Metadata["status"] != "To Do" ||
		got.Metadata["url"] != "https://x/browse/PROJ-1" {
		t.Errorf("item[0] metadata wrong: %+v", got.Metadata)
	}
	if len(cmd.argv) < 2 || cmd.argv[0] != "pg-pr-issues-jira-zr" || cmd.argv[1] != "search" {
		t.Errorf("argv must invoke 'pg-pr-issues-jira-zr search': %v", cmd.argv)
	}
	if !argvHasPair(cmd.argv, "--limit", "100") || !slices.Contains(cmd.argv, "--jql") {
		t.Errorf("argv missing --jql/--limit: %v", cmd.argv)
	}
}

func TestJiraIssues_jqlExplicitTakesPrecedence(t *testing.T) {
	if got := (JiraIssues{Project: "PROJ", JQL: "assignee = currentUser()"}).jql(); got != "assignee = currentUser()" {
		t.Errorf("explicit JQL must be used verbatim, got %q", got)
	}
}

func TestJiraIssues_jqlBuiltFromProjectAndLabels(t *testing.T) {
	got := (JiraIssues{Project: "PROJ", Labels: []string{"worker-ready"}}).jql()
	want := `project = "PROJ" AND labels = "worker-ready" AND resolution = Unresolved ORDER BY created ASC`
	if got != want {
		t.Errorf("built JQL\n got %q\nwant %q", got, want)
	}
}

func TestJiraIssues_validateRequiresProjectOrJQL(t *testing.T) {
	if err := (JiraIssues{}).Validate(); err == nil {
		t.Fatal("neither project nor jql must fail Validate")
	}
	if err := (JiraIssues{JQL: "x"}).Validate(); err != nil {
		t.Fatalf("jql-only must pass Validate: %v", err)
	}
}

func TestJiraIssues_missingKeyIsError(t *testing.T) {
	cmd := &recordingCmd{out: []byte(`{"items":[{"summary":"no key"}],"truncated":false}`)}
	if _, err := (JiraIssues{Project: "PROJ"}).Run(context.Background(), Env{Cmd: cmd}); err == nil {
		t.Fatal("item missing key must error")
	}
}

func TestJiraIssues_truncatedStillReturnsItems(t *testing.T) {
	cmd := &recordingCmd{out: []byte(`{"items":[{"key":"PROJ-1","summary":"X"}],"truncated":true}`)}
	items, err := (JiraIssues{Project: "PROJ"}).Run(context.Background(), Env{Cmd: cmd})
	if err != nil || len(items) != 1 {
		t.Fatalf("truncated=true must still return items, no error; got items=%v err=%v", items, err)
	}
}

func TestJiraIssues_nonZeroExitPropagates(t *testing.T) {
	_, err := JiraIssues{Project: "PROJ"}.Run(context.Background(), Env{Cmd: &recordingCmd{err: errors.New("jira: unauthorized")}})
	if err == nil {
		t.Fatal("jira failure must propagate as error")
	}
}

func TestJiraIssues_emptyEnvelopeIsZeroItems(t *testing.T) {
	cmd := &recordingCmd{out: []byte(`{"items":[],"truncated":false}`)}
	items, err := (JiraIssues{Project: "PROJ"}).Run(context.Background(), Env{Cmd: cmd})
	if err != nil || len(items) != 0 {
		t.Fatalf("empty envelope = zero items, no error; got items=%v err=%v", items, err)
	}
}

func TestJiraIssues_malformedIsError(t *testing.T) {
	cmd := &recordingCmd{out: []byte(`{not json}`)}
	if _, err := (JiraIssues{Project: "PROJ"}).Run(context.Background(), Env{Cmd: cmd}); err == nil {
		t.Fatal("malformed envelope output must error")
	}
}

func TestIsStub_noStubTypesRemain(t *testing.T) {
	for _, q := range []Query{GitHubIssues{Repo: "o/r"}, JiraIssues{Project: "P"}, BeadsReady{}} {
		if IsStub(q) {
			t.Errorf("%T must not be reported as a stub now that Run is implemented", q)
		}
	}
}
