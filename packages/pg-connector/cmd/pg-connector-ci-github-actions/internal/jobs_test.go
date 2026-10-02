// jobs_test.go covers ListRuns' per-job results (bead pg2-gllcn): jobs are
// attached to non-successful, completed runs on the PR's current head and
// nowhere else, bounded, and best-effort. The gh payloads are the recorded
// fixtures under testdata/ (shaped per `gh run list`/`gh run view --json
// jobs`).
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

// jobFetchCalls returns the recorded `gh run view ... --json jobs` calls.
func jobFetchCalls(gh *fakeGH) [][]string {
	var out [][]string
	for _, c := range gh.calls {
		if len(c) >= 2 && c[0] == "run" && c[1] == "view" && strings.Contains(strings.Join(c, " "), "--json jobs") {
			out = append(out, c)
		}
	}
	return out
}

func TestListRuns_FailedRunCarriesJobsWithConclusions(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = readFixture(t, "run_list_pr_checks_failed.json")
	gh.responses["run view"] = readFixture(t, "run_view_jobs_failed.json")
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})

	runs, err := p.ListRuns(context.Background(), "foo/bar#42")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(runs))
	}
	failed := runs[0]
	if failed.ID != "1001" || failed.Name != "PR Checks" || failed.Conclusion != "failure" {
		t.Fatalf("run[0] = %+v", failed)
	}
	if len(failed.Jobs) != 2 {
		t.Fatalf("run[0].Jobs = %+v, want 2 jobs", failed.Jobs)
	}
	if j := failed.Jobs[0]; j.Name != "changes" || j.Conclusion != "success" || j.Status != "completed" || j.ID != "70010" {
		t.Fatalf("job[0] = %+v", j)
	}
	if j := failed.Jobs[1]; j.Name != "build-test-validate" || j.Conclusion != "failure" || j.ID != "70011" ||
		j.URL != "https://github.com/foo/bar/actions/runs/1001/job/70011" {
		t.Fatalf("job[1] = %+v", j)
	}

	// Exactly one job fetch: the successful run and the superseded
	// (older-head) failed run must not trigger one.
	calls := jobFetchCalls(gh)
	if len(calls) != 1 {
		t.Fatalf("job fetches = %v, want exactly 1", calls)
	}
	if got, want := strings.Join(calls[0], " "), "run view --repo foo/bar --json jobs -- 1001"; got != want {
		t.Fatalf("job fetch args = %q, want %q", got, want)
	}
	if runs[1].Jobs != nil || runs[2].Jobs != nil {
		t.Fatalf("non-eligible runs carry jobs: %+v / %+v", runs[1].Jobs, runs[2].Jobs)
	}
}

func TestListRuns_AllSuccess_NoJobFetches(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(`[
  {"databaseId": 1, "name": "PR Checks", "status": "completed", "conclusion": "SUCCESS", "url": "u1", "headSha": "abc"},
  {"databaseId": 2, "name": "lint", "status": "completed", "conclusion": "SUCCESS", "url": "u2", "headSha": "abc"},
  {"databaseId": 3, "name": "docs", "status": "in_progress", "conclusion": "", "url": "u3", "headSha": "abc"},
  {"databaseId": 4, "name": "opt", "status": "completed", "conclusion": "SKIPPED", "url": "u4", "headSha": "abc"}
]`)
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})

	runs, err := p.ListRuns(context.Background(), "foo/bar#42")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if n := len(jobFetchCalls(gh)); n != 0 {
		t.Fatalf("job fetches = %d, want 0 (calls: %v)", n, gh.calls)
	}
	for _, r := range runs {
		if r.Jobs != nil {
			t.Errorf("run %s Jobs = %+v, want nil", r.ID, r.Jobs)
		}
	}
	// Only the single `run list` call happened.
	if len(gh.calls) != 1 {
		t.Fatalf("gh calls = %v, want only `run list`", gh.calls)
	}
}

func TestListRuns_JobFetchFailureLeavesJobsOmitted(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = readFixture(t, "run_list_pr_checks_failed.json")
	gh.errs["run view"] = errors.New("gh run view: HTTP 502")
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})

	runs, err := p.ListRuns(context.Background(), "foo/bar#42")
	if err != nil {
		t.Fatalf("ListRuns must not fail on a job-fetch error: %v", err)
	}
	if len(runs) != 3 || runs[0].Jobs != nil {
		t.Fatalf("runs = %+v, want 3 runs with run[0].Jobs omitted", runs)
	}
}

func TestListRuns_JobFetchesAreCapped(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 1; i <= maxJobFetchesPerList+5; i++ {
		if i > 1 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"databaseId": %d, "name": "w%d", "status": "completed", "conclusion": "FAILURE", "url": "u", "headSha": "abc"}`, i, i)
	}
	sb.WriteString("]")
	gh := newFakeGH()
	gh.responses["run list"] = []byte(sb.String())
	gh.responses["run view"] = readFixture(t, "run_view_jobs_failed.json")
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})

	if _, err := p.ListRuns(context.Background(), "foo/bar#42"); err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if n := len(jobFetchCalls(gh)); n != maxJobFetchesPerList {
		t.Fatalf("job fetches = %d, want cap %d", n, maxJobFetchesPerList)
	}
}

func TestListRuns_JobsAdditiveOnTheWire(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = readFixture(t, "run_list_pr_checks_failed.json")
	gh.responses["run view"] = readFixture(t, "run_view_jobs_failed.json")
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})

	runs, err := p.ListRuns(context.Background(), "foo/bar#42")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	raw, err := json.Marshal(runs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire []map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := wire[0]["jobs"]; !ok {
		t.Fatalf("failed run lacks jobs on the wire: %s", raw)
	}
	if _, ok := wire[1]["jobs"]; ok {
		t.Fatalf("successful run carries jobs on the wire: %s", raw)
	}
}
