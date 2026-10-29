package effectpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
)

// probeSpecJSON is a consumer command spec (pg2-7ekzoi): two inert literal
// value flags, no positionals, an unmodeled flag is insufficient.
const probeSpecJSON = `{
 "version": "v1", "kind": "command", "name": "example-probe",
 "command": {
  "name": "example-probe",
  "provenance": "example-probe --help (test fixture)",
  "citations": {
   "provenance": {"source": "test fixture"},
   "stdin": {"source": "test fixture"},
   "stdout": {"source": "test fixture"},
   "unknownFlag": {"source": "test fixture"}
  },
  "flags": {
   "--pr": {"arity": "one", "operand": {"kind": "literal"}, "transform": {"kind": "none"}, "citation": {"source": "test fixture"}},
   "--worktree": {"arity": "one", "operand": {"kind": "literal"}, "transform": {"kind": "none"}, "citation": {"source": "test fixture"}}
  },
  "positionals": {"rest": {"kind": "unmodeled"}, "restOverride": {"role": {"kind": "literal"}}, "citation": {"source": "test fixture"}},
  "stdin": "never", "stdout": "metadata", "unknownFlag": "insufficient",
  "endOfOptions": true
 }
}`

// TestLiveRegistryUserLayerSpec drives the real effect engine with a registry
// assembled from a HOME-resolved user dir whose spec is a symlink to a file
// outside it (the Home Manager shape: a symlink into /nix/store).
func TestLiveRegistryUserLayerSpec(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "decoy")) // P7: must be ignored
	userDir, err := specfmt.DefaultUserDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(userDir, home) {
		t.Fatalf("DefaultUserDir %q is not HOME-resolved (%q)", userDir, home)
	}
	store := t.TempDir() // stands in for /nix/store
	target := filepath.Join(store, "hash-example-probe.json")
	if err := os.WriteFile(target, []byte(probeSpecJSON), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(userDir, "specs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(userDir, "specs", "example-probe.json")); err != nil {
		t.Fatal(err)
	}

	builtin := cmddesc.DefaultRegistry()
	live, set, skipped, err := specfmt.LoadLiveRegistry(builtin, userDir, "")
	if err != nil {
		t.Fatalf("LoadLiveRegistry: %v", err)
	}
	if len(skipped) != 0 || len(set.Invalid) != 0 {
		t.Fatalf("unexpected skipped=%v invalid=%v", skipped, set.Invalid)
	}

	root := t.TempDir()
	rows := []struct {
		cmd  string
		live evalcontract.Decision
	}{
		{"example-probe --pr=12", evalcontract.Approve},
		{"example-probe --pr 12 --worktree " + root, evalcontract.Approve},
		{"example-probe --unmodeled", evalcontract.Abstain},
		{"example-probe stray", evalcontract.Abstain},
	}
	for _, r := range rows {
		req := evalcontract.Request{Command: r.cmd, CWD: root, ProjectRoot: root}
		got := Evaluate(req, live, DefaultPolicies(), DefaultGraphPolicies()).Decision
		if got != r.live {
			t.Errorf("live registry: %q = %v, want %v", r.cmd, got, r.live)
		}
		if b := Evaluate(req, builtin, DefaultPolicies(), DefaultGraphPolicies()).Decision; b != evalcontract.Abstain {
			t.Errorf("builtin-only registry: %q = %v, want Abstain", r.cmd, b)
		}
	}
}
