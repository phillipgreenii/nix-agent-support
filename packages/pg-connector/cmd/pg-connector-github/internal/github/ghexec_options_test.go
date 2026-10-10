package github

import (
	"context"
	"strings"
	"testing"
)

// ghHostEntries returns every GH_HOST= entry of env.
func ghHostEntries(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "GH_HOST=") {
			out = append(out, kv)
		}
	}
	return out
}

// TestCLIWithOptions_MainCallCarriesBinaryAndHost proves the main gh call uses
// the configured binary and carries exactly one GH_HOST, the configured one,
// even when the parent environment carries a different GH_HOST (the strip
// removes the ambient value; the configured one is appended after it).
func TestCLIWithOptions_MainCallCarriesBinaryAndHost(t *testing.T) {
	t.Setenv("GH_HOST", "ambient.example.invalid")
	t.Setenv("GH_CONFIG_DIR", "/ambient/gh-config")

	cli := NewCLIWithOptions(Options{
		Binary:      "/opt/example/bin/gh-custom",
		Host:        "github.example.com",
		TokenSource: &fakeTokenSource{tok: "resolved-tok"},
	})
	cmd, err := cli.Command(context.Background(), "api", "rate_limit")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Path != "/opt/example/bin/gh-custom" && cmd.Args[0] != "/opt/example/bin/gh-custom" {
		t.Errorf("main call binary = Path %q Args[0] %q, want /opt/example/bin/gh-custom", cmd.Path, cmd.Args[0])
	}
	if got := ghHostEntries(cmd.Env); len(got) != 1 || got[0] != "GH_HOST=github.example.com" {
		t.Errorf("main call GH_HOST entries = %v, want exactly [GH_HOST=github.example.com]", got)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GH_CONFIG_DIR=") {
			t.Errorf("main call env carries ambient %q", kv)
		}
	}
}

// TestCLIWithOptions_TokenChildCarriesBinaryAndHost proves the `gh auth token`
// child built by the default token chain uses the configured binary and host
// too, with the same append-after-strip ordering.
func TestCLIWithOptions_TokenChildCarriesBinaryAndHost(t *testing.T) {
	t.Setenv("GH_HOST", "ambient.example.invalid")

	cmd := ghAuthTokenCommand(context.Background(), "/opt/example/bin/gh-custom", "github.example.com")
	if cmd.Args[0] != "/opt/example/bin/gh-custom" {
		t.Errorf("token child binary = %q, want /opt/example/bin/gh-custom", cmd.Args[0])
	}
	if got := strings.Join(cmd.Args[1:], " "); got != "auth token" {
		t.Errorf("token child args = %q, want %q", got, "auth token")
	}
	if got := ghHostEntries(cmd.Env); len(got) != 1 || got[0] != "GH_HOST=github.example.com" {
		t.Errorf("token child GH_HOST entries = %v, want exactly [GH_HOST=github.example.com]", got)
	}

	// The default chain built by NewCLIWithOptions wires the same values.
	cli := NewCLIWithOptions(Options{Binary: "/opt/example/bin/gh-custom", Host: "github.example.com"})
	chain, ok := cli.r.src.(chainTokenSource)
	if !ok {
		t.Fatalf("default token source is %T, want chainTokenSource", cli.r.src)
	}
	var found bool
	for _, s := range chain.sources {
		if g, ok := s.(ghCLITokenSource); ok {
			found = true
			if g.bin != "/opt/example/bin/gh-custom" || g.host != "github.example.com" {
				t.Errorf("chain gh source = %+v, want the configured binary and host", g)
			}
		}
	}
	if !found {
		t.Error("default chain has no ghCLITokenSource")
	}
}

// TestCLIWithOptions_ZeroValueIsToday proves the zero Options behaves exactly
// like the original NewCLI: binary gh, no GH_HOST in either child.
func TestCLIWithOptions_ZeroValueIsToday(t *testing.T) {
	t.Setenv("GH_HOST", "ambient.example.invalid")

	cli := NewCLIWithOptions(Options{TokenSource: &fakeTokenSource{tok: "resolved-tok"}})
	cmd, err := cli.Command(context.Background(), "pr", "view", "1")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Args[0] != "gh" {
		t.Errorf("default binary = %q, want gh", cmd.Args[0])
	}
	if got := ghHostEntries(cmd.Env); len(got) != 0 {
		t.Errorf("zero Options leaked GH_HOST entries %v into the main call", got)
	}
	tok := ghAuthTokenCommand(context.Background(), "", "")
	if tok.Args[0] != "gh" {
		t.Errorf("default token child binary = %q, want gh", tok.Args[0])
	}
	if got := ghHostEntries(tok.Env); len(got) != 0 {
		t.Errorf("zero host leaked GH_HOST entries %v into the token child", got)
	}
}
