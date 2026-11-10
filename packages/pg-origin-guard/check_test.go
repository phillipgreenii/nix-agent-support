package originguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type checkRig struct {
	chk   *Checker
	sink  *captureSink
	clock *fakeClock
	agent fakeAgent
	dir   string
}

func newCheckRig(t *testing.T) *checkRig {
	t.Helper()
	env := testEnv(t)
	cfg := mustParse(t, `
[realm.zr]
match = { host = "github.com", user = "org-*" }
`, env)
	clock := newClock()
	sink := &captureSink{}
	dir := filepath.Join(t.TempDir(), "state")
	store := NewStore(dir, clock, cfg.MaxBackoff())
	agent := fakeAgent{"/env.sock": {{ValidAfter: clock.Now().Add(-time.Hour), ValidBefore: clock.Now().Add(time.Hour)}}}
	chk := &Checker{
		Config: cfg, Clock: clock, Store: store,
		Certs: &CertChecker{
			Clock: clock, Dialer: agent,
			Getenv: func(k string) string {
				if k == "SSH_AUTH_SOCK" {
					return "/env.sock"
				}
				return ""
			},
		},
		Rec: &Recorder{Store: store, Sink: sink, Clock: clock, HeartbeatInterval: 5 * time.Minute},
	}
	return &checkRig{chk: chk, sink: sink, clock: clock, agent: agent, dir: dir}
}

func (r *checkRig) check(t *testing.T, req Request) Result {
	t.Helper()
	res, err := r.chk.Check(context.Background(), req)
	if err != nil {
		t.Fatalf("Check(%+v): %v", req, err)
	}
	return res
}

func TestCheckAvailable(t *testing.T) {
	r := newCheckRig(t)
	res := r.check(t, Request{Realm: "zr"})
	if !res.Available || res.Reason != ReasonOK || res.Realm != "zr" || !res.Since.IsZero() {
		t.Fatalf("res = %+v", res)
	}
	if n := len(r.sink.list()); n != 0 {
		t.Errorf("%d events for an available realm", n)
	}
}

func TestCheckCertExpiredThenRecovers(t *testing.T) {
	r := newCheckRig(t)
	r.clock.Advance(2 * time.Hour)
	res := r.check(t, Request{Realm: "zr"})
	if res.Available || res.Reason != ReasonCertExpired || !res.Since.Equal(r.clock.Now()) {
		t.Fatalf("res = %+v", res)
	}
	if evs := r.sink.list(); len(evs) != 1 || evs[0].Reason != "cert-expired" || *evs[0].Transition != 1 {
		t.Fatalf("events = %+v", evs)
	}
	r.clock.Advance(10 * time.Minute)
	res = r.check(t, Request{Realm: "zr"}) // still expired: heartbeat
	if res.Since.Equal(r.clock.Now()) || len(r.sink.list()) != 2 {
		t.Fatalf("res=%+v events=%d, want heartbeat and the first Since", res, len(r.sink.list()))
	}
	// a manual renewal is seen at once
	r.agent["/env.sock"] = []Cert{{ValidAfter: r.clock.Now().Add(-time.Minute), ValidBefore: r.clock.Now().Add(time.Hour)}}
	res = r.check(t, Request{Realm: "zr"})
	if !res.Available || res.Reason != ReasonOK {
		t.Fatalf("res = %+v", res)
	}
	if evs := r.sink.list(); len(evs) != 3 || *evs[2].Available != 1 || *evs[2].Transition != 1 {
		t.Fatalf("recovery event missing: %+v", evs)
	}
}

func TestCheckAgentUnreachableAndCertMissing(t *testing.T) {
	r := newCheckRig(t)
	r.agent["/env.sock"] = nil
	if res := r.check(t, Request{Realm: "zr"}); res.Reason != ReasonCertMissing || res.Available {
		t.Errorf("res = %+v", res)
	}
	delete(r.agent, "/env.sock")
	if res := r.check(t, Request{Realm: "zr"}); res.Reason != ReasonAgentUnreachable || res.Available {
		t.Errorf("res = %+v", res)
	}
}

func TestCheckIgnoredRealm(t *testing.T) {
	r := newCheckRig(t)
	r.agent["/env.sock"] = nil // would be cert-missing
	if err := r.chk.Store.SetIgnored("zr", true); err != nil {
		t.Fatal(err)
	}
	res := r.check(t, Request{Realm: "zr"})
	if !res.Available || res.Reason != ReasonIgnored {
		t.Fatalf("res = %+v", res)
	}
	if n := len(r.sink.list()); n != 0 {
		t.Errorf("an ignored realm must emit nothing, got %d", n)
	}
}

func TestCheckBreakerOpenWindow(t *testing.T) {
	r := newCheckRig(t)
	b := r.chk.Config.Breaker
	if err := r.chk.Store.Update("zr", func(st *RealmState) error {
		st.ApplyStartupFailure(r.clock.Now(), ReasonNetwork, "dial tcp: i/o timeout", b)
		st.ApplyStartupFailure(r.clock.Now(), ReasonNetwork, "dial tcp: i/o timeout", b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res := r.check(t, Request{Realm: "zr"})
	if res.Available || res.Reason != ReasonNetwork {
		t.Fatalf("res = %+v", res)
	}
	if res.LastError != "dial tcp: i/o timeout" {
		t.Errorf("LastError = %q", res.LastError)
	}
	r.clock.Advance(b.OpenDuration)
	if res := r.check(t, Request{Realm: "zr"}); !res.Available {
		t.Fatalf("breaker must close after open_duration: %+v", res)
	}
}

func TestCheckBreakerWithUnknownClassReportsUnknown(t *testing.T) {
	if breakerReason("") != ReasonUnknown || breakerReason("weird") != ReasonUnknown ||
		breakerReason("timeout") != ReasonTimeout {
		t.Error("breakerReason maps outside the closed set")
	}
}

func TestCheckCertBeatsBreaker(t *testing.T) {
	r := newCheckRig(t)
	_ = r.chk.Store.Update("zr", func(st *RealmState) error {
		st.OpenUntil = r.clock.Now().Add(time.Hour)
		st.Class = "network"
		return nil
	})
	delete(r.agent, "/env.sock")
	if res := r.check(t, Request{Realm: "zr"}); res.Reason != ReasonAgentUnreachable {
		t.Fatalf("res = %+v", res)
	}
}

func TestCheckStateWriteFailureNeverChangesVerdict(t *testing.T) {
	r := newCheckRig(t)
	// A file where the state directory should be: every state write fails.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(blocker, "state"), r.clock, 0)
	r.chk.Store = store
	r.chk.Rec.Store = store
	var recErr error
	r.chk.OnRecordError = func(err error) { recErr = err }

	if res := r.check(t, Request{Realm: "zr"}); !res.Available || res.Reason != ReasonOK {
		t.Fatalf("available verdict changed: %+v", res)
	}
	r.clock.Advance(2 * time.Hour)
	res := r.check(t, Request{Realm: "zr"})
	if res.Available || res.Reason != ReasonCertExpired {
		t.Fatalf("unavailable verdict changed: %+v", res)
	}
	if recErr == nil {
		t.Error("write failure was not reported to OnRecordError")
	}
}

func TestCheckEventFailureNeverChangesVerdict(t *testing.T) {
	r := newCheckRig(t)
	r.sink.err = errors.New("disk full")
	r.clock.Advance(2 * time.Hour)
	if res := r.check(t, Request{Realm: "zr"}); res.Available || res.Reason != ReasonCertExpired {
		t.Fatalf("res = %+v", res)
	}
}

func TestCheckResolvesRealmFromRemote(t *testing.T) {
	r := newCheckRig(t)
	good := []string{
		"ssh://org-acme@github.com/org/repo.git",
		"org-acme@github.com:org/repo.git",
	}
	for _, remote := range good {
		if res := r.check(t, Request{Remote: remote}); res.Realm != "zr" {
			t.Errorf("remote %q -> realm %q", remote, res.Realm)
		}
	}
	for _, remote := range []string{"git@github.com:org/repo.git", "org-a@gitlab.com:x/y.git", "https://github.com/org/repo", "/local/path"} {
		var ue *UsageError
		if _, err := r.chk.Check(context.Background(), Request{Remote: remote}); !errors.As(err, &ue) {
			t.Errorf("remote %q: err = %v, want UsageError", remote, err)
		}
	}
}

func TestParseRemote(t *testing.T) {
	cases := []struct {
		in         string
		host, user string
		ok         bool
	}{
		{"ssh://u@h.example:2222/x", "h.example", "u", true},
		{"https://h.example/x", "h.example", "", true},
		{"u@h.example:x/y", "h.example", "u", true},
		{"h.example:x/y", "h.example", "", true},
		{"/abs/path", "", "", false},
		{"relative/path:x", "", "", false},
		{"ssh://", "", "", false},
	}
	for _, c := range cases {
		h, u, ok := ParseRemote(c.in)
		if h != c.host || u != c.user || ok != c.ok {
			t.Errorf("ParseRemote(%q) = %q,%q,%v", c.in, h, u, ok)
		}
	}
}

func TestCheckUsageErrors(t *testing.T) {
	r := newCheckRig(t)
	for _, req := range []Request{{}, {Realm: "nope"}, {Realm: "zr", Remote: "x@y:z"}} {
		var ue *UsageError
		if _, err := r.chk.Check(context.Background(), req); !errors.As(err, &ue) {
			t.Errorf("Check(%+v) err = %v, want UsageError", req, err)
		}
	}
}

func TestCheckRepoPathMountMissing(t *testing.T) {
	r := newCheckRig(t)
	repo := t.TempDir() // no .git: an unmounted repository
	res := r.check(t, Request{RepoPath: repo})
	if res.Available || res.Reason != ReasonMountMissing {
		t.Fatalf("res = %+v", res)
	}
}

func TestCheckRepoPathResolvesThroughSeam(t *testing.T) {
	r := newCheckRig(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	var asked string
	r.chk.Resolve = func(_ context.Context, p string) (string, error) {
		asked = p
		return "org-acme@github.com:org/repo.git", nil
	}
	res := r.check(t, Request{RepoPath: repo})
	if asked != repo || res.Realm != "zr" || !res.Available {
		t.Fatalf("asked=%q res=%+v", asked, res)
	}
	r.chk.Resolve = func(context.Context, string) (string, error) { return "", errors.New("git failed") }
	if _, err := r.chk.Check(context.Background(), Request{RepoPath: repo}); err == nil {
		t.Error("a resolver fault must surface as an error, not as unavailable")
	}
}

func TestCheckRepoPathWorktreeGitFile(t *testing.T) {
	r := newCheckRig(t)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: /x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.chk.Resolve = func(context.Context, string) (string, error) { return "org-a@github.com:o/r.git", nil }
	if res := r.check(t, Request{RepoPath: repo}); res.Reason == ReasonMountMissing {
		t.Fatalf(".git file (worktree) must count as present: %+v", res)
	}
}

func TestGitRemoteResolverReadsLocalConfig(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := execCommand("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", "org-acme@github.com:org/repo.git")
	got, err := GitRemoteResolver(context.Background(), repo)
	if err != nil || got != "org-acme@github.com:org/repo.git" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := GitRemoteResolver(context.Background(), t.TempDir()); err == nil {
		t.Error("a non-repository must be an error")
	}
}
