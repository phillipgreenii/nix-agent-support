package claudecodeadapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// tempDirUnderRealTmp builds a fixture root under the LITERAL "/tmp" path
// rather than t.TempDir() (which resolves against the ambient $TMPDIR).
// tc-fpbpp: under `nix build`, $TMPDIR (and therefore every t.TempDir())
// IS the build's own NIX_BUILD_TOP, which on this host lands under
// /nix/var/nix/builds/<id> — and ADR 0068's osNixKind (internal/pathspec/
// osspec.go) declares the WHOLE /nix tree Write:Forbidden/Delete:Forbidden,
// a faithful port of production patheval.classify()'s own "/nix/**" rule.
// A fixture built there is misclassified as living in a forbidden zone —
// a nix-sandbox TMPDIR-placement artifact, not a defect in the policy under
// test (see internal/patheval/escape_zone_ladder_test.go's near-identical
// pg2-lw19e writeup, which root-caused the exact same TMPDIR-under-/nix
// mechanism for a different test). That file's "tmp-root" subtest already
// proved the fix used here: literal /tmp (bypassing $TMPDIR entirely) stays
// outside NIX_BUILD_TOP even inside this same sandbox, and osTmpKind (the
// pathspec port of classify()'s own /tmp rule) classifies all of /tmp as
// full ReadWrite — so a fixture built here reaches the SAME verdict inside
// or outside a nix sandbox, unlike one built via t.TempDir()/$TMPDIR.
func tempDirUnderRealTmp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ceta-adapter-fixture-")
	if err != nil {
		t.Skipf("cannot create fixture dir under /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fixture builds a throwaway project root: a plain `.git` directory (the
// same marker internal/effectpolicy/golden_test.go's own fixture uses — a
// directory suffices for patheval's project-root detection and zone
// classification, no real git process involved), a tracked-looking ordinary
// file (README.md), and a WellKnownSecret file (.env, per
// internal/secretpath's purely lexical classification — matching name only,
// no git-tracked state needed). Neither NoWriteToReadOnlyPath nor
// NoWriteToSecretPath's AccessTruncate/AccessModify branches ever call out
// to git (see this file's own package doc for why no hermetic git-stripped
// helper is needed here: the AccessModify secret-write carve-out that WOULD
// call deletable.NonSecret is never reached by any case in this file), so a
// plain directory is sufficient — this mirrors golden_test.go's own fixture
// exactly for the same reason.
func fixture(t *testing.T) string {
	t.Helper()
	root := tempDirUnderRealTmp(t)
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("S=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// writeInput builds a Write HookInput naming path (absolute, matching the
// real Claude Code hook's own shape: tool_input.file_path is always
// absolute).
func writeInput(t *testing.T, cwd, path string) *hookio.HookInput {
	t.Helper()
	raw, err := json.Marshal(hookio.FileToolInput{FilePath: path, Content: "new content\n"})
	if err != nil {
		t.Fatal(err)
	}
	return &hookio.HookInput{CWD: cwd, ToolName: "Write", ToolInput: raw}
}

// editInput builds an Edit HookInput naming path.
func editInput(t *testing.T, cwd, path string) *hookio.HookInput {
	t.Helper()
	raw, err := json.Marshal(hookio.FileToolInput{FilePath: path, OldString: "hi", NewString: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	return &hookio.HookInput{CWD: cwd, ToolName: "Edit", ToolInput: raw}
}

// multiEditInput builds a MultiEdit HookInput naming path. MultiEdit's real
// tool_input carries an "edits" array, not old_string/new_string directly,
// but hookio.HookInput.FilePath (this adapter's only reader of the payload)
// reads file_path exactly the same way for MultiEdit as for Edit, so a bare
// file_path is all this adapter needs to see for MultiEdit too.
func multiEditInput(t *testing.T, cwd, path string) *hookio.HookInput {
	t.Helper()
	raw, err := json.Marshal(hookio.FileToolInput{FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	return &hookio.HookInput{CWD: cwd, ToolName: "MultiEdit", ToolInput: raw}
}

// TestWrite_SecretPath_RejectsLikeShellEcho is the bead's own minimum
// requirement: a Write to a secret path Rejects, exactly as
// internal/effectpolicy/golden_test.go's "echo_redirect_dotenv" case
// (`echo x > .env`, evalcontract.Reject) does for the equivalent shell
// redirect against the identical fixture path — proving the one-node graph
// this adapter builds is judged by the SAME policy (NoWriteToSecretPath)
// that judges the shell command's graph, not a second implementation.
func TestWrite_SecretPath_RejectsLikeShellEcho(t *testing.T) {
	root := fixture(t)
	dotenv := filepath.Join(root, ".env")

	resp, err := Evaluate(writeInput(t, root, dotenv), RequestConfig{ProjectRoot: root}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != evalcontract.Reject {
		t.Fatalf("decision = %s, want reject (matching golden echo_redirect_dotenv) — reason: %s", resp.Decision, resp.Reason)
	}
}

// TestWrite_OrdinaryPath_ApprovesLikeShellEcho: a Write to an ordinary,
// non-secret path Approves, exactly as golden_test.go's
// "echo_redirect_readme" case (`echo x > README.md`, evalcontract.Approve)
// does against the identical fixture path.
func TestWrite_OrdinaryPath_ApprovesLikeShellEcho(t *testing.T) {
	root := fixture(t)
	readme := filepath.Join(root, "README.md")

	resp, err := Evaluate(writeInput(t, root, readme), RequestConfig{ProjectRoot: root}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != evalcontract.Approve {
		t.Fatalf("decision = %s, want approve (matching golden echo_redirect_readme) — reason: %s", resp.Decision, resp.Reason)
	}
}

// TestEdit_OrdinaryPath_ApprovesLikeShellSedInPlace: an Edit to an ordinary
// tracked path Approves, exactly as golden_test.go's "sed_inplace_readme"
// case (`sed -i 's/a/b/' README.md`, evalcontract.Approve) does — both are
// AccessModify against the same file.
func TestEdit_OrdinaryPath_ApprovesLikeShellSedInPlace(t *testing.T) {
	root := fixture(t)
	readme := filepath.Join(root, "README.md")

	resp, err := Evaluate(editInput(t, root, readme), RequestConfig{ProjectRoot: root}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != evalcontract.Approve {
		t.Fatalf("decision = %s, want approve (matching golden sed_inplace_readme) — reason: %s", resp.Decision, resp.Reason)
	}
}

// TestMultiEdit_OrdinaryPath_Approves: MultiEdit is judged identically to
// Edit (same AccessModify mapping — see fileToolAccess's own doc comment).
func TestMultiEdit_OrdinaryPath_Approves(t *testing.T) {
	root := fixture(t)
	readme := filepath.Join(root, "README.md")

	resp, err := Evaluate(multiEditInput(t, root, readme), RequestConfig{ProjectRoot: root}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != evalcontract.Approve {
		t.Fatalf("decision = %s, want approve — reason: %s", resp.Decision, resp.Reason)
	}
}

// TestBuildFileToolGraph_IsOneNode pins the bead's own framing literally: a
// Write/Edit tool call becomes exactly ONE command node (plus the one file
// node it writes to), carrying exactly one effect — not a multi-node graph,
// and not zero nodes either.
func TestBuildFileToolGraph_IsOneNode(t *testing.T) {
	root := fixture(t)
	readme := filepath.Join(root, "README.md")

	structural, interpreted, err := BuildFileToolGraph(writeInput(t, root, readme))
	if err != nil {
		t.Fatal(err)
	}
	for name, g := range map[string]effectgraph.Graph{"structural": structural, "interpreted": interpreted} {
		commandNodes := 0
		for _, n := range g.Nodes {
			if n.Kind == effectgraph.NodeCommand {
				commandNodes++
				if len(n.Effects) != 1 {
					t.Errorf("%s: command node has %d effects, want 1", name, len(n.Effects))
				}
				if len(n.Effects) == 1 && n.Effects[0].Kind != cmddesc.EffectPath {
					t.Errorf("%s: effect kind = %s, want path", name, n.Effects[0].Kind)
				}
			}
		}
		if commandNodes != 1 {
			t.Errorf("%s: %d command nodes, want exactly 1 (one-node graph)", name, commandNodes)
		}
	}
}

// TestBuildFileToolGraph_AccessMapping pins fileToolAccess's own mapping
// directly, so a future change to it is a visible, deliberate diff here.
func TestBuildFileToolGraph_AccessMapping(t *testing.T) {
	root := fixture(t)
	readme := filepath.Join(root, "README.md")

	cases := []struct {
		name  string
		input *hookio.HookInput
		want  cmddesc.PathAccess
	}{
		{"write", writeInput(t, root, readme), cmddesc.AccessTruncate},
		{"edit", editInput(t, root, readme), cmddesc.AccessModify},
		{"multiedit", multiEditInput(t, root, readme), cmddesc.AccessModify},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, interpreted, err := BuildFileToolGraph(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			got := interpreted.Nodes[0].Effects[0].Access
			if got != tc.want {
				t.Errorf("access = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestBuildFileToolGraph_UnsupportedTool: a tool this adapter does not
// govern (Read, Delete, or anything else) errors rather than silently
// producing a graph — BuildFileToolGraph is not meant to be called for a
// tool IsFileEditTool reports false for, and this pins that it fails
// loudly if it is.
func TestBuildFileToolGraph_UnsupportedTool(t *testing.T) {
	root := fixture(t)
	raw, err := json.Marshal(hookio.FileToolInput{FilePath: filepath.Join(root, "README.md")})
	if err != nil {
		t.Fatal(err)
	}
	input := &hookio.HookInput{CWD: root, ToolName: "Read", ToolInput: raw}
	if IsFileEditTool(input.ToolName) {
		t.Fatal("Read must not be a recognised file-edit tool")
	}
	if _, _, err := BuildFileToolGraph(input); err == nil {
		t.Fatal("expected an error for an unsupported tool")
	}
}

// TestEvaluate_Bash_RoutesThroughShellPath: a Bash HookInput still goes
// through the ordinary shell-command path unchanged, proving this adapter's
// Write/Edit special-casing does not disturb Bash routing.
func TestEvaluate_Bash_RoutesThroughShellPath(t *testing.T) {
	root := fixture(t)
	raw, err := json.Marshal(hookio.BashToolInput{Command: "cat README.md"})
	if err != nil {
		t.Fatal(err)
	}
	input := &hookio.HookInput{CWD: root, ToolName: "Bash", ToolInput: raw}

	resp, err := Evaluate(input, RequestConfig{ProjectRoot: root}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != evalcontract.Approve {
		t.Fatalf("decision = %s, want approve — reason: %s", resp.Decision, resp.Reason)
	}
}

// TestEvaluate_UnsupportedTool_Errors: a tool neither Bash nor a file-edit
// tool (e.g. Glob) is not modeled by this adapter and must error, not
// silently approve or abstain.
func TestEvaluate_UnsupportedTool_Errors(t *testing.T) {
	root := fixture(t)
	raw, err := json.Marshal(hookio.SearchToolInput{Pattern: "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	input := &hookio.HookInput{CWD: root, ToolName: "Glob", ToolInput: raw}
	if _, err := Evaluate(input, RequestConfig{ProjectRoot: root}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies()); err == nil {
		t.Fatal("expected an error for a tool this adapter does not model")
	}
}
