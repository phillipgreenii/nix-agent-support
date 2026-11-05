package beadhandler

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/tmpldata"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.editor=true"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil && args[0] != "rebase" {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// stoppedRebase returns a real repo whose rebase stopped on a conflict.
func stoppedRebase(t *testing.T) string {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{Name: "rebasing"}).Dir
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("base\n")
	git(t, repo, "add", "f.txt")
	git(t, repo, "commit", "-m", "base")
	git(t, repo, "branch", "-M", "main")
	git(t, repo, "checkout", "-b", "topic")
	write("topic\n")
	git(t, repo, "commit", "-am", "topic")
	git(t, repo, "checkout", "main")
	write("main\n")
	git(t, repo, "commit", "-am", "main")
	git(t, repo, "checkout", "topic")
	git(t, repo, "rebase", "main") // conflicts and stops
	return repo
}

func metadataOf(t *testing.T, c []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, tok := range c {
		rec, err := csv.NewReader(strings.NewReader(tok)).Read()
		if err != nil || len(rec) != 1 {
			t.Fatalf("metadata token %q: %v", tok, err)
		}
		k, v, _ := strings.Cut(rec[0], "=")
		m[k] = v
	}
	return m
}

func TestFilingWritesTheLiveStateSnapshotFromARealStoppedRebase(t *testing.T) {
	s := newScene(t, "first-attempt")
	repo := stoppedRebase(t)
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	s.rep.Command.Cwd = repo
	rt := s.runtime()
	rt.Env = tmpldata.DefaultEnv()
	var out, errb bytes.Buffer
	if code := Main(rt, nil, &out, &errb); code != 3 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	m := metadataOf(t, only(t, s.fake.Calls(), "create").Values("metadata"))
	for _, k := range []string{metaHost, metaCwd, metaFiledAt, metaState, metaStateKind} {
		if m[k] == "" {
			t.Errorf("metadata %s missing: %v", k, m)
		}
	}
	if m[metaHost] != s.rep.Host || m[metaCwd] != repo || m[metaFiledAt] != filedAt.Format("2006-01-02T15:04:05Z") {
		t.Errorf("host/cwd/filed_at wrong: %v", m)
	}
	if m[metaStateKind] != KindCanonicalClone {
		t.Errorf("kind %q", m[metaStateKind])
	}
	var st State
	if err := json.Unmarshal([]byte(m[metaState]), &st); err != nil {
		t.Fatalf("state %q: %v", m[metaState], err)
	}
	if !st.Exists || st.Operation != "rebase" || st.Head == "" || st.StatusCount == 0 || st.StatusDigest == "" {
		t.Errorf("state %+v", st)
	}
}

func TestDedupHitAppendsAFreshStateBlock(t *testing.T) {
	s := newScene(t, "first-attempt")
	repo := stoppedRebase(t)
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	s.rep.Command.Cwd = repo
	s.fake.Respond("list", listOf(item("pg2-hit", "open", s.rep.Fingerprint)))
	rt := s.runtime()
	rt.Env = tmpldata.DefaultEnv()
	var out, errb bytes.Buffer
	if code := Main(rt, []string{"--dedup-query", "q"}, &out, &errb); code != 3 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	body := only(t, s.fake.Calls(), "comment").Value("body")
	want, _ := Snapshot(repo)
	if !strings.Contains(body, "State snapshot at 2026-10-02T14:30:00Z (kind canonical-clone)") || !strings.Contains(body, want.JSON()) {
		t.Errorf("comment lacks the fresh state block:\n%s", body)
	}
}

func TestSnapshotKinds(t *testing.T) {
	repo := stoppedRebase(t)
	if _, kind := Snapshot(repo); kind != KindCanonicalClone {
		t.Errorf("clone kind %q", kind)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "rebase", "--abort")
	git(t, repo, "worktree", "add", "-b", "other", wt)
	st, kind := Snapshot(wt)
	if kind != KindWorktree || st.Operation != "" || st.Branch != "other" {
		t.Errorf("worktree: %q %+v", kind, st)
	}
	if st, kind := Snapshot(t.TempDir()); kind != KindNotARepo || !st.Exists {
		t.Errorf("plain dir: %q %+v", kind, st)
	}
	if st, kind := Snapshot(filepath.Join(t.TempDir(), "gone")); kind != KindNotARepo || st.Exists {
		t.Errorf("missing dir: %q %+v", kind, st)
	}
}
