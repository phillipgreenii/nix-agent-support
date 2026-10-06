package internal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeRemoteURL(t *testing.T) {
	cases := map[string]string{
		"https://github.example.test/owner/name.git":         "github.example.test/owner/name",
		"https://github.example.test/owner/name":             "github.example.test/owner/name",
		"https://github.example.test/owner/name/":            "github.example.test/owner/name",
		"https://user:secret@Github.Example.test/owner/name": "github.example.test/owner/name",
		"https://github.example.test:8443/owner/name.git":    "github.example.test/owner/name",
		"ssh://git@github.example.test:22/owner/name.git":    "github.example.test/owner/name",
		"git@github.example.test:owner/name.git":             "github.example.test/owner/name",
		"github.example.test:owner/name":                     "github.example.test/owner/name",
		"user@host.example.test:group/sub/name.git":          "host.example.test/group/sub/name",
		"/srv/git/owner/name.git":                            "srv/git/owner/name",
		"file:///srv/git/owner/name.git":                     "srv/git/owner/name",
		"":                                                   "",
		"   ":                                                "",
	}
	for in, want := range cases {
		if got := normalizeRemoteURL(in); got != want {
			t.Errorf("normalizeRemoteURL(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeRunner struct {
	calls [][]string
	run   func(dir string, args ...string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, dir string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{dir}, args...))
	return f.run(dir, args...)
}

func TestRepoIdentFromOrigin(t *testing.T) {
	r := &fakeRunner{run: func(string, ...string) (string, error) {
		return "git@github.example.test:owner/name.git", nil
	}}
	if got, want := repoIdent(context.Background(), r, "/clones/x"), "github.example.test/owner/name"; got != want {
		t.Errorf("repoIdent = %q, want %q", got, want)
	}
	if len(r.calls) != 1 || strings.Join(r.calls[0], " ") != "/clones/x config --get remote.origin.url" {
		t.Errorf("calls = %v", r.calls)
	}
}

func TestRepoIdentFallbackWithoutOrigin(t *testing.T) {
	r := &fakeRunner{run: func(string, ...string) (string, error) { return "", errors.New("exit status 1") }}
	a := repoIdent(context.Background(), r, "/clones/proj")
	b := repoIdent(context.Background(), r, "/other/proj")
	if !strings.HasPrefix(a, "proj-") || len(a) != len("proj-")+8 {
		t.Errorf("fallback ident = %q, want proj-<8 hex>", a)
	}
	if a == b {
		t.Errorf("same basename at different paths produced the same ident %q", a)
	}
	if a != repoIdent(context.Background(), r, "/clones/proj/") {
		t.Error("fallback ident must be stable across a trailing slash")
	}
}

func TestRepoIdentUnparseableOriginFallsBack(t *testing.T) {
	r := &fakeRunner{run: func(string, ...string) (string, error) { return "", nil }}
	if got := repoIdent(context.Background(), r, "/clones/proj"); !strings.HasPrefix(got, "proj-") {
		t.Errorf("repoIdent = %q, want fallback", got)
	}
}
