package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// config --show prints both gate paths, and each one's "paused since" mtime
// when set, or an explicit not-paused state when absent.
func TestRenderConfigShow_gatesPathsStateMtime(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.QuotaPaused = filepath.Join(dir, "quota-paused")
	cfg.CICDDown = filepath.Join(dir, "cicd-down")
	if err := os.WriteFile(cfg.QuotaPaused, []byte("paused\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var b bytes.Buffer
	renderConfigShow(&b, cfg)
	out := b.String()

	for _, want := range []string{cfg.QuotaPaused, cfg.CICDDown, "paused since", "not paused"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The mtime string itself must be present (RFC3339), not just the label.
	fi, err := os.Stat(cfg.QuotaPaused)
	if err != nil {
		t.Fatal(err)
	}
	if want := fi.ModTime().Format(time.RFC3339); !strings.Contains(out, want) {
		t.Errorf("missing mtime %q in:\n%s", want, out)
	}
}
