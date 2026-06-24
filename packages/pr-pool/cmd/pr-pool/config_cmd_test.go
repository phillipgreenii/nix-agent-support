package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

func TestRenderConfigShow_includesDispatchScalars(t *testing.T) {
	cfg := config.Default()
	cfg.ConfigPath = "/repo/.pr-pool/config.toml"
	var b bytes.Buffer
	renderConfigShow(&b, cfg)
	out := b.String()

	// existing surface preserved
	for _, want := range []string{"config path: /repo/.pr-pool/config.toml", "roles ("} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// dispatch scalars (the audit payoff) — no worker launched
	for _, want := range []string{
		"permission-mode", "dontAsk",
		"allowed-tools", cfg.AllowedTools, // verbatim
		"autonomous", "true",
		"confirm-ingest",
		"budget",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing dispatch scalar %q in:\n%s", want, out)
		}
	}
	// audit: 'git push' must be absent from the printed allowlist
	if strings.Contains(out, "git push") {
		t.Errorf("allowlist must not contain 'git push'; got:\n%s", out)
	}
}
