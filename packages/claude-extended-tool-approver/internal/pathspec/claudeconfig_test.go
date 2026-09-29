package pathspec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/userconfig"
)

// claudeConfigProjectRoot builds a throwaway project root (a plain `.git`
// directory is enough — the same convention repoFixture uses) suitable for
// exercising claudeConfigGitKind/claudeConfigHomeKind's Write-facet
// classification, and clears the env vars that could otherwise widen a
// zone underneath it.
func claudeConfigProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"WORKSPACE_ROOT", "CETA_EXTRA_READWRITE_ROOTS", "CETA_EXTRA_READONLY_ROOTS", "CETA_DENIED_ROOTS", "MONOREPO_ROOT"} {
		t.Setenv(v, "")
	}
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// writeVerdictOf resolves path against root (project root) and home,
// returning the Write facet's AccessResult ceta's own effect-policy write
// ladder would see (judgeWrite, internal/effectpolicy/policy.go: a
// pathspec Write==Forbidden Rejects; anything else falls to the ordinary
// zone — this test only asserts pathspec's OWN classification, not the
// full effectpolicy wiring, per this packet's Files/Validation scope).
func writeVerdictOf(t *testing.T, root, home, path string) (AccessResult, string) {
	t.Helper()
	t.Setenv("HOME", home)
	pe := patheval.NewWithCWD(root, root)
	abs := ResolveComponentwise(pe, path)
	if abs == "" {
		t.Fatalf("path %q did not resolve", path)
	}
	kinds := EffectiveKinds(pe)
	v := ResolveAccess(kinds, abs).Write
	return v.Result, v.Reason
}

// TestClaudeConfigGoldenR7 runs every "r7"-tagged row in the checked-in
// golden corpus (internal/goldencorpus/testdata/corpus.json) against this
// package's own Write-facet classification, per P2's contract: a "reject"
// row must resolve Forbidden; a "not-approve" row must NOT resolve
// Permitted (Forbidden or Unknown are both acceptable — P2's own "abstain
// or reject both acceptable" for not-approve rows).
func TestClaudeConfigGoldenR7(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "goldencorpus", "testdata", "corpus.json"))
	if err != nil {
		t.Fatalf("reading corpus.json: %v", err)
	}
	var rows []goldencorpus.Row
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("unmarshalling corpus.json: %v", err)
	}

	var r7Rows int
	root := claudeConfigProjectRoot(t)
	home := t.TempDir()
	homeResolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range rows {
		hasR7 := false
		for _, tag := range row.Tags {
			if tag == "r7" {
				hasR7 = true
				break
			}
		}
		if !hasR7 {
			continue
		}
		r7Rows++
		t.Run(row.Case, func(t *testing.T) {
			filePath, _ := row.ToolInput.ToolInput["file_path"].(string)
			if filePath == "" {
				t.Fatalf("case %q: tool_input.file_path is empty", row.Case)
			}
			result, reason := writeVerdictOf(t, root, homeResolved, filePath)
			switch row.ExpectedVerdict {
			case goldencorpus.Reject:
				if result != Forbidden {
					t.Errorf("case %q (%s): Write = %s (%q), want Forbidden", row.Case, filePath, result, reason)
				}
			case goldencorpus.NotApprove:
				if result == Permitted {
					t.Errorf("case %q (%s): Write = Permitted (%q), want Forbidden or Unknown (not-approve)", row.Case, filePath, reason)
				}
			default:
				t.Fatalf("case %q: unexpected expected_verdict %q for an r7-tagged row", row.Case, row.ExpectedVerdict)
			}
		})
	}
	if r7Rows == 0 {
		t.Fatal("no r7-tagged rows found in corpus.json — this test would vacuously pass")
	}
	t.Logf("exercised %d r7-tagged golden-corpus rows", r7Rows)
}

// TestAsksDBReject: any write to an asks.db* path (ceta's own sqlite
// database, under $XDG_DATA_HOME/claude-extended-tool-approver or the
// $HOME/.local/share default) ⇒ Reject, unconditionally — no carve-out
// (osspec.go's osAsksDBKind).
func TestAsksDBReject(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".local", "share", "claude-extended-tool-approver")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"asks.db", "asks.db-wal", "asks.db-shm", "asks.db-journal"} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := claudeConfigProjectRoot(t)
	homeResolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", "")

	for _, name := range []string{"asks.db", "asks.db-wal", "asks.db-shm", "asks.db-journal"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(homeResolved, ".local", "share", "claude-extended-tool-approver", name)
			result, reason := writeVerdictOf(t, root, homeResolved, path)
			if result != Forbidden {
				t.Errorf("Write(%s) = %s (%q), want Forbidden", name, result, reason)
			}
		})
	}

	// The rest of the SAME directory (not an asks.db* sidecar) is untouched
	// by this Kind — osXDGDataKind's own blanket ReadWrite still governs it.
	t.Run("an unrelated file in the same data directory is unaffected", func(t *testing.T) {
		other := filepath.Join(dataDir, "unrelated.txt")
		if err := os.WriteFile(other, []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		result, reason := writeVerdictOf(t, root, homeResolved, other)
		if result == Forbidden {
			t.Errorf("Write(unrelated.txt) = %s (%q), want NOT Forbidden", result, reason)
		}
	})
}

// TestHookRouterConfigReject: a write to router-config.json (claude-hook-
// router's own config, wherever it is named) ⇒ Reject.
func TestHookRouterConfigReject(t *testing.T) {
	root := claudeConfigProjectRoot(t)
	home := t.TempDir()
	homeResolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	result, reason := writeVerdictOf(t, root, homeResolved, "some/plugin/dir/router-config.json")
	if result != Forbidden {
		t.Errorf("Write(router-config.json) = %s (%q), want Forbidden", result, reason)
	}
}

// TestClaudePluginsReject: a non-plugin-manager write anywhere under
// ~/.claude/plugins/** ⇒ Reject, unconditionally.
func TestClaudePluginsReject(t *testing.T) {
	root := claudeConfigProjectRoot(t)
	home := t.TempDir()
	homeResolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"~/.claude/plugins",
		"~/.claude/plugins/marketplace.json",
		"~/.claude/plugins/some-plugin/hooks/hook.sh",
	} {
		t.Run(path, func(t *testing.T) {
			result, reason := writeVerdictOf(t, root, homeResolved, path)
			if result != Forbidden {
				t.Errorf("Write(%s) = %s (%q), want Forbidden", path, result, reason)
			}
		})
	}
}

// TestP18AgentWritableConfig: an agent write to a ceta config file in
// EITHER P17 layer (user-level config dir, repo `.ceta/`) ⇒ Reject by
// default; the SAME write ⇒ judged by normal path policy once that
// file/glob is named in a human-set agentWritableConfig setting; a write to
// the agentWritableConfig setting FILE ITSELF is Reject unless
// agentWritableConfig also allowlists that file (P18's self-referential
// rule).
func TestP18AgentWritableConfig(t *testing.T) {
	root := claudeConfigProjectRoot(t)
	home := t.TempDir()
	homeResolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}

	repoLayerFile := filepath.Join(root, ".ceta", "some-spec.json")
	if err := os.MkdirAll(filepath.Dir(repoLayerFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repoLayerFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// userconfig.DefaultPath() resolves via os.UserHomeDir(), so it must be
	// computed under THIS test's fake $HOME, not whatever ambient HOME the
	// test binary started with.
	t.Setenv("HOME", homeResolved)
	userConfigPath, err := userconfig.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("repo .ceta/ layer defaults to Reject", func(t *testing.T) {
		result, reason := writeVerdictOf(t, root, homeResolved, repoLayerFile)
		if result != Forbidden {
			t.Errorf("Write(%s) = %s (%q), want Forbidden", repoLayerFile, result, reason)
		}
	})

	t.Run("agentWritableConfig self-referential file defaults to Reject", func(t *testing.T) {
		// userConfigPath is resolved from THIS test's $HOME (set by
		// writeVerdictOf's t.Setenv), so no config file exists yet at that
		// path — cetaProtectedConfigWrite must still treat the LOCATION as
		// Reject-by-default even though userconfig.LoadDefault() reads an
		// empty *Config from a missing file.
		result, reason := writeVerdictOf(t, root, homeResolved, userConfigPath)
		if result != Forbidden {
			t.Errorf("Write(%s) = %s (%q), want Forbidden", userConfigPath, result, reason)
		}
	})

	t.Run("allowlisted repo .ceta/ file defers to normal path policy", func(t *testing.T) {
		t.Setenv("HOME", homeResolved)
		if err := os.MkdirAll(filepath.Dir(userConfigPath), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := userconfig.Config{AgentWritableConfig: []string{repoLayerFile}}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(userConfigPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		result, reason := writeVerdictOf(t, root, homeResolved, repoLayerFile)
		if result == Forbidden {
			t.Errorf("Write(%s) = Forbidden (%q), want NOT Forbidden once allowlisted", repoLayerFile, reason)
		}
	})

	t.Run("self-referential allowlist: naming the config file itself lifts its own Reject", func(t *testing.T) {
		t.Setenv("HOME", homeResolved)
		if err := os.MkdirAll(filepath.Dir(userConfigPath), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := userconfig.Config{AgentWritableConfig: []string{userConfigPath}}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(userConfigPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		result, reason := writeVerdictOf(t, root, homeResolved, userConfigPath)
		if result == Forbidden {
			t.Errorf("Write(%s) = Forbidden (%q), want NOT Forbidden once it allowlists itself", userConfigPath, reason)
		}
	})
}
