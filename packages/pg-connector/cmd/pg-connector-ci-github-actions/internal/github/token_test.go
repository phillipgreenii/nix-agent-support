package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// TestEnvWithoutGHToken exercises the general strip-token behavior; the
// enterprise/target-var and git-dir-family regressions have their own
// dedicated tests below since they cover distinct, unrelated leak vectors.
func TestEnvWithoutGHToken(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GH_TOKEN=secret",
		"HOME=/home/me",
		"GITHUB_TOKEN=other",
		"LANG=en_US.UTF-8",
	}
	out := envWithoutGHToken(in)
	for _, kv := range out {
		if strings.HasPrefix(kv, "GH_TOKEN=") || strings.HasPrefix(kv, "GITHUB_TOKEN=") {
			t.Errorf("envWithoutGHToken left token entry: %q", kv)
		}
	}
	// Non-token entries must survive.
	want := map[string]bool{"PATH=/usr/bin": false, "HOME=/home/me": false, "LANG=en_US.UTF-8": false}
	for _, kv := range out {
		if _, ok := want[kv]; ok {
			want[kv] = true
		}
	}
	for kv, seen := range want {
		if !seen {
			t.Errorf("envWithoutGHToken dropped non-token entry %q", kv)
		}
	}
}

// TestEnvWithoutGHToken_StripsEnterpriseAndTargetVars is the regression test
// for bead pg2-y23d4 #21: under an enterprise GH_HOST, gh prefers
// GH_ENTERPRISE_TOKEN/GITHUB_ENTERPRISE_TOKEN over the resolved GH_TOKEN, so
// an ambient enterprise credential would otherwise silently win; an
// inherited GH_REPO overrides the explicit --repo this backend always
// passes; and GH_CONFIG_DIR points gh at a different config/credential
// store. None of these were previously stripped.
func TestEnvWithoutGHToken_StripsEnterpriseAndTargetVars(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GH_ENTERPRISE_TOKEN=ent-secret",
		"GITHUB_ENTERPRISE_TOKEN=ent-other",
		"GH_HOST=github.example.com",
		"GH_REPO=leaked/repo",
		"GH_CONFIG_DIR=/leaked/gh-config",
		"HOME=/home/me",
	}
	out := envWithoutGHToken(in)
	stripped := []string{
		"GH_ENTERPRISE_TOKEN=", "GITHUB_ENTERPRISE_TOKEN=",
		"GH_HOST=", "GH_REPO=", "GH_CONFIG_DIR=",
	}
	for _, kv := range out {
		for _, prefix := range stripped {
			if strings.HasPrefix(kv, prefix) {
				t.Errorf("envWithoutGHToken left enterprise/target entry: %q", kv)
			}
		}
	}
	want := map[string]bool{"PATH=/usr/bin": false, "HOME=/home/me": false}
	for _, kv := range out {
		if _, ok := want[kv]; ok {
			want[kv] = true
		}
	}
	for kv, seen := range want {
		if !seen {
			t.Errorf("envWithoutGHToken dropped non-token entry %q", kv)
		}
	}
}

func TestEnvWithGHToken(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GH_TOKEN=old",
		"GITHUB_TOKEN=older",
	}
	out := envWithGHToken(in, "newtok")

	var ghCount, githubCount int
	var ghVal string
	for _, kv := range out {
		switch {
		case strings.HasPrefix(kv, "GH_TOKEN="):
			ghCount++
			ghVal = strings.TrimPrefix(kv, "GH_TOKEN=")
		case strings.HasPrefix(kv, "GITHUB_TOKEN="):
			githubCount++
		}
	}
	if ghCount != 1 {
		t.Errorf("want exactly one GH_TOKEN= entry, got %d", ghCount)
	}
	if githubCount != 0 {
		t.Errorf("want no lingering GITHUB_TOKEN= entry, got %d", githubCount)
	}
	if ghVal != "newtok" {
		t.Errorf("GH_TOKEN value = %q, want newtok", ghVal)
	}
}

// TestGHAuthTokenCommand_ExcludesEnterpriseAndTargetVars is the token-
// resolver half of bead pg2-y23d4 #21: ghAuthTokenCommand must not hand any
// of the enterprise-credential or target-repo vars to the `gh auth token`
// child.
func TestGHAuthTokenCommand_ExcludesEnterpriseAndTargetVars(t *testing.T) {
	t.Setenv("GH_ENTERPRISE_TOKEN", "ent-secret")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "ent-other")
	t.Setenv("GH_HOST", "github.example.com")
	t.Setenv("GH_REPO", "leaked/repo")
	t.Setenv("GH_CONFIG_DIR", "/leaked/gh-config")

	cmd := ghAuthTokenCommand(context.Background())

	for _, name := range []string{
		"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
		"GH_HOST", "GH_REPO", "GH_CONFIG_DIR",
	} {
		for _, kv := range cmd.Env {
			if strings.HasPrefix(kv, name+"=") {
				t.Errorf("ghAuthTokenCommand env carries leaked %q into the gh child", name)
			}
		}
	}
}

// TestGHAuthTokenCommand_ExcludesLeakedGitDirFamily is the token-resolver
// half of bead pg2-5xn2j's regression, ported to this backend:
// ghAuthTokenCommand has the same os.Environ()-passthrough shape as
// ghexec.go's choke point, so it must be scrubbed the same way even though
// `gh auth token` is account-scoped rather than repository-scoped.
// leakedGitDirFamily/assertNoLeakedGitDirFamily live in ghexec_test.go
// (same package) since ghexec_test.go's own choke-point test needs them
// too.
func TestGHAuthTokenCommand_ExcludesLeakedGitDirFamily(t *testing.T) {
	for _, kv := range leakedGitDirFamily {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}

	cmd := ghAuthTokenCommand(context.Background())

	assertNoLeakedGitDirFamily(t, cmd.Env)
}

// TestGHAuthTokenCommand_StripsAmbientGHToken proves the resolver's own `gh
// auth token` exec never forwards an inherited GH_TOKEN/GITHUB_TOKEN — the
// reason this backend's ghCLITokenSource strips them (auth.go/token.go's
// doc comments): a leaked ambient token would make `gh auth token` just
// echo it back instead of consulting the stored (keychain) credential.
func TestGHAuthTokenCommand_StripsAmbientGHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "ambient-gh")
	t.Setenv("GITHUB_TOKEN", "ambient-github")

	cmd := ghAuthTokenCommand(context.Background())

	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GH_TOKEN=") || strings.HasPrefix(kv, "GITHUB_TOKEN=") {
			t.Errorf("ghAuthTokenCommand leaked ambient token entry into its own env: %q", kv)
		}
	}
}

// TestGHCLITokenSource_Token_SurfacesStderrOnFailure is the regression test
// for bead pg2-y23d4 #32: Token() used to call .Output() and return only
// "gh auth token: exit status N", discarding gh's actual stderr — so every
// credential failure looked identical regardless of cause. It must now
// surface the real message.
func TestGHCLITokenSource_Token_SurfacesStderrOnFailure(t *testing.T) {
	stderrMsg := "HTTP 403: Resource protected by organization SAML enforcement. You must grant your personal access token access to this organization."
	ghStubExitingWithStderr(t, 1, stderrMsg)

	_, err := ghCLITokenSource{}.Token(context.Background())
	if err == nil {
		t.Fatal("Token() = nil error, want error from the failing gh stub")
	}
	if !strings.Contains(err.Error(), "SAML enforcement") {
		t.Errorf("Token() error does not surface gh's stderr, got: %v", err)
	}
}

func TestEnvTokenSource(t *testing.T) {
	t.Run("set returns value", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "x")
		src := envTokenSource{vars: []string{"GH_TOKEN", "GITHUB_TOKEN"}}
		tok, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "x" {
			t.Errorf("Token = %q, want x", tok)
		}
	})
	t.Run("empty returns empty no error", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		src := envTokenSource{vars: []string{"GH_TOKEN", "GITHUB_TOKEN"}}
		tok, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "" {
			t.Errorf("Token = %q, want empty", tok)
		}
	})
	t.Run("second var used when first is empty", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "fallback")
		src := envTokenSource{vars: []string{"GH_TOKEN", "GITHUB_TOKEN"}}
		tok, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "fallback" {
			t.Errorf("Token = %q, want fallback", tok)
		}
	})
}

// fakeTokenSource is a TokenSource stub used by the chain tests here and by
// ghexec_test.go's CLI-level tests; it records whether it was consulted so
// chain/short-circuit behavior can be asserted.
type fakeTokenSource struct {
	tok    string
	err    error
	called bool
}

func (f *fakeTokenSource) Token(_ context.Context) (string, error) {
	f.called = true
	return f.tok, f.err
}

func TestChainTokenSource(t *testing.T) {
	t.Run("env set short-circuits without calling gh", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "envtok")
		gh := &fakeTokenSource{tok: "ghtok"}
		chain := chainTokenSource{sources: []TokenSource{
			envTokenSource{vars: []string{"GH_TOKEN", "GITHUB_TOKEN"}},
			gh,
		}}
		tok, err := chain.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "envtok" {
			t.Errorf("Token = %q, want envtok", tok)
		}
		if gh.called {
			t.Error("downstream source consulted despite env hit")
		}
	})

	t.Run("env empty falls through", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		gh := &fakeTokenSource{tok: "ghtok"}
		chain := chainTokenSource{sources: []TokenSource{
			envTokenSource{vars: []string{"GH_TOKEN", "GITHUB_TOKEN"}},
			gh,
		}}
		tok, err := chain.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "ghtok" {
			t.Errorf("Token = %q, want ghtok", tok)
		}
		if !gh.called {
			t.Error("downstream source not consulted on env miss")
		}
	})

	t.Run("downstream error surfaces when no source yields a token", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		chain := chainTokenSource{sources: []TokenSource{
			envTokenSource{vars: []string{"GH_TOKEN", "GITHUB_TOKEN"}},
			&fakeTokenSource{tok: ""},
		}}
		_, err := chain.Token(context.Background())
		if err == nil {
			t.Fatal("want error when no source yields a token")
		}
	})
}

// TestDefaultTokenSource_HonorsEnvBeforeGH proves defaultTokenSource wires
// the env source ahead of the gh CLI source, matching its doc comment
// ("honors an existing GH_TOKEN/GITHUB_TOKEN, else reads gh's stored
// credential"): with GH_TOKEN set, the gh CLI must never be consulted, so
// this must resolve without gh on PATH at all.
func TestDefaultTokenSource_HonorsEnvBeforeGH(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no gh anywhere on PATH
	t.Setenv("GH_TOKEN", "envtok")
	t.Setenv("GITHUB_TOKEN", "")

	tok, err := defaultTokenSource().Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "envtok" {
		t.Errorf("Token = %q, want envtok", tok)
	}
}

// ghStubScript puts an executable named `gh` with the given /bin/sh body on
// PATH, shadowing any real gh.
func ghStubScript(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("write gh stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCLIRun_TokenLookupKilledAtDeadline_IsNotAuthInvalid mirrors the sibling
// backend's regression test for bead pg2-ev2uf: a `gh auth token` killed at
// the per-call budget never judged the credential, so it must not surface as
// ErrGHAuthInvalid (unauthenticated) or tell the operator to re-login.
func TestCLIRun_TokenLookupKilledAtDeadline_IsNotAuthInvalid(t *testing.T) {
	ghStubScript(t, "exec sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	out, err := NewCLIWithTokenSource(ghCLITokenSource{}).Run(ctx, "run", "list")

	if len(out) != 0 {
		t.Errorf("Run returned stdout %q, want empty", out)
	}
	if err == nil {
		t.Fatal("Run returned nil error although the token lookup was killed")
	}
	if errors.Is(err, ErrGHAuthInvalid) {
		t.Errorf("a killed `gh auth token` was classified as auth-invalid: %v", err)
	}
	if strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("error tells the operator to re-login for a lookup that never finished: %v", err)
	}
	if got := scriptout.CodeForError(err); got != "unavailable" {
		t.Errorf("wire code = %q, want unavailable (err=%v)", got, err)
	}
}

// TestCLIRun_TokenLookupSignalled_IsNotAuthInvalid covers a `gh auth token`
// killed by a signal with the context still live.
func TestCLIRun_TokenLookupSignalled_IsNotAuthInvalid(t *testing.T) {
	ghStubScript(t, "kill -KILL $$")

	_, err := NewCLIWithTokenSource(ghCLITokenSource{}).Run(context.Background(), "run", "list")

	if err == nil {
		t.Fatal("Run returned nil error although the token lookup was killed")
	}
	if errors.Is(err, ErrGHAuthInvalid) {
		t.Errorf("a signalled `gh auth token` was classified as auth-invalid: %v", err)
	}
}

// TestCLIRun_TokenLookupGenuineNonZeroExit_IsAuthInvalid pins the other side:
// a `gh auth token` that ran to completion and exited non-zero stays in the
// unauthenticated class.
func TestCLIRun_TokenLookupGenuineNonZeroExit_IsAuthInvalid(t *testing.T) {
	for _, exit := range []int{1, 4} {
		t.Run(fmt.Sprintf("exit %d", exit), func(t *testing.T) {
			ghStubExitingWithStderr(t, exit, "no oauth token found for github.com")

			_, err := NewCLIWithTokenSource(ghCLITokenSource{}).Run(context.Background(), "run", "list")

			if !errors.Is(err, ErrGHAuthInvalid) {
				t.Errorf("errors.Is(err, ErrGHAuthInvalid) = false, want true (err=%v)", err)
			}
		})
	}
}
