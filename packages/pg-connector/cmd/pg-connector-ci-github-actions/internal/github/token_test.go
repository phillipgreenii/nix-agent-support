package github

import (
	"context"
	"strings"
	"testing"
)

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
}
