package effectpolicy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// TestMarketplacePluginScriptAbsolutePath pins pg2-yr90y end to end through
// the real effect engine: the plan-decompose packet scripts invoked by their
// ABSOLUTE installed marketplace path approve (operator ruling pg2-cjfpy:
// every command a plugin skill instructs is approved; docs/adr/0099), while
// the same command spelled any other way stays Abstain.
//
// The install is a hermetic copy of the real layout: a fake store directory
// under a temp root (the store root is redirected with
// patheval.SetMarketplaceStoreRootForTest), a temp HOME, and a symlink
// <HOME>/.local/share/pgii-marketplaces/<marketplace> -> <store>/<hash>-claude-marketplace-<name>.
func TestMarketplacePluginScriptAbsolutePath(t *testing.T) {
	root, _ := fixture(t)

	home := tempDirUnderRealTmp(t)
	t.Setenv("HOME", home)
	storeRoot := tempDirUnderRealTmp(t)
	t.Cleanup(patheval.SetMarketplaceStoreRootForTest(storeRoot))

	storeDir := filepath.Join(storeRoot, "0123456789abcdfghijklmnpqrsvwxyz-claude-marketplace-fake")
	scriptsDir := filepath.Join(storeDir, "plan-decompose", "scripts")
	for _, s := range []string{"create-packet.sh", "assemble-packet.sh", "update-packet.sh"} {
		writeExecutable(t, filepath.Join(scriptsDir, s))
	}
	installRoot := filepath.Join(home, ".local", "share", "pgii-marketplaces")
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(installRoot, "fake-marketplace-local")
	if err := os.Symlink(storeDir, installed); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	installedScripts := filepath.Join(installed, "plan-decompose", "scripts")

	// A non-marketplace absolute directory holding same-named scripts.
	elsewhere := tempDirUnderRealTmp(t)
	for _, s := range []string{"create-packet.sh", "assemble-packet.sh", "update-packet.sh"} {
		writeExecutable(t, filepath.Join(elsewhere, s))
	}
	// A real directory planted under the install root (not a store symlink).
	planted := filepath.Join(installRoot, "planted-marketplace-local", "plan-decompose", "scripts")
	for _, s := range []string{"create-packet.sh", "assemble-packet.sh", "update-packet.sh"} {
		writeExecutable(t, filepath.Join(planted, s))
	}
	// A path that only LOOKS like the marketplace prefix: ".." climbs out of
	// the install and lands in `elsewhere`.
	rel, err := filepath.Rel(installedScripts, elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	dotdot := installedScripts + "/" + rel
	// A symlink inside the install whose target is outside the store.
	if err := os.Symlink(elsewhere, filepath.Join(installRoot, "escape-marketplace-local")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	viaEscape := filepath.Join(installRoot, "escape-marketplace-local")

	args := map[string]string{
		"create-packet.sh":   `--parent pg2-epic1 --title "title" --body-file /tmp/body.md --acceptance "criteria" --metadata '{}' --actor sess-1`,
		"assemble-packet.sh": `--design /tmp/design.md --in /tmp/draft.md --out /tmp/body.md`,
		"update-packet.sh":   `--id pg2-pk1 --body-file /tmp/body.md --acceptance-file /tmp/acc.md --actor sess-1`,
	}
	reg := cmddesc.DefaultRegistry()
	eval := func(command string) evalcontract.Response {
		return Evaluate(evalcontract.Request{Command: command, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
	}

	for script, a := range args {
		t.Run("approve/"+script, func(t *testing.T) {
			cmd := filepath.Join(installedScripts, script) + " " + a
			if resp := eval(cmd); resp.Decision != evalcontract.Approve {
				t.Errorf("%s\n  decision = %s, want approve; reason: %s", cmd, resp.Decision, resp.Reason)
			}
		})
		for name, dir := range map[string]string{
			"non-marketplace-absolute":      elsewhere,
			"planted-real-directory":        planted,
			"dotdot-looks-like-marketplace": "",
			"symlink-out-of-store":          viaEscape,
		} {
			t.Run("abstain/"+name+"/"+script, func(t *testing.T) {
				cmd := filepath.Join(dir, script)
				if name == "dotdot-looks-like-marketplace" {
					cmd = dotdot + "/" + script
				}
				cmd += " " + a
				if resp := eval(cmd); resp.Decision != evalcontract.Abstain {
					t.Errorf("%s\n  decision = %s, want abstain; reason: %s", cmd, resp.Decision, resp.Reason)
				}
			})
		}
	}

	// A shell variable argv0 is unverifiable, so it stays Abstain even when it
	// holds the trusted path (the skill text therefore requires a literal path).
	t.Run("abstain/variable-argv0", func(t *testing.T) {
		cmd := `S=` + installedScripts + ` && "$S"/create-packet.sh ` + args["create-packet.sh"]
		if resp := eval(cmd); resp.Decision != evalcontract.Abstain {
			t.Errorf("%s\n  decision = %s, want abstain; reason: %s", cmd, resp.Decision, resp.Reason)
		}
	})
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
