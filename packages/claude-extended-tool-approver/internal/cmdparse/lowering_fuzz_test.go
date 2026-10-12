// lowering_fuzz_test.go - tc-o14i5.3.15 (Phase 2m, design item m).
//
// FuzzCmdparseToEffectgraph fuzzes the whole cmdparse -> effectgraph lowering
// (ParseShell, BuildStructural, BuildInterpreted, then the policy fold) and
// asserts, for every input:
//
//  1. No stage panics. effectpolicy.EvaluateGraph converts a panic into an
//     Abstain whose Reason starts "recovered panic", which would MASK a crash
//     from a fuzzer, so the earlier stages are called directly (a panic there
//     fails the run) and the fold's Reason is checked for that prefix.
//  2. Every argv position of a node the fold marked PERMITTED is CLAIMED by the
//     command's spec: each token that looks like a flag is a flag spelling the
//     schema models (exact, "--flag=value", "-flag=value", or a short bundle of
//     modeled letters), or arrives as the value of one; positionals are claimed
//     by the schema's positional layout (an unresolvable layout makes the node
//     insufficient, so a Permitted node has already passed it). A Permitted node
//     carrying an unmodeled flag is an UNCLAIMED POSITION: a finding to fix in
//     the spec/interpreter, not a flake in the test.
//
// The unclaimed-position oracle is applied to leaves whose schema is a plain
// flag-table schema (no Subcommands, VerbFamily, or non-generic Interpreter):
// those interpreters own their argv grammar (find predicates, ssh options, ...)
// and are covered by their own tests. The oracle is a re-statement of the
// scanner's flag grammar (cmddesc/interpreter.go, flag()), deliberately
// re-implemented here rather than called, so a scanner bug cannot vouch for
// itself.
//
// Seeds: every Bash command of the golden corpus
// (internal/goldencorpus/testdata/corpus.json) plus a curated adversarial set,
// and the checked-in files under testdata/fuzz/FuzzCmdparseToEffectgraph.
// `go test` replays them as an ordinary, finite test; the fuzzer is run with
// `go test -run '^$' -fuzz FuzzCmdparseToEffectgraph -fuzztime 60s
// ./internal/cmdparse/`.
package cmdparse_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/pathspec"
)

// loweringFixtureRoot builds a project root under the LITERAL /tmp (not
// $TMPDIR: under `nix build` $TMPDIR sits in /nix/var/nix/builds, which
// ADR 0068's os-nix zone declares Forbidden - see effectpolicy/golden_test.go
// tempDirUnderRealTmp) with a .git marker, and pins HOME so no real user
// config is read. The git probes are faked so the run never spawns git.
func loweringFixtureRoot(f *testing.F) string {
	root, err := os.MkdirTemp("/tmp", "ceta-lowering-fuzz-")
	if err != nil {
		f.Skipf("cannot create fixture dir under /tmp: %v", err)
	}
	f.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o644); err != nil {
		f.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		f.Fatal(err)
	}
	home, err := os.MkdirTemp("/tmp", "ceta-lowering-fuzz-home-")
	if err != nil {
		f.Skipf("cannot create HOME under /tmp: %v", err)
	}
	f.Cleanup(func() { _ = os.RemoveAll(home) })
	f.Setenv("HOME", home)
	f.Cleanup(pathspec.SetWorktreeStateProbe(func(string) (pathspec.WorktreeState, error) {
		return pathspec.WorktreeUnknown, fmt.Errorf("fuzz fixture: no real git")
	}))
	f.Cleanup(pathspec.SetGitTrackedProbe(func(string, string, bool) (bool, error) { return false, nil }))
	return real
}

// loweringCorpusSeeds returns every Bash command of the golden corpus.
func loweringCorpusSeeds(f *testing.F) []string {
	data, err := os.ReadFile("../goldencorpus/testdata/corpus.json")
	if err != nil {
		f.Fatalf("read golden corpus: %v", err)
	}
	var rows []goldencorpus.Row
	if err := json.Unmarshal(data, &rows); err != nil {
		f.Fatalf("decode golden corpus: %v", err)
	}
	var out []string
	for _, r := range rows {
		if r.ToolInput.ToolName != "Bash" {
			continue
		}
		if c, _ := r.ToolInput.ToolInput["command"].(string); c != "" {
			out = append(out, c)
		}
	}
	return out
}

var loweringAdversarialSeeds = []string{
	"git status",
	"ls -la /tmp",
	"cat README.md | grep hi",
	"echo a > out.txt",
	"echo $(cat README.md)",
	"FOO=bar ls",
	"cd /tmp && ls",
	"bash -c 'echo hi'",
	"bash -c 'bash -c \"echo nested\"'",
	"xargs -n1 echo",
	"find . -name '*.go' -print",
	"sed -i s/a/b/ README.md",
	"grep -r --include=*.go foo .",
	"ls --no-such-flag",
	"ls -Z",
	"ls -la --color=always",
	"cat -- -weird-name",
	"wc -l <<EOF\nx\nEOF",
	"{a,b}{1,2}",
	"echo {1..3}",
	"ls $(",
	"ls 'unterminated",
	"echo ${",
	"(((((((((((((((((((((((echo deep)))))))))))))))))))))))",
	"echo \x00",
	"a=1 b=2 c=3",
	"export X=1; ls",
	"env -i PATH=/usr/bin ls",
	"/opt/attacker-controlled/true --anything -x",
	"true",
	"git -C /tmp log --oneline -n5",
	"git commit -m 'x' --amend",
	"",
	" ",
	";",
	"&&",
	"| |",
}

func FuzzCmdparseToEffectgraph(f *testing.F) {
	root := loweringFixtureRoot(f)
	reg := cmddesc.DefaultRegistry()
	policies := effectpolicy.DefaultPolicies()
	graphPolicies := effectpolicy.DefaultGraphPolicies()

	for _, s := range loweringAdversarialSeeds {
		f.Add(s)
	}
	for _, s := range loweringCorpusSeeds(f) {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, command string) {
		sp := cmdparse.ParseShell(command)
		if sp.Unparseable {
			return
		}
		ctx := cmddesc.Context{CWD: root}
		structural := effectgraph.BuildStructural(sp)
		interpreted := effectgraph.BuildInterpreted(sp, reg, ctx)

		req := evalcontract.Request{Command: command, CWD: root, ProjectRoot: root}
		resp := effectpolicy.EvaluateGraph(structural, interpreted, policies, graphPolicies, effectpolicy.BuildPolicyContext(req))
		if strings.HasPrefix(resp.Reason, "recovered panic") {
			t.Fatalf("panic inside the fold for %q: %s", command, resp.Reason)
		}

		g := resp.Interpreted
		for i := range g.Nodes {
			n := &g.Nodes[i]
			if n.Kind != effectgraph.NodeCommand || n.Mark != effectgraph.MarkPermitted || n.Leaf == nil {
				continue
			}
			if bad := unclaimedPosition(reg, *n.Leaf); bad != "" {
				t.Fatalf("command %q: node %s (%s) is PERMITTED but %s", command, n.ID, n.Label, bad)
			}
		}
	})
}

// unclaimedPosition returns a description of the first argv position of leaf
// that its spec does not claim, or "" when every position is claimed (or the
// leaf's schema is outside this oracle's scope - see the file comment).
func unclaimedPosition(reg cmddesc.Registry, leaf cmdparse.ParsedCommand) string {
	if leaf.Executable == "" {
		return ""
	}
	schema, ok := reg.Lookup(path.Base(leaf.Executable))
	if !ok {
		return fmt.Sprintf("its executable %q has no schema", leaf.Executable)
	}
	if len(schema.Subcommands) > 0 || schema.VerbFamily != "" || schema.Interpreter != "" {
		return ""
	}
	if schema.UnknownFlag == cmddesc.UnknownFlagInert {
		return ""
	}
	optionsEnded := false
	args := leaf.Args
	for i := 0; i < len(args); i++ {
		tok := args[i]
		isFlag := !optionsEnded && strings.HasPrefix(tok, "-") && tok != "-" && tok != schema.Positionals.StdinToken
		if !isFlag {
			if schema.PositionalsEndOptions {
				optionsEnded = true
			}
			continue
		}
		if tok == "--" && schema.EndOfOptions {
			optionsEnded = true
			continue
		}
		consumed, ok := claimFlag(schema, tok)
		if !ok {
			return fmt.Sprintf("argv position %d (%q) of %q is a flag the spec does not model", i, tok, leaf.Executable)
		}
		i += consumed
	}
	return ""
}

// claimFlag re-states the scanner's flag grammar: it reports how many FOLLOWING
// args the token consumes and whether the token is claimed at all.
func claimFlag(schema cmddesc.CommandSchema, tok string) (int, bool) {
	arity := func(spec cmddesc.FlagSpec) int {
		switch spec.Arity {
		case cmddesc.ArityOne:
			return 1
		case cmddesc.ArityN:
			return len(spec.Operands)
		}
		return 0
	}
	if spec, ok := schema.Flags[tok]; ok {
		return arity(spec), true
	}
	if eq := strings.IndexByte(tok, '='); eq > 0 {
		if spec, ok := schema.Flags[tok[:eq]]; ok && spec.Arity != cmddesc.ArityNone {
			return 0, true
		}
	}
	if strings.HasPrefix(tok, "--") {
		return 0, false
	}
	rest := tok[1:]
	for pos := 0; pos < len(rest); pos++ {
		spec, ok := schema.Flags["-"+string(rest[pos])]
		if !ok {
			return 0, false
		}
		if spec.Arity == cmddesc.ArityNone {
			continue
		}
		if rest[pos+1:] == "" && spec.Arity == cmddesc.ArityOne {
			return 1, true
		}
		return 0, true
	}
	return 0, true
}

// TestUnclaimedPositionOracle keeps the fuzz oracle honest: it must flag an
// unmodeled flag and a leaf without a schema, and accept modeled spellings, so
// a "0 crashers" fuzz run cannot be vacuous.
func TestUnclaimedPositionOracle(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	leaf := func(exe string, args ...string) cmdparse.ParsedCommand {
		return cmdparse.ParsedCommand{Executable: exe, Args: args}
	}
	for _, c := range []struct {
		name    string
		leaf    cmdparse.ParsedCommand
		flagged bool
	}{
		{"modeled short bundle", leaf("ls", "-la"), false},
		{"modeled long flag", leaf("ls", "--all"), false},
		{"unmodeled flag", leaf("ls", "--definitely-not-a-flag"), true},
		{"unmodeled short in bundle", leaf("ls", "-aQ#"), true},
		{"no schema", leaf("definitely-not-a-command", "x"), true},
		{"operand after --", leaf("cat", "--", "-weird"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := unclaimedPosition(reg, c.leaf)
			if (got != "") != c.flagged {
				t.Fatalf("unclaimedPosition = %q, want flagged=%v", got, c.flagged)
			}
		})
	}
}
