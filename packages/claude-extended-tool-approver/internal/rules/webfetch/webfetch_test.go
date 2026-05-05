package webfetch

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func makeWebFetchInput(url string) *hookio.HookInput {
	return &hookio.HookInput{
		ToolName:  "WebFetch",
		CWD:       "/tmp",
		ToolInput: mustJSON(map[string]string{"url": url, "prompt": ""}),
	}
}

func TestWebFetch_RawGitHub_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeWebFetchInput("https://raw.githubusercontent.com/owner/repo/main/file.go"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_Blob_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeWebFetchInput("https://github.com/owner/repo/blob/main/src/file.go"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_Raw_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeWebFetchInput("https://github.com/owner/repo/raw/main/file.go"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_RepoRoot_Approve(t *testing.T) {
	r := New()
	approve := []string{
		"https://github.com/owner/repo",
		"https://github.com/owner/repo/issues",
		"https://github.com/owner/repo/pulls",
		"https://github.com/owner/repo?tab=readme-ov-file",
		"https://github.com/owner/repo?tab=readme-ov-file#section",
	}
	for _, url := range approve {
		got := r.Evaluate(makeWebFetchInput(url))
		if got.Decision != hookio.Approve {
			t.Errorf("url %q: got %s, want approve", url, got.Decision)
		}
	}
}

func TestWebFetch_NonGitHub_Abstain(t *testing.T) {
	r := New()
	got := r.Evaluate(makeWebFetchInput("https://example.com/file.go"))
	if got.Decision != hookio.Abstain {
		t.Errorf("got %s, want abstain", got.Decision)
	}
}

func TestWebFetch_EmptyURL_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "WebFetch",
		ToolInput: mustJSON(map[string]string{"url": "", "prompt": ""}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("got %s, want abstain", got.Decision)
	}
}

func TestWebFetch_NonWebFetchTool_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "curl https://github.com/owner/repo/blob/main/file.go"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("got %s, want abstain (not WebFetch tool)", got.Decision)
	}
}

func TestWebFetch_DocsAnthropic_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeWebFetchInput("https://docs.anthropic.com/en/docs/overview"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_MDN_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeWebFetchInput("https://developer.mozilla.org/en-US/docs/Web/JavaScript"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_NixosOrg_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeWebFetchInput("https://nixos.org/manual/nix/stable/"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_PkgGoDev_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeWebFetchInput("https://pkg.go.dev/fmt"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_GitHubReleaseBinary_Abstain(t *testing.T) {
	r := New()
	got := r.Evaluate(makeWebFetchInput("https://github.com/owner/repo/releases/download/v1.0/binary.tar.gz"))
	if got.Decision != hookio.Abstain {
		t.Errorf("got %s, want abstain (release binary)", got.Decision)
	}
}

func TestWebFetch_RegistryNpmjs_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeWebFetchInput("https://registry.npmjs.org/express"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "webfetch" {
		t.Errorf("Name() = %q, want webfetch", got)
	}
}
