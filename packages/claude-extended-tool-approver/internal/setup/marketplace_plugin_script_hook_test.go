package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// TestLiveHook_MarketplacePlanDecomposeScripts pins pg2-zp3fl through the REAL
// factory-built engine, i.e. the decision path the live PreToolUse hook takes
// (main.go handlePreToolUse -> NewEngineForCWD -> EvaluateHook).
//
// THE DEFECT. ADR 0099 (pg2-yr90y) taught only the effect engine
// (patheval.ResolveTrustedExecutable) to trust marketplace plugin scripts, and
// the effect engine decides `evaluate --corpus` replays but NOT the live hook,
// which still runs this package's RuleChain until production cutover. So the
// corpus rows were correct while the applied hook abstained on the identical
// command (measured 2026-10-15 on the applied binary). The effect-engine
// tests (internal/effectpolicy TestMarketplacePluginScriptAbsolutePath) can
// never catch that; this test drives the live chain over a hermetic copy of
// the real install layout (a temp HOME whose pgii-marketplaces entry is a
// symlink into a fake "store" directory named <hash>-claude-marketplace-<x>).
func TestLiveHook_MarketplacePlanDecomposeScripts(t *testing.T) {
	withXDGConfig(t, "")

	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	storeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(patheval.SetMarketplaceStoreRootForTest(storeRoot))

	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"body.md", "acc.md", "design.md", "draft.md"} {
		if err := os.WriteFile(filepath.Join(project, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	body := filepath.Join(project, "body.md")
	acc := filepath.Join(project, "acc.md")
	design := filepath.Join(project, "design.md")
	draft := filepath.Join(project, "draft.md")
	out := filepath.Join(project, "out.md")

	scripts := []string{"create-packet.sh", "assemble-packet.sh", "update-packet.sh", "chunk-for-bd-field.sh"}
	mkScripts := func(dir string) {
		for _, s := range scripts {
			p := filepath.Join(dir, s)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}

	storeDir := filepath.Join(storeRoot, "0123456789abcdfghijklmnpqrsvwxyz-claude-marketplace-fake")
	mkScripts(filepath.Join(storeDir, "plan-decompose", "scripts"))
	installRoot := filepath.Join(home, ".local", "share", "pgii-marketplaces")
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(installRoot, "fake-marketplace-local")
	if err := os.Symlink(storeDir, installed); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	trusted := filepath.Join(installed, "plan-decompose", "scripts")

	// A real directory planted under the install root (not a store symlink).
	planted := filepath.Join(installRoot, "planted-marketplace-local", "plan-decompose", "scripts")
	mkScripts(planted)
	// An absolute directory outside the install holding same-named scripts.
	elsewhere := filepath.Join(t.TempDir(), "plan-decompose", "scripts")
	mkScripts(elsewhere)

	createArgs := ` --parent pg2-epic1 --title "title" --body-file ` + body +
		` --acceptance "criteria" --metadata '{}' --actor sess-1`
	assembleArgs := ` --design ` + design + ` --in ` + draft + ` --out ` + out
	updateArgs := ` --id pg2-pk1 --body-file ` + body + ` --acceptance-file ` + acc + ` --actor sess-1`

	tests := []struct {
		name    string
		command string
		approve bool
	}{
		// The acceptance criterion: the absolute spelling approves.
		{"create-packet absolute", trusted + "/create-packet.sh" + createArgs, true},
		{"create-packet with --acceptance-file", trusted + "/create-packet.sh --parent pg2-epic1 --title t --body-file " + body + " --acceptance-file " + acc + " --label docket --actor sess-1", true},
		{"assemble-packet absolute", trusted + "/assemble-packet.sh" + assembleArgs, true},
		{"update-packet absolute", trusted + "/update-packet.sh" + updateArgs, true},
		{"quoted absolute argv0", `"` + trusted + `/update-packet.sh"` + updateArgs, true},

		// The variable spelling must NOT approve (ADR 0099 item 3): the shell
		// variable is unverifiable text, even when it holds the trusted path.
		{"variable argv0 after assignment", "S=" + trusted + "; $S/create-packet.sh" + createArgs, false},
		{"variable argv0 quoted", "S=" + trusted + `; "$S"/create-packet.sh` + createArgs, false},
		{"variable argv0 and-chained", "S=" + trusted + ` && "$S"/update-packet.sh` + updateArgs, false},

		// Identity negatives: same basename, not the marketplace install.
		{"non-marketplace absolute", elsewhere + "/create-packet.sh" + createArgs, false},
		{"planted real directory under the install root", planted + "/create-packet.sh" + createArgs, false},
		{"dotdot lookalike", trusted + "/../../../../../" + strings.TrimPrefix(elsewhere, "/") + "/create-packet.sh" + createArgs, false},
		{"bare basename from PATH", "create-packet.sh" + createArgs, false},
		{"missing script in the install", trusted + "/nonexistent.sh" + createArgs, false},
		// A script this rule does not model stays out of scope.
		{"unmodeled script in the same directory", trusted + "/chunk-for-bd-field.sh " + body + " " + out, false},

		// Argument negatives for a trusted argv0.
		{"body-file outside any readable zone", trusted + "/create-packet.sh --parent pg2-epic1 --title t --body-file /etc/shadow --acceptance c --actor s", false},
		{"body-file is a credential store", trusted + "/update-packet.sh --id pg2-pk1 --body-file " + home + "/.ssh/id_rsa --actor s", false},
		{"body-file dynamically expanded", trusted + `/create-packet.sh --parent pg2-epic1 --title t --body-file "$B" --acceptance c --actor s`, false},
		{"metadata @file reads a file", trusted + "/create-packet.sh --parent pg2-epic1 --title t --body-file " + body + " --acceptance c --metadata @/etc/shadow --actor s", false},
		{"assemble --out outside any writable zone", trusted + "/assemble-packet.sh --design " + design + " --in " + draft + " --out /etc/out.md", false},
		{"unknown flag", trusted + "/create-packet.sh" + createArgs + " --force", false},
		{"glued flag=value spelling", trusted + "/update-packet.sh --id=pg2-pk1 --body-file " + body, false},
		{"flag missing its value", trusted + "/update-packet.sh --id pg2-pk1 --body-file", false},
		{"positional argument", trusted + "/update-packet.sh" + updateArgs + " extra", false},
		{"command substitution in a value", trusted + "/update-packet.sh --id $(rm -rf /) --body-file " + body, false},
	}

	eng := NewEngineForCWD(project)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eng.EvaluateHook(bashHook(project, tt.command))
			if tt.approve && got.Decision != hookio.Approve {
				t.Errorf("cmd %q\n  got %s (%s: %s), want approve", tt.command, got.Decision, got.Module, got.Reason)
			}
			// A negative row may abstain, ask (unattended sessions turn that
			// into a deny) or reject; the contract is only "never allow".
			if !tt.approve && got.Decision == hookio.Approve {
				t.Errorf("cmd %q\n  got approve (%s: %s), want anything but approve", tt.command, got.Module, got.Reason)
			}
		})
	}
}
