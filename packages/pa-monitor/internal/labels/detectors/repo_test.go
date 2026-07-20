package detectors

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/labels"
)

func TestNormaliseGitOrigin(t *testing.T) {
	cases := map[string]string{
		"git@github.com:owner/repo.git":      "github.com/owner/repo",
		"https://github.com/owner/repo.git":  "github.com/owner/repo",
		"https://github.com/owner/repo":      "github.com/owner/repo",
		"ssh://git@github.com/owner/repo":    "github.com/owner/repo",
		"git@gitlab.com:group/sub/repo.git":  "gitlab.com/group/sub/repo",
		"http://internal.example/owner/repo": "internal.example/owner/repo",
	}
	for in, want := range cases {
		if got := NormaliseOrigin(in); got != want {
			t.Errorf("NormaliseOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeRepoCache struct {
	calls []string
	val   string
	ok    bool
}

func (f *fakeRepoCache) RepoLabel(cwd string) (string, bool) {
	f.calls = append(f.calls, cwd)
	return f.val, f.ok
}

// When a Cache is wired, Detect delegates to it and does NOT spawn git.
func TestRepo_UsesCacheWhenSet(t *testing.T) {
	fc := &fakeRepoCache{val: "acme/x", ok: true}
	r := Repo{Cache: fc}
	got := r.Detect(labels.Session{ID: "s1", CWD: "/some/cwd"})
	if got["workspace.repo"] != "acme/x" {
		t.Fatalf("Detect via cache = %v, want workspace.repo=acme/x", got)
	}
	if len(fc.calls) != 1 || fc.calls[0] != "/some/cwd" {
		t.Fatalf("cache not consulted with the cwd: %v", fc.calls)
	}
}

func TestRepo_CacheMissReturnsNil(t *testing.T) {
	fc := &fakeRepoCache{ok: false}
	r := Repo{Cache: fc}
	if got := r.Detect(labels.Session{ID: "s1", CWD: "/some/cwd"}); got != nil {
		t.Fatalf("cache miss should yield nil label set, got %v", got)
	}
}
