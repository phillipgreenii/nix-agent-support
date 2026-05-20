package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/plugin/scriptout"
)

// makeConfig builds a Config with the given repo provider triples
// (vcs, cicd[], issues). All other Config fields are filled with placeholders
// so finalize-style invariants are not violated; we bypass finalize because
// these tests construct *Config directly.
func makeConfig(repos ...config.RepoConfig) *config.Config {
	if len(repos) == 0 {
		repos = []config.RepoConfig{{Remote: "owner/repo", VCS: "github"}}
	}
	return &config.Config{
		SelfLogin:    "me",
		WorktreeRoot: "/tmp/wt",
		Repos:        repos,
	}
}

func TestCheckAll_GitHubOK(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{Remote: "o/r", VCS: "github"})
	runners := Runners{
		GH: func(_ context.Context, args ...string) (string, string, error) {
			if got, want := strings.Join(args, " "), "auth status"; got != want {
				t.Fatalf("gh args = %q, want %q", got, want)
			}
			// `gh auth status` writes to stderr in practice.
			return "", "github.com\n  Token scopes: 'repo', 'workflow'\n", nil
		},
	}
	got, err := CheckAllWithRunners(context.Background(), cfg, runners)
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}
	if len(got) != 1 || got[0].Provider != "github" {
		t.Fatalf("expected one github status, got %+v", got)
	}
	if got[0].State != string(StateOK) {
		t.Fatalf("state = %s, want OK", got[0].State)
	}
	if !strings.Contains(got[0].Detail, "Token scopes") {
		t.Fatalf("expected scopes in detail, got %q", got[0].Detail)
	}
}

func TestCheckAll_GitHubMissing(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{Remote: "o/r", VCS: "github"})
	runners := Runners{
		GH: func(_ context.Context, _ ...string) (string, string, error) {
			return "", "You are not logged into any GitHub hosts.\n", errors.New("exit status 1")
		},
	}
	got, _ := CheckAllWithRunners(context.Background(), cfg, runners)
	if got[0].State != string(StateMissing) {
		t.Fatalf("state = %s, want MISSING", got[0].State)
	}
}

func TestCheckAll_GitHubExpired(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{Remote: "o/r", VCS: "github"})
	runners := Runners{
		GH: func(_ context.Context, _ ...string) (string, string, error) {
			return "", "The token expired on 2026-01-01\n", errors.New("exit status 1")
		},
	}
	got, _ := CheckAllWithRunners(context.Background(), cfg, runners)
	if got[0].State != string(StateExpired) {
		t.Fatalf("state = %s, want EXPIRED", got[0].State)
	}
}

func TestCheckAll_JiraOK(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{Remote: "o/r", VCS: "github", Issues: "jira"})
	env := map[string]string{
		"JIRA_API_TOKEN": "tok",
		"JIRA_EMAIL":     "me@example.com",
		"JIRA_BASE_URL":  "https://example.atlassian.net/",
	}
	var seenURL, seenAuth string
	runners := Runners{
		// keep github happy via injection
		GH: func(_ context.Context, _ ...string) (string, string, error) {
			return "", "Token scopes: 'repo'", nil
		},
		Env: func(k string) string { return env[k] },
		HTTP: func(req *http.Request) (*http.Response, error) {
			seenURL = req.URL.String()
			seenAuth = req.Header.Get("Authorization")
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		},
	}
	got, _ := CheckAllWithRunners(context.Background(), cfg, runners)
	var jira *Status
	for i := range got {
		if got[i].Provider == "jira" {
			jira = &got[i]
		}
	}
	if jira == nil {
		t.Fatalf("no jira status in %+v", got)
	}
	if jira.State != string(StateOK) {
		t.Fatalf("state = %s, want OK", jira.State)
	}
	if !strings.HasSuffix(seenURL, "/rest/api/3/myself") {
		t.Fatalf("url = %s", seenURL)
	}
	if !strings.HasPrefix(seenAuth, "Basic ") {
		t.Fatalf("auth = %s", seenAuth)
	}
}

func TestCheckAll_JiraExpired(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{Remote: "o/r", VCS: "github", Issues: "jira"})
	env := map[string]string{
		"JIRA_API_TOKEN": "tok",
		"JIRA_EMAIL":     "me@example.com",
		"JIRA_BASE_URL":  "https://example.atlassian.net",
	}
	runners := Runners{
		GH:  func(_ context.Context, _ ...string) (string, string, error) { return "", "", nil },
		Env: func(k string) string { return env[k] },
		HTTP: func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(""))}, nil
		},
	}
	got, _ := CheckAllWithRunners(context.Background(), cfg, runners)
	var jira *Status
	for i := range got {
		if got[i].Provider == "jira" {
			jira = &got[i]
		}
	}
	if jira == nil || jira.State != string(StateExpired) {
		t.Fatalf("jira = %+v, want EXPIRED", jira)
	}
}

func TestCheckAll_JiraMissingEnv(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{Remote: "o/r", VCS: "github", Issues: "jira"})
	runners := Runners{
		GH:   func(_ context.Context, _ ...string) (string, string, error) { return "", "", nil },
		Env:  func(_ string) string { return "" }, // nothing set
		HTTP: func(_ *http.Request) (*http.Response, error) { t.Fatal("HTTP should not be called"); return nil, nil },
	}
	got, _ := CheckAllWithRunners(context.Background(), cfg, runners)
	var jira *Status
	for i := range got {
		if got[i].Provider == "jira" {
			jira = &got[i]
		}
	}
	if jira == nil || jira.State != string(StateMissing) {
		t.Fatalf("jira = %+v, want MISSING", jira)
	}
	if !strings.Contains(jira.Detail, "JIRA_API_TOKEN") {
		t.Fatalf("detail missing JIRA_API_TOKEN: %q", jira.Detail)
	}
}

func TestCheckAll_ExecOK(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{
		Remote: "o/r",
		VCS:    "github",
		CICD:   []string{"exec:my-cicd"},
	})
	runners := Runners{
		GH: func(_ context.Context, _ ...string) (string, string, error) { return "", "", nil },
		Exec: func(_ context.Context, binary string, req scriptout.Request) (scriptout.Response, error) {
			if binary != "my-cicd" {
				t.Fatalf("binary = %s", binary)
			}
			if req.Op != scriptout.OpAuthStatus {
				t.Fatalf("op = %s", req.Op)
			}
			return scriptout.Response{Result: scriptout.AuthStatus{
				State:  scriptout.AuthOK,
				Detail: "from exec",
			}}, nil
		},
	}
	got, _ := CheckAllWithRunners(context.Background(), cfg, runners)
	var ex *Status
	for i := range got {
		if got[i].Provider == "exec:my-cicd" {
			ex = &got[i]
		}
	}
	if ex == nil || ex.State != string(StateOK) {
		t.Fatalf("exec = %+v, want OK", ex)
	}
}

func TestCheckAll_ExecInsufficientScopes(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{
		Remote: "o/r",
		VCS:    "exec:my-vcs",
	})
	runners := Runners{
		Exec: func(_ context.Context, _ string, _ scriptout.Request) (scriptout.Response, error) {
			return scriptout.Response{Result: scriptout.AuthStatus{
				State:  scriptout.AuthInsufficientScopes,
				Detail: "needs 'repo' scope",
			}}, nil
		},
	}
	got, _ := CheckAllWithRunners(context.Background(), cfg, runners)
	if got[0].State != string(StateInsufficientScopes) {
		t.Fatalf("state = %s, want INSUFFICIENT_SCOPES", got[0].State)
	}
	if got[0].Detail != "needs 'repo' scope" {
		t.Fatalf("detail = %q", got[0].Detail)
	}
}

func TestCheckAll_ExecError(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{Remote: "o/r", VCS: "exec:bad"})
	runners := Runners{
		Exec: func(_ context.Context, _ string, _ scriptout.Request) (scriptout.Response, error) {
			return scriptout.Response{}, errors.New("binary not found")
		},
	}
	got, _ := CheckAllWithRunners(context.Background(), cfg, runners)
	if got[0].State != string(StateMissing) {
		t.Fatalf("state = %s", got[0].State)
	}
	if !strings.Contains(got[0].Detail, "binary not found") {
		t.Fatalf("detail = %q", got[0].Detail)
	}
}

func TestCheckAll_StableOrder(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{
		Remote: "o/r",
		VCS:    "github",
		CICD:   []string{"github-actions", "exec:zz"},
		Issues: "jira",
	})
	runners := Runners{
		GH:  func(_ context.Context, _ ...string) (string, string, error) { return "", "", nil },
		Env: func(_ string) string { return "" }, // jira missing
		Exec: func(_ context.Context, _ string, _ scriptout.Request) (scriptout.Response, error) {
			return scriptout.Response{Result: scriptout.AuthStatus{State: scriptout.AuthOK}}, nil
		},
	}
	got, _ := CheckAllWithRunners(context.Background(), cfg, runners)
	wantOrder := []string{"exec:zz", "github", "github-actions", "jira"}
	if len(got) != len(wantOrder) {
		t.Fatalf("len = %d (%+v)", len(got), got)
	}
	for i, w := range wantOrder {
		if got[i].Provider != w {
			t.Fatalf("got[%d] = %s, want %s", i, got[i].Provider, w)
		}
	}
}

func TestCheckAll_NilConfig(t *testing.T) {
	_, err := CheckAll(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error from nil config")
	}
}

// Smoke test: ensure exec dispatch round-trips through the JSON-tagged
// AuthStatus shape (state survives marshal/unmarshal).
func TestExecAuthStatus_JSONRoundTrip(t *testing.T) {
	cfg := makeConfig(config.RepoConfig{Remote: "o/r", VCS: "exec:p"})
	runners := Runners{
		Exec: func(_ context.Context, _ string, _ scriptout.Request) (scriptout.Response, error) {
			// Simulate JSON coming over the wire by re-marshaling.
			payload := scriptout.AuthStatus{State: scriptout.AuthExpired, Detail: "401 from upstream"}
			raw, _ := json.Marshal(payload)
			var asAny any
			if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&asAny); err != nil {
				return scriptout.Response{}, err
			}
			return scriptout.Response{Result: asAny}, nil
		},
	}
	got, _ := CheckAllWithRunners(context.Background(), cfg, runners)
	if got[0].State != string(StateExpired) {
		t.Fatalf("state = %s", got[0].State)
	}
	if got[0].Detail != "401 from upstream" {
		t.Fatalf("detail = %q", got[0].Detail)
	}
}
