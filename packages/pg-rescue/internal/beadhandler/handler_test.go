package beadhandler

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/testenv"
	"github.com/phillipgreenii/pg-rescue/internal/tmpldata"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

const fixtures = "../../testdata/reports"

// seqRand yields bytes 1,2,3,... so the fence nonce is predictable.
type seqRand struct{ next byte }

func (s *seqRand) Read(p []byte) (int, error) {
	for i := range p {
		s.next++
		p[i] = s.next
	}
	return len(p), nil
}

var filedAt = time.Date(2026, 10, 2, 14, 30, 0, 0, time.UTC)

// scene is one handler run's world: a repo with a tracker, a report pointing
// at it, and a fake pg-connector on PATH.
type scene struct {
	t    *testing.T
	repo string // git toplevel (faked), named "the-repo"
	fake *testenv.FakeConnector
	rep  *report.Report
	env  map[string]string
	top  string // what the faked GitToplevel answers; "" means not a git repo
}

func newScene(t *testing.T, fixture string) *scene {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtures, fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep report.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "the-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if rep.Command != nil {
		rep.Command.Cwd = repo
	}
	s := &scene{t: t, repo: repo, top: repo, fake: testenv.NewFakeConnector(t), rep: &rep, env: map[string]string{}}
	s.fake.Install()
	return s
}

// write stores the (possibly edited) report and returns its path.
func (s *scene) write() string {
	s.t.Helper()
	b, err := json.Marshal(s.rep)
	if err != nil {
		s.t.Fatal(err)
	}
	p := filepath.Join(s.t.TempDir(), "report.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		s.t.Fatal(err)
	}
	return p
}

func (s *scene) runtime() *Runtime {
	s.t.Helper()
	s.env["PG_RESCUE_REPORT"] = s.write()
	s.env["PG_RESCUE_FINGERPRINT"] = s.rep.Fingerprint
	return &Runtime{
		Getenv:  func(k string) string { return s.env[k] },
		Environ: os.Environ,
		Now:     func() time.Time { return filedAt },
		Env: tmpldata.Env{
			Getwd: func() (string, error) { return s.repo, nil },
			GitToplevel: func(string) (string, error) {
				if s.top == "" {
					return "", os.ErrNotExist
				}
				return s.top, nil
			},
			Rand: &seqRand{},
		},
		Watch: func(func()) func() { return func() {} },
	}
}

func (s *scene) run(args ...string) (code int, stdout, stderr string) {
	s.t.Helper()
	var out, errb bytes.Buffer
	code = Main(s.runtime(), args, &out, &errb)
	return code, out.String(), errb.String()
}

// resultOf classifies stdout with the one contract implementation.
func resultOf(t *testing.T, code int, stdout string) (contract.Outcome, contract.Reported) {
	t.Helper()
	o, reason, rep := contract.Classify(code, []byte(stdout), false)
	if o == contract.Failed {
		t.Fatalf("contract.Classify rejected exit %d stdout %q: %s", code, stdout, reason)
	}
	return o, rep
}

func metaOf(t *testing.T, rep contract.Reported) map[string]string {
	t.Helper()
	m := map[string]string{}
	if err := json.Unmarshal(rep.Meta, &m); err != nil {
		t.Fatalf("meta %q: %v", rep.Meta, err)
	}
	return m
}

func only(t *testing.T, calls []testenv.Call, verb string) testenv.Call {
	t.Helper()
	var got []testenv.Call
	for _, c := range calls {
		if c.Verb() == verb {
			got = append(got, c)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one %q call, got %d: %v", verb, len(got), calls)
	}
	return got[0]
}

func TestCreateExits3WithMeta(t *testing.T) {
	s := newScene(t, "first-attempt")
	code, stdout, stderr := s.run()
	if code != 3 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	outcome, rep := resultOf(t, code, stdout)
	if outcome != contract.Deferred {
		t.Errorf("outcome %s", outcome)
	}
	if m := metaOf(t, rep); m["item_id"] != "pg2-new1" || m["action"] != "created" {
		t.Errorf("meta %v", m)
	}
	if rep.Summary != "Created pg2-new1" {
		t.Errorf("summary %q", rep.Summary)
	}
	if len(s.fake.Calls()) != 1 {
		t.Errorf("want one pg-connector call (no dedup query), got %v", s.fake.Calls())
	}
}

func TestEveryCallIsPinnedToTheBeadsBackendAsJSON(t *testing.T) {
	s := newScene(t, "first-attempt")
	if code, _, e := s.run("--dedup-query", "q"); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	calls := s.fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls %v", calls)
	}
	for _, c := range calls {
		if c.Args[0] != "issue" || c.Value("backend") != "pg-connector-issue-beads" || c.Value("output") != "json" {
			t.Errorf("call %v is not pinned to the beads backend with --output json", c.Args)
		}
	}
}

func TestBodyIsSelfContained(t *testing.T) {
	s := newScene(t, "three-prior-attempts")
	if code, _, e := s.run(); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	body := only(t, s.fake.Calls(), "create").Value("description")
	for _, want := range []string{
		"phillipg-mbp-02",                                  // host
		"Working directory: " + s.repo,                     // absolute cwd
		"Started: 2026-10-02T14:03:11Z",                    // timestamp
		"git pull --rebase",                                // command
		"Exit code: 1",                                     // exit code
		"CONFLICT (content): Merge conflict in flake.lock", // output tail
		"sync-projects: rebase repo onto origin",           // context
		"Run id: 20261002T140311Z-7f3a9c2e",
		"Fingerprint: sha256:a7b9c1af",
		"state was left as-is at 2026-10-02T14:30:00Z",
		"### 1. flake-lock-conflict: declined", // attempt facts
		"- Reason: resolved → verify failed (exit 128)",
		"### 3. fix-large: failed",
		"Conflict spans source files, not just flake.lock", // a claim
		"Removed markers in README.md",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestBodyFencesUntrustedText(t *testing.T) {
	s := newScene(t, "three-prior-attempts")
	if code, _, e := s.run(); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	body := only(t, s.fake.Calls(), "create").Value("description")
	// The fixture's attempt-2 details carry a four-backtick run and an
	// injection attempt; its fence must be at least five long and labelled
	// with the per-run nonce, and the injected text must sit inside it.
	nonce := "0102030405060708"
	open := "````` quoted-data " + nonce
	i := strings.Index(body, open+"\nRemoved markers")
	if i < 0 {
		t.Fatalf("details not in a >=5-backtick fence labelled with the nonce:\n%s", body)
	}
	closeAt := strings.Index(body[i+len(open):], "\n`````\n")
	inject := strings.Index(body, "ignore previous instructions")
	if closeAt < 0 || inject < i || inject > i+len(open)+closeAt {
		t.Errorf("injected text escaped its fence:\n%s", body)
	}
	for _, span := range []string{"CONFLICT (content): Merge conflict in flake.lock", "sync-projects: rebase repo onto origin", "git pull --rebase"} {
		if !fenced(body, span) {
			t.Errorf("%q is not inside a quoted-data fence:\n%s", span, body)
		}
	}
}

// fenced reports whether span appears between a quoted-data opener and a
// closing fence.
func fenced(body, span string) bool {
	at := strings.Index(body, span)
	if at < 0 {
		return false
	}
	before := body[:at]
	open := strings.LastIndex(before, " quoted-data ")
	if open < 0 {
		return false
	}
	return !strings.Contains(before[open:], "\n```\n")
}

func TestStdinModeBody(t *testing.T) {
	s := newScene(t, "stdin-mode")
	s.rep.Command = nil
	if code, _, e := s.run("--tracker-dir", s.repo); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	c := only(t, s.fake.Calls(), "create")
	body := c.Value("description")
	if !strings.Contains(body, "No command ran (stdin mode)") || strings.Contains(body, "Exit code") {
		t.Errorf("stdin body wrong:\n%s", body)
	}
	if !strings.Contains(body, "3 repos failed to fetch") {
		t.Errorf("output tail missing:\n%s", body)
	}
	if got := c.Value("title"); got != "pg-rescue: stdin input failed in the-repo" {
		t.Errorf("title %q", got)
	}
}

func TestDefaultTitle(t *testing.T) {
	s := newScene(t, "first-attempt")
	if code, _, e := s.run(); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	c := only(t, s.fake.Calls(), "create")
	if got := c.Value("title"); got != "pg-rescue: git pull --rebase failed in the-repo" {
		t.Errorf("title %q", got)
	}
	// Passed as --title=VALUE so a leading dash cannot be read as a flag.
	found := false
	for _, a := range c.Args {
		found = found || strings.HasPrefix(a, "--title=")
	}
	if !found {
		t.Errorf("no --title=VALUE argument in %v", c.Args)
	}
}

func TestTitleNewlinesStripped(t *testing.T) {
	s := newScene(t, "first-attempt")
	if code, _, e := s.run("--title-template", "line one\nline two\r\nline three {{.Repo}}\n"); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if got := only(t, s.fake.Calls(), "create").Value("title"); got != "line one line two line three the-repo" {
		t.Errorf("title %q", got)
	}
}

func TestEmptyTitleIsAnError(t *testing.T) {
	s := newScene(t, "first-attempt")
	code, _, stderr := s.run("--title-template", "\n")
	if code != 1 || !strings.Contains(stderr, "empty title") || len(s.fake.Calls()) != 0 {
		t.Errorf("code=%d stderr=%q calls=%v", code, stderr, s.fake.Calls())
	}
}

func TestTemplateFilesAndAppendedInstructions(t *testing.T) {
	s := newScene(t, "first-attempt")
	dir := t.TempDir()
	tf := filepath.Join(dir, "title.tmpl")
	bf := filepath.Join(dir, "body.tmpl")
	af := filepath.Join(dir, "append.txt")
	for p, c := range map[string]string{tf: "T {{.RunID}}", bf: "B {{.Repo}}: {{.Quote .Context}}", af: "Read the context first."} {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, e := s.run("--title-template-file", tf, "--body-template-file", bf, "--append-instructions-file", af); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	c := only(t, s.fake.Calls(), "create")
	if c.Value("title") != "T 20261002T140311Z-7f3a9c2e" {
		t.Errorf("title %q", c.Value("title"))
	}
	body := c.Value("description")
	if !strings.HasPrefix(body, "B the-repo: ``` quoted-data ") || !strings.HasSuffix(body, "\n\nRead the context first.") {
		t.Errorf("body %q", body)
	}
}

func TestInlineAppendInstructions(t *testing.T) {
	s := newScene(t, "first-attempt")
	if code, _, e := s.run("--append-instructions", "Please triage."); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if body := only(t, s.fake.Calls(), "create").Value("description"); !strings.HasSuffix(body, "\n\nPlease triage.") {
		t.Errorf("body does not end with the instructions:\n%s", body)
	}
}

func TestOutputTailLines(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.rep.OutputTail = "one\ntwo\nthree\nfour\n"
	if code, _, e := s.run("--output-tail-lines", "2"); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	body := only(t, s.fake.Calls(), "create").Value("description")
	if strings.Contains(body, "one") || strings.Contains(body, "two") || !strings.Contains(body, "three\nfour\n") {
		t.Errorf("tail not trimmed to the last two lines:\n%s", body)
	}
}

func TestOversizedBodyShrinksTheTailToFit(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.rep.OutputTail = strings.Repeat(strings.Repeat("x", 99)+"\n", 900) // ~90 KB
	if code, _, e := s.run("--output-tail-lines", "1000"); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	body := only(t, s.fake.Calls(), "create").Value("description")
	if len(body) > maxBodyBytes || len(body) < 1000 {
		t.Errorf("body is %d bytes; want within (1000, %d]", len(body), maxBodyBytes)
	}
	if !strings.Contains(body, "state was left as-is at") || strings.Contains(body, "truncated") {
		t.Errorf("shrinking the tail should have been enough:\n%.300s", body)
	}
}

func TestBodyThatCannotShrinkIsClamped(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.rep.Context = strings.Repeat("é", 40000) // 80 KB of two-byte runes, not in the tail
	if code, _, e := s.run(); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	body := only(t, s.fake.Calls(), "create").Value("description")
	if len(body) > maxBodyBytes || !strings.HasSuffix(body, truncatedMarker) {
		t.Errorf("body %d bytes, ends %q", len(body), body[max(0, len(body)-80):])
	}
	if !strings.ContainsRune(body, 'é') || strings.ContainsRune(body, '\uFFFD') {
		t.Errorf("clamp split a rune")
	}
}

func TestLabels(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"no map and no flag: no repo label", nil, []string{"pg-rescue"}},
		{"map entry", []string{"--repo-label-map", "other=o", "--repo-label-map", "the-repo=tr"}, []string{"pg-rescue", "tr"}},
		{"map without an entry for this repo", []string{"--repo-label-map", "other=o"}, []string{"pg-rescue"}},
		{"--repo-label overrides the map", []string{"--repo-label-map", "the-repo=tr", "--repo-label", "forced"}, []string{"pg-rescue", "forced"}},
		{"--label values come last, deduplicated", []string{"--repo-label", "r", "--label", "a", "--label", "pg-rescue", "--label", "r", "--label", "b"}, []string{"pg-rescue", "r", "a", "b"}},
		{"--repo-label alone", []string{"--repo-label", "agent-support"}, []string{"pg-rescue", "agent-support"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScene(t, "first-attempt")
			if code, _, e := s.run(tc.args...); code != 3 {
				t.Fatalf("exit %d: %s", code, e)
			}
			got := only(t, s.fake.Calls(), "create").Values("labels")
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("labels %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNoRepoLabelWhenNotInAGitRepo(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.top = ""
	if code, _, e := s.run("--tracker-dir", s.repo, "--repo-label-map", "the-repo=tr"); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if got := only(t, s.fake.Calls(), "create").Values("labels"); strings.Join(got, "|") != "pg-rescue" {
		t.Errorf("labels %v: the map must not match without a toplevel", got)
	}
}

func TestCreateFlags(t *testing.T) {
	s := newScene(t, "first-attempt")
	if code, _, e := s.run("--priority", "1", "--issue-type", "bug"); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	c := only(t, s.fake.Calls(), "create")
	if c.Value("priority") != "P1" || c.Value("issue-type") != "bug" {
		t.Errorf("args %v", c.Args)
	}
	if got := c.Values("metadata"); len(got) != 1 || got[0] != "pg_rescue_fingerprint="+s.rep.Fingerprint {
		t.Errorf("metadata %v", got)
	}
}

func TestDefaultsAreP2Task(t *testing.T) {
	s := newScene(t, "first-attempt")
	if code, _, e := s.run(); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	c := only(t, s.fake.Calls(), "create")
	if c.Value("priority") != "P2" || c.Value("issue-type") != "task" {
		t.Errorf("args %v", c.Args)
	}
}

func TestChildEnvironment(t *testing.T) {
	t.Setenv("PG_CONNECTOR_ISSUE_BEADS_DIR", "/wrong/tracker")
	t.Setenv("PG_CONNECTOR_ISSUE_BEADS_ACTOR", "someone-else")
	s := newScene(t, "first-attempt")
	if code, _, e := s.run("--dedup-query", "q"); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	for _, c := range s.fake.Calls() {
		if c.TrackerDir != s.repo {
			t.Errorf("%s: PG_CONNECTOR_ISSUE_BEADS_DIR=%q, want %q", c.Verb(), c.TrackerDir, s.repo)
		}
		if c.Actor != "pg-rescue/20261002T140311Z-7f3a9c2e" {
			t.Errorf("%s: actor %q", c.Verb(), c.Actor)
		}
	}
}

func TestTrackerDirOverridesTheDerivedTracker(t *testing.T) {
	s := newScene(t, "first-attempt")
	elsewhere := t.TempDir()
	if code, _, e := s.run("--tracker-dir", elsewhere); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if got := only(t, s.fake.Calls(), "create").TrackerDir; got != elsewhere {
		t.Errorf("tracker %q, want %q", got, elsewhere)
	}
}

func TestNoBeadsDirAndNoTrackerDirExits1(t *testing.T) {
	s := newScene(t, "first-attempt")
	if err := os.RemoveAll(filepath.Join(s.repo, ".beads")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := s.run()
	if code != 1 || stdout != "" || !strings.Contains(stderr, "--tracker-dir") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if len(s.fake.Calls()) != 0 {
		t.Errorf("nothing may be written without a tracker: %v", s.fake.Calls())
	}
}

func TestNotAGitRepoWithoutTrackerDirExits1(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.top = ""
	if code, _, stderr := s.run(); code != 1 || !strings.Contains(stderr, "no beads tracker") || len(s.fake.Calls()) != 0 {
		t.Errorf("code=%d stderr=%q calls=%v", code, stderr, s.fake.Calls())
	}
}

func TestMissingTrackerDirExits1(t *testing.T) {
	s := newScene(t, "first-attempt")
	code, _, stderr := s.run("--tracker-dir", filepath.Join(s.repo, "nope"))
	if code != 1 || !strings.Contains(stderr, "not a directory") || len(s.fake.Calls()) != 0 {
		t.Errorf("code=%d stderr=%q", code, stderr)
	}
}

func TestRealGitToplevelDerivation(t *testing.T) {
	s := newScene(t, "first-attempt")
	repo := gittest.New(t, gitfixture.RepoOptions{Name: "real-repo"}).Dir
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	s.rep.Command.Cwd = sub
	rt := s.runtime()
	rt.Env = tmpldata.DefaultEnv()
	var out, errb bytes.Buffer
	if code := Main(rt, []string{"--repo-label-map", filepath.Base(repo) + "=mapped"}, &out, &errb); code != 3 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	c := only(t, s.fake.Calls(), "create")
	want, _ := filepath.EvalSymlinks(repo)
	if got, _ := filepath.EvalSymlinks(c.TrackerDir); got != want {
		t.Errorf("tracker %q, want the git toplevel %q", c.TrackerDir, want)
	}
	if got := c.Values("labels"); strings.Join(got, "|") != "pg-rescue|mapped" {
		t.Errorf("labels %v", got)
	}
}

func listOf(items ...string) string { return `{"entities":[` + strings.Join(items, ",") + `]}` }

func item(id, state, fingerprint string) string {
	return `{"id":"` + id + `","state":"` + state + `","metadata":{"pg_rescue_fingerprint":"` + fingerprint + `"}}`
}

func TestDedupMatchCommentsAndReportsUpdated(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.fake.Respond("list", listOf(item("pg2-other", "open", "sha256:other"), item("pg2-hit", "in_progress", s.rep.Fingerprint)))
	code, stdout, stderr := s.run("--dedup-query", "pg-rescue-open")
	if code != 3 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	_, rep := resultOf(t, code, stdout)
	if m := metaOf(t, rep); m["item_id"] != "pg2-hit" || m["action"] != "updated" || rep.Summary != "Updated pg2-hit" {
		t.Errorf("meta %v summary %q", m, rep.Summary)
	}
	calls := s.fake.Calls()
	if len(calls) != 2 || calls[0].Verb() != "list" || calls[1].Verb() != "comment" {
		t.Fatalf("calls %v", calls)
	}
	if calls[0].Value("query") != "pg-rescue-open" {
		t.Errorf("query %q", calls[0].Value("query"))
	}
	if calls[1].Args[2] != "pg2-hit" || !strings.Contains(calls[1].Value("body"), "state was left as-is at") {
		t.Errorf("comment %v", calls[1].Args)
	}
}

func TestDedupClosedMatchIsIgnored(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.fake.Respond("list", listOf(item("pg2-old", "closed", s.rep.Fingerprint), item("pg2-old2", "Closed", s.rep.Fingerprint)))
	code, stdout, stderr := s.run("--dedup-query", "q")
	if code != 3 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	_, rep := resultOf(t, code, stdout)
	if m := metaOf(t, rep); m["action"] != "created" || m["item_id"] != "pg2-new1" {
		t.Errorf("meta %v", m)
	}
	only(t, s.fake.Calls(), "create")
}

func TestDedupNoMatchCreates(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.fake.Respond("list", listOf(item("pg2-x", "open", "sha256:different"), `{"id":"pg2-bare","state":"open"}`))
	if code, _, e := s.run("--dedup-query", "q"); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	calls := s.fake.Calls()
	if len(calls) != 2 || calls[1].Verb() != "create" {
		t.Errorf("calls %v", calls)
	}
}

func TestNoDedupQueryMeansNoListAndNoDedup(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.fake.Respond("list", listOf(item("pg2-hit", "open", s.rep.Fingerprint)))
	code, stdout, e := s.run()
	if code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	_, rep := resultOf(t, code, stdout)
	if metaOf(t, rep)["action"] != "created" {
		t.Errorf("want a new item despite an existing match")
	}
	for _, c := range s.fake.Calls() {
		if c.Verb() == "list" {
			t.Errorf("list was called without --dedup-query")
		}
	}
}

func TestDedupFingerprintComesFromTheEnvironment(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.fake.Respond("list", listOf(item("pg2-env", "open", "sha256:from-env")))
	rt := s.runtime()
	s.env["PG_RESCUE_FINGERPRINT"] = "sha256:from-env"
	var out, errb bytes.Buffer
	if code := Main(rt, []string{"--dedup-query", "q"}, &out, &errb); code != 3 || !strings.Contains(out.String(), "Updated pg2-env") {
		t.Errorf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
}

func TestDedupListFailureExits1WithoutCreating(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.fake.Fail("list", 1, "issue-beads: bd workspace not configured")
	code, stdout, stderr := s.run("--dedup-query", "q")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "bd workspace not configured") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if len(s.fake.Calls()) != 1 {
		t.Errorf("must not fall through to create: %v", s.fake.Calls())
	}
}

func TestAnnotateCommentsAndLabels(t *testing.T) {
	s := newScene(t, "first-attempt")
	code, stdout, stderr := s.run("--annotate", "pg2-abc", "--add-label", "needs-triage", "--add-label", "pg-rescue-seen")
	if code != 3 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	_, rep := resultOf(t, code, stdout)
	if m := metaOf(t, rep); m["item_id"] != "pg2-abc" || m["action"] != "annotated" || rep.Summary != "Annotated pg2-abc" {
		t.Errorf("meta %v summary %q", m, rep.Summary)
	}
	calls := s.fake.Calls()
	if len(calls) != 2 || calls[0].Verb() != "comment" || calls[1].Verb() != "update" {
		t.Fatalf("calls %v", calls)
	}
	if calls[0].Args[2] != "pg2-abc" || !strings.Contains(calls[0].Value("body"), "Run id: 20261002T140311Z-7f3a9c2e") {
		t.Errorf("comment %v", calls[0].Args)
	}
	if calls[1].Args[2] != "pg2-abc" || strings.Join(calls[1].Values("add-label"), "|") != "needs-triage|pg-rescue-seen" {
		t.Errorf("update %v", calls[1].Args)
	}
	for _, c := range calls {
		if c.TrackerDir != s.repo {
			t.Errorf("tracker %q", c.TrackerDir)
		}
	}
}

func TestAnnotateWithoutLabelsOnlyComments(t *testing.T) {
	s := newScene(t, "first-attempt")
	if code, _, e := s.run("--annotate", "pg2-abc"); code != 3 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if calls := s.fake.Calls(); len(calls) != 1 || calls[0].Verb() != "comment" {
		t.Errorf("calls %v", calls)
	}
}

func TestAnnotateFailuresExit1(t *testing.T) {
	for _, failing := range []string{"comment", "update"} {
		t.Run(failing, func(t *testing.T) {
			s := newScene(t, "first-attempt")
			s.fake.Fail(failing, 1, "boom")
			code, stdout, stderr := s.run("--annotate", "pg2-abc", "--add-label", "x")
			if code != 1 || stdout != "" || !strings.Contains(stderr, "boom") {
				t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}

func TestUnreachableTrackerExits1WithNoFallback(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.fake.Fail("create", 1, "tracker unreachable")
	code, stdout, stderr := s.run()
	if code != 1 || stdout != "" || !strings.Contains(stderr, "tracker unreachable") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if calls := s.fake.Calls(); len(calls) != 1 {
		t.Errorf("no second attempt or fallback is allowed: %v", calls)
	}
}

func TestConnectorNotOnPathExits1(t *testing.T) {
	s := newScene(t, "first-attempt")
	t.Setenv("PATH", t.TempDir())
	if code, _, stderr := s.run(); code != 1 || !strings.Contains(stderr, "pg-connector") {
		t.Errorf("code=%d stderr=%q", code, stderr)
	}
}

func TestMalformedCreateResponseExits1(t *testing.T) {
	for name, resp := range map[string]string{"not json": "oops", "no id": `{"result":{}}`} {
		t.Run(name, func(t *testing.T) {
			s := newScene(t, "first-attempt")
			s.fake.Respond("create", resp)
			if code, stdout, _ := s.run(); code != 1 || stdout != "" {
				t.Errorf("code=%d stdout=%q", code, stdout)
			}
		})
	}
}

func TestReportProblemsExit1(t *testing.T) {
	t.Run("unknown schema_version", func(t *testing.T) {
		s := newScene(t, "first-attempt")
		s.rep.SchemaVersion = 2
		if code, _, stderr := s.run(); code != 1 || !strings.Contains(stderr, "schema_version 2") || len(s.fake.Calls()) != 0 {
			t.Errorf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("no report", func(t *testing.T) {
		s := newScene(t, "first-attempt")
		rt := s.runtime()
		rt.Getenv = func(string) string { return "" }
		var out, errb bytes.Buffer
		if code := Main(rt, nil, &out, &errb); code != 1 || !strings.Contains(errb.String(), "PG_RESCUE_REPORT") {
			t.Errorf("code=%d stderr=%q", code, errb.String())
		}
	})
	t.Run("unreadable report", func(t *testing.T) {
		s := newScene(t, "first-attempt")
		rt := s.runtime()
		rt.Getenv = func(string) string { return filepath.Join(t.TempDir(), "missing.json") }
		var out, errb bytes.Buffer
		if code := Main(rt, nil, &out, &errb); code != 1 {
			t.Errorf("code=%d", code)
		}
	})
}

func TestUsageErrorsExit1NotTheDeclinedCode(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown flag":             {"--nope"},
		"positional":               {"extra"},
		"bad priority":             {"--priority", "9"},
		"negative tail":            {"--output-tail-lines", "-1"},
		"bad map entry":            {"--repo-label-map", "norepo"},
		"empty map label":          {"--repo-label-map", "repo="},
		"add-label alone":          {"--add-label", "x"},
		"annotate with dedup":      {"--annotate", "pg2-x", "--dedup-query", "q"},
		"title template and file":  {"--title-template", "t", "--title-template-file", "f"},
		"body template and file":   {"--body-template", "t", "--body-template-file", "f"},
		"append text and file":     {"--append-instructions", "t", "--append-instructions-file", "f"},
		"template file is missing": {"--body-template-file", "/nonexistent/body.tmpl"},
		"unknown template field":   {"--body-template", "{{.Nope}}"},
		"template syntax error":    {"--title-template", "{{"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newScene(t, "first-attempt")
			code, stdout, stderr := s.run(args...)
			if code != 1 || stdout != "" || stderr == "" || len(s.fake.Calls()) != 0 {
				t.Errorf("code=%d stdout=%q stderr=%q calls=%v", code, stdout, stderr, s.fake.Calls())
			}
		})
	}
}

func TestHelp(t *testing.T) {
	s := newScene(t, "first-attempt")
	if code, stdout, _ := s.run("--help"); code != 0 || !strings.HasPrefix(stdout, "usage: pg-rescue-bead") {
		t.Errorf("code=%d stdout=%q", code, stdout)
	}
}

func TestPrintFlagsNeedNoReport(t *testing.T) {
	rt := &Runtime{Getenv: func(string) string { return "" }}
	var out, errb bytes.Buffer
	if code := Main(rt, []string{"--print-default-template"}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	for _, want := range []string{"-- title --\n" + DefaultTitleTemplate, "-- body --\n", "state was left as-is at {{.FiledAt}}"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("default templates lack %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := Main(rt, []string{"--print-template-vars"}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	for _, want := range []string{".RunID", ".OutputTail", ".Quote", ".FiledAt"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("template vars lack %q:\n%s", want, out.String())
		}
	}
}

func TestDefaultBodyTemplateOnlyUsesKnownFields(t *testing.T) {
	// A template that renders every field of both data types is what
	// --print-template-vars promises; rendering the default with all its
	// branches (stdin, no context, no attempts, no chain) must not error.
	for _, fixture := range []string{"first-attempt", "three-prior-attempts", "stdin-mode"} {
		s := newScene(t, fixture)
		s.rep.Context = ""
		if fixture == "stdin-mode" {
			s.rep.Command = nil
		}
		if code, _, e := s.run("--tracker-dir", s.repo); code != 3 {
			t.Errorf("%s: exit %d: %s", fixture, code, e)
		}
	}
}

func TestLastLines(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"", 3, ""},
		{"a\nb\nc\n", 0, ""},
		{"a\nb\nc\n", 1, "c\n"},
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc\n", 3, "a\nb\nc\n"},
		{"a\nb\nc\n", 9, "a\nb\nc\n"},
		{"a\nb\nc", 2, "b\nc"},
		{"only", 1, "only"},
		{"\n\nx\n", 2, "\nx\n"},
	} {
		if got := lastLines(tc.in, tc.n); got != tc.want {
			t.Errorf("lastLines(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
	for in, want := range map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "a\nb\n": 2, "\n": 1} {
		if got := lineCount(in); got != want {
			t.Errorf("lineCount(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCSVPairKeepsCommasIntact(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"k", "v"}:         "k=v",
		{"k", "sha256:ab"}: "k=sha256:ab",
		{"k", "a,b=c"}:     `"k=a,b=c"`,
		{"k", `q"q`}:       `"k=q""q"`,
	} {
		if got := csvPair(in[0], in[1]); got != want {
			t.Errorf("csvPair(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestOrphanWatcherKillsTheRunningChild(t *testing.T) {
	s := newScene(t, "first-attempt")
	s.fake.Block("create")
	rt := s.runtime()
	cleanups := make(chan func(), 1)
	var stopped atomic.Bool
	rt.Watch = func(c func()) func() {
		cleanups <- c
		return func() { stopped.Store(true) }
	}
	done := make(chan int, 1)
	go func() {
		var out, errb bytes.Buffer
		done <- Main(rt, nil, &out, &errb)
	}()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(s.fake.Dir, "started")); return err == nil })
	(<-cleanups)() // what the real watcher runs before exiting 1
	select {
	case code := <-done:
		if code != 1 {
			t.Errorf("exit %d after the child was killed, want 1", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Main did not return after the orphan cleanup killed its child")
	}
	if !stopped.Load() {
		t.Error("the orphan watch was not stopped on the way out")
	}
	pid := strings.TrimSpace(readFile(t, filepath.Join(s.fake.Dir, "started.child")))
	if err := exec.Command("kill", "-0", pid).Run(); err == nil {
		t.Errorf("the fake pg-connector (pid %s) is still alive", pid)
	}
}

func TestOrphanCleanupBeforeTheChildStartsKillsItOnArrival(t *testing.T) {
	c := &child{}
	c.kill()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = sysProcGroup()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c.set(cmd)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("child exited cleanly; want it killed")
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("a child registered after kill() was not killed")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
