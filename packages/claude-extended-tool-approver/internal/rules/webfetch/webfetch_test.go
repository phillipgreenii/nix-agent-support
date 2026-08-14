package webfetch

import (
	"encoding/json"
	"errors"
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
	got := hookio.Verdict(r.Evaluate(makeWebFetchInput("https://raw.githubusercontent.com/owner/repo/main/file.go")))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_Blob_Approve(t *testing.T) {
	r := New()
	got := hookio.Verdict(r.Evaluate(makeWebFetchInput("https://github.com/owner/repo/blob/main/src/file.go")))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_Raw_Approve(t *testing.T) {
	r := New()
	got := hookio.Verdict(r.Evaluate(makeWebFetchInput("https://github.com/owner/repo/raw/main/file.go")))
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
		got := hookio.Verdict(r.Evaluate(makeWebFetchInput(url)))
		if got.Decision != hookio.Approve {
			t.Errorf("url %q: got %s, want approve", url, got.Decision)
		}
	}
}

func TestWebFetch_NonGitHub_Abstain(t *testing.T) {
	r := New()
	got := hookio.Verdict(r.Evaluate(makeWebFetchInput("https://example.com/file.go")))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("got %s, want abstain", got.Decision)
	}
}

func TestWebFetch_EmptyURL_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "WebFetch",
		ToolInput: mustJSON(map[string]string{"url": "", "prompt": ""}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("got %s, want abstain", got.Decision)
	}
}

func TestWebFetch_NonWebFetchTool_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "curl https://github.com/owner/repo/blob/main/file.go"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("got %s, want abstain (not WebFetch tool)", got.Decision)
	}
}

func TestWebFetch_DocsAnthropic_Approve(t *testing.T) {
	r := New()
	got := hookio.Verdict(r.Evaluate(makeWebFetchInput("https://docs.anthropic.com/en/docs/overview")))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_MDN_Approve(t *testing.T) {
	r := New()
	got := hookio.Verdict(r.Evaluate(makeWebFetchInput("https://developer.mozilla.org/en-US/docs/Web/JavaScript")))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_NixosOrg_Approve(t *testing.T) {
	r := New()
	got := hookio.Verdict(r.Evaluate(makeWebFetchInput("https://nixos.org/manual/nix/stable/")))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_PkgGoDev_Approve(t *testing.T) {
	r := New()
	got := hookio.Verdict(r.Evaluate(makeWebFetchInput("https://pkg.go.dev/fmt")))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestWebFetch_GitHubReleaseBinary_Abstain(t *testing.T) {
	r := New()
	got := hookio.Verdict(r.Evaluate(makeWebFetchInput("https://github.com/owner/repo/releases/download/v1.0/binary.tar.gz")))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("got %s, want abstain (release binary)", got.Decision)
	}
}

func TestWebFetch_RegistryNpmjs_Approve(t *testing.T) {
	r := New()
	got := hookio.Verdict(r.Evaluate(makeWebFetchInput("https://registry.npmjs.org/express")))
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

// TestWebFetch_UnparseableURLIsAGenuineError pins webfetch's one error site, which
// the corpus replay cannot reach (a logged row carries a URL Claude Code already
// accepted).
//
// Before ADR 0043 the url.Parse failure folded into the same Abstain as "this is not
// a WebFetch tool" and "this host is not one I govern". The chain outcome is
// unchanged — still continue — but the failure is now countable, and the two
// not-applicable neighbours are asserted alongside it so the conversion cannot have
// swept them in as well.
func TestWebFetch_UnparseableURLIsAGenuineError(t *testing.T) {
	r := new(Rule)

	t.Run("unparseable url", func(t *testing.T) {
		// An unterminated IPv6 literal: url.Parse rejects it outright.
		_, err := r.Evaluate(makeWebFetchInput("http://[::1"))
		if err == nil || errors.Is(err, hookio.ErrNotApplicable) {
			t.Fatalf("err = %v, want a GENUINE error (not ErrNotApplicable): the tool IS WebFetch, so "+
				"this rule governs the call and merely could not read the URL", err)
		}
	})

	t.Run("empty url is not-applicable", func(t *testing.T) {
		_, err := r.Evaluate(makeWebFetchInput(""))
		if !errors.Is(err, hookio.ErrNotApplicable) {
			t.Errorf("err = %v, want ErrNotApplicable: no URL to judge is not a failure", err)
		}
	})

	t.Run("non-WebFetch tool is not-applicable", func(t *testing.T) {
		_, err := r.Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": "ls"})})
		if !errors.Is(err, hookio.ErrNotApplicable) {
			t.Errorf("err = %v, want ErrNotApplicable", err)
		}
	})
}

// TestADR0044_WebFetch_ReleaseDownloadRefuses is the per-rule half of pg2-qxe85's census
// for webfetch: its single site now REFUSES.
//
// The site is a CARVE-OUT of this rule's own approve — github.com is on the allowlist and
// every other path under it is cleared, so the release-asset branch is the one shape the
// rule looked at and declined. Reported as not-applicable that decision was
// indistinguishable from a URL on no allowlist at all (an EXHAUSTION), which is the half a
// consumer may act on to clear the fetch.
//
// The control row is the point of the relation: the allowlisted page must still APPROVE, so
// the floor is proven to be scoped to the leaf that earned it rather than to the host.
func TestADR0044_WebFetch_ReleaseDownloadRefuses(t *testing.T) {
	r := New()
	in := func(u string) *hookio.HookInput {
		return &hookio.HookInput{ToolName: "WebFetch", ToolInput: mustJSON(map[string]string{"url": u, "prompt": "x"})}
	}

	res, err := r.Evaluate(in("https://github.com/o/r/releases/download/v1.0/binary.tar.gz"))
	if !errors.Is(err, hookio.ErrRefused) {
		t.Fatalf("release download: err=%v res=%+v, want ErrRefused", err, res)
	}
	if res.Decision < hookio.NoOpinion {
		t.Errorf("release download: floor is %s, weaker than NoOpinion", res.Decision)
	}
	if res.Reason == "" || res.Module != r.Name() {
		t.Errorf("release download: floor = %+v, want a reasoned refusal attributed to %q", res, r.Name())
	}
	if !errors.Is(err, hookio.ErrNotApplicable) {
		t.Error("release download: refusal does not match ErrNotApplicable; the engine would file it as a FAILURE")
	}

	// An ordinary allowlisted page is UNAFFECTED: the conversion must not turn this rule's
	// approve into a floor for the whole host.
	res, err = r.Evaluate(in("https://github.com/o/r"))
	if err != nil || res.Decision != hookio.Approve {
		t.Errorf("ordinary GitHub page = %+v (err=%v), want approve — the refusal is scoped to the release path", res, err)
	}

	// A host on no allowlist stays a genuine not-applicable, NOT a refusal: this rule never
	// examined it, so claiming a refusal would assert a judgement that was not made.
	res, err = r.Evaluate(in("https://evil.example/x"))
	if errors.Is(err, hookio.ErrRefused) {
		t.Errorf("unallowlisted host reported a REFUSAL (%+v); it was never examined", res)
	}
	if !errors.Is(err, hookio.ErrNotApplicable) {
		t.Errorf("unallowlisted host: err=%v, want ErrNotApplicable", err)
	}
}
