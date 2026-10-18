package servetest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Shim is the counting fake gh of one daemon: a copy of
// internal/serve/testdata/fake-gh.sh in its own directory, plus the fixture and
// control files the script reads (see the script's header) and the call log it
// writes.
type Shim struct {
	// Dir holds the script (as "gh"), the fixtures and calls.log; Path is the
	// script the daemon's gh_path names.
	Dir  string
	Path string

	t testing.TB
}

func newShim(t testing.TB, dir, token string) *Shim {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(shimSource(t))
	if err != nil {
		t.Fatalf("servetest: read the fake gh: %v", err)
	}
	s := &Shim{Dir: dir, Path: filepath.Join(dir, "gh"), t: t}
	if err := os.WriteFile(s.Path, withResolvedBash(src), 0o700); err != nil {
		t.Fatal(err)
	}
	s.Set("token", token)
	// Empty conversation, files and commits pages for every PR nobody gave a
	// fixture of its own (the group fetchers are always running).
	empty := func(field string) string {
		return repoPRDoc(t, map[string]any{field: page(0, []map[string]any{})})
	}
	s.Set("threads.json", empty("reviewThreads"))
	s.Set("issue_comments.json", empty("comments"))
	s.Set("reviews.json", empty("reviews"))
	s.Set("files.json", empty("files"))
	s.Set("commits.json", empty("commits"))
	return s
}

// Set writes a control or fixture file (see the script's header for the names).
func (s *Shim) Set(name, content string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.Dir, name), []byte(content), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// Mode sets how GraphQL calls answer: "ok", "502", "secondary", "primary",
// "401" or "notoken".
func (s *Shim) Mode(mode string) { s.Set("mode", mode) }

// FailNext makes the next n GraphQL calls answer 502, then the mode applies.
func (s *Shim) FailNext(n int) { s.Set("failn", fmt.Sprint(n)) }

// Remaining sets the rateLimit remaining every answer reports.
func (s *Shim) Remaining(n int) { s.Set("remaining", fmt.Sprint(n)) }

// Delay makes every GraphQL call sleep this many seconds before answering.
func (s *Shim) Delay(seconds int) { s.Set("delay", fmt.Sprint(seconds)) }

// Viewer sets the login the viewer query answers.
func (s *Shim) Viewer(login string) { s.Set("viewer.login", login) }

// PR is one fixture pull request, generic by construction.
type PR struct {
	// NodeID is the GraphQL node id; Repo is owner/name; Number the PR number.
	NodeID string
	Repo   string
	Number int
	Title  string
	// State defaults to OPEN; UpdatedAt to a fixed time.
	State     string
	UpdatedAt string
	// Head is the head commit sha (default "head-<number>") and Branch the head
	// branch name (default "feature-<number>").
	Head   string
	Branch string
}

// Ref is the wire reference owner/repo#n.
func (p PR) Ref() string { return fmt.Sprintf("%s#%d", p.Repo, p.Number) }

func (p PR) node() string {
	state := p.State
	if state == "" {
		state = "OPEN"
	}
	updated := p.UpdatedAt
	if updated == "" {
		updated = "2026-10-12T08:00:00Z"
	}
	title := p.Title
	if title == "" {
		title = "Example change " + fmt.Sprint(p.Number)
	}
	head, branch := p.Head, p.Branch
	if head == "" {
		head = fmt.Sprintf("head-%d", p.Number)
	}
	if branch == "" {
		branch = fmt.Sprintf("feature-%d", p.Number)
	}
	return fmt.Sprintf(`{"id":%q,"number":%d,"title":%q,"url":"https://%s/%s/pull/%d","state":%q,"body":"example body",`+
		`"isDraft":false,"updatedAt":%q,"reviewDecision":"REVIEW_REQUIRED","mergeable":"MERGEABLE",`+
		`"author":{"login":"fixture-author"},"repository":{"nameWithOwner":%q},`+
		`"labels":{"totalCount":1,"nodes":[{"name":"example"}]},"comments":{"totalCount":0},"reviews":{"totalCount":0},`+
		`"reviewThreads":{"totalCount":0},"headRefOid":%q,"headRefName":%q,"baseRefName":"main","baseRefOid":"base-1",`+
		`"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}}`,
		p.NodeID, p.Number, title, Host, p.Repo, p.Number, state, updated, p.Repo, head, branch)
}

// SetPRs makes the search for a configured query's expression (for example
// MineExpr) answer exactly prs, and makes each PR's node answerable. An empty
// expr sets the default answer every other search gets.
func (s *Shim) SetPRs(expr string, prs ...PR) {
	s.t.Helper()
	var members []string
	for _, p := range prs {
		members = append(members, fmt.Sprintf(`{"id":%q,"number":%d,"repository":{"nameWithOwner":%q}}`, p.NodeID, p.Number, p.Repo))
		s.Set("node."+p.NodeID+".json", p.node())
		if _, err := os.Stat(filepath.Join(s.Dir, "detail."+p.NodeID+".json")); err != nil {
			s.SetDetail(p, Detail{})
		}
	}
	body := fmt.Sprintf(`{"data":{"rateLimit":{"cost":1,"remaining":@REMAINING@,"resetAt":"2999-01-01T00:00:00Z"},`+
		`"search":{"issueCount":%d,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[%s]}}}`,
		len(prs), strings.Join(members, ","))
	if expr == "" {
		s.Set("search.json", body)
		return
	}
	s.Set("search."+sanitizeQuery("is:pr "+expr)+".json", body)
}

// sanitizeQuery is the shim's file-name key of a search string: every character
// outside [A-Za-z0-9] becomes an underscore.
func sanitizeQuery(q string) string {
	var b strings.Builder
	for _, r := range q {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Call is one recorded gh invocation.
type Call struct {
	// Host is the GH_HOST the call carried.
	Host string
	// Args are the argv after the program name.
	Args []string
}

// Kind classifies the call as the fake gh does: "auth_token", "viewer",
// "search" (the membership search), "nodes" (the summary batch), "detail" (the
// detail batch), "threads", "issue_comments", "reviews", "files", "commits",
// "pending" (the pending review lookup), "runs", "jobs", "rerun" (the REST
// runs list, a run's jobs and the rerun request), "create_review" (the REST
// create of a pending review), "search_prs" (`gh search prs`), "run_view"
// (`gh run view`) or "other". A GraphQL write sends its document on standard
// input, so its argv says nothing: Shim.Stdin and Shim.CountStdin classify it.
func (c Call) Kind() string {
	if len(c.Args) >= 2 && c.Args[0] == "auth" && c.Args[1] == "token" {
		return "auth_token"
	}
	if len(c.Args) >= 2 && c.Args[0] == "search" && c.Args[1] == "prs" {
		return "search_prs"
	}
	if len(c.Args) >= 2 && c.Args[0] == "run" && c.Args[1] == "view" {
		return "run_view"
	}
	all := strings.Join(c.Args, " ")
	if !strings.Contains(all, "graphql") {
		switch {
		case strings.Contains(all, "/rerun-failed-jobs"):
			return "rerun"
		case strings.Contains(all, "/attempts/") && strings.Contains(all, "/jobs"):
			return "jobs"
		case strings.Contains(all, "/actions/runs?"):
			return "runs"
		case strings.Contains(all, "/pulls/") && strings.Contains(all, "/reviews"):
			return "create_review"
		}
		return "other"
	}
	switch {
	case strings.Contains(all, "pending: reviews("):
		return "pending"
	case strings.Contains(all, "mergeStateStatus"):
		return "detail"
	case strings.Contains(all, "viewer { login }"):
		return "viewer"
	case strings.Contains(all, "search("):
		return "search"
	case strings.Contains(all, "nodes(ids"):
		return "nodes"
	case strings.Contains(all, "reviewThreads(first: 100, after"):
		return "threads"
	case strings.Contains(all, "reviews(first: 100, after: $after) {"):
		return "reviews"
	case strings.Contains(all, "comments(first: 100, after: $after) {"):
		return "issue_comments"
	case strings.Contains(all, "files(first: 100"):
		return "files"
	case strings.Contains(all, "commits(first: 100"):
		return "commits"
	}
	return "other"
}

// Calls returns every recorded call, oldest first.
func (s *Shim) Calls() []Call {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	var out []Call
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		c := Call{Host: strings.TrimPrefix(fields[0], "GH_HOST=")}
		c.Args = append(c.Args, fields[1:]...)
		out = append(out, c)
	}
	return out
}

// OriginCalls returns the calls that reached GitHub: every one but `gh auth
// token`.
func (s *Shim) OriginCalls() []Call {
	var out []Call
	for _, c := range s.Calls() {
		if c.Kind() != "auth_token" {
			out = append(out, c)
		}
	}
	return out
}

// CountKind counts the recorded calls of one kind.
func (s *Shim) CountKind(kind string) int {
	n := 0
	for _, c := range s.Calls() {
		if c.Kind() == kind {
			n++
		}
	}
	return n
}

// WaitQuiet waits until the shim has recorded no new call for quiet (the daemon
// has caught up with whatever work it started), and returns how many calls it
// has recorded; it fails the test if that does not happen within max. Without
// kinds every origin call counts; with kinds only the calls of those kinds do
// (a test whose daemon polls the viewer on a short interval waits on "search"
// and "nodes").
func (s *Shim) WaitQuiet(t testing.TB, quiet, max time.Duration, kinds ...string) int {
	t.Helper()
	count := func() int {
		if len(kinds) == 0 {
			return len(s.OriginCalls())
		}
		n := 0
		for _, k := range kinds {
			n += s.CountKind(k)
		}
		return n
	}
	deadline := time.Now().Add(max)
	last := count()
	since := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		n := count()
		if n != last {
			last, since = n, time.Now()
			continue
		}
		if time.Since(since) >= quiet {
			return n
		}
	}
	t.Fatalf("servetest: the daemon kept calling gh for %s (%d calls)", max, last)
	return last
}

// withResolvedBash rewrites the script's "#!/bin/bash" line to the bash on
// PATH. The nix build sandbox has no /bin/bash (only /bin/sh), so the literal
// shebang makes every exec of the fake gh fail with "no such file or
// directory"; the module's check derivation puts pkgs.bash on PATH. When no
// bash is on PATH the script is left unchanged.
func withResolvedBash(src []byte) []byte {
	const shebang = "#!/bin/bash\n"
	bash, err := exec.LookPath("bash")
	if err != nil || !strings.HasPrefix(string(src), shebang) {
		return src
	}
	return append([]byte("#!"+bash+"\n"), src[len(shebang):]...)
}
