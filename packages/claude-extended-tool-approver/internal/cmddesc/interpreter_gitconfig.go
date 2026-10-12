package cmddesc

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// gitConfigInterpreter is `git config`'s own interpretation (P16, packet
// tc-o14i5.3.9). The generic scan collects flags and defers operands; this
// interpreter then reads the positional COUNT, which decides the form (git
// config -h; git help config, DEPRECATED MODES):
//
//   - read forms (a --get*/--list flag, or a single bare <name>): no effect
//     beyond the metadata stdout the schema declares;
//   - the write form `<name> <value> [<value-pattern>]`: one EffectConfigKey
//     carrying the key, value, scope and the FACT class
//     LookupGitConfigKey assigns - never a verdict (P14);
//   - anything else (more than three operands): insufficient.
//
// An `alias.<name>` whose value begins with `!` is additionally handed to
// the graph builder as a shell-dialect child so the hidden command line is
// classified by ITS effects, not as a bare key lookup (P16). A key or value
// that is a runtime expansion is insufficient (fail-closed).
type gitConfigInterpreter struct{}

// Interpret implements Interpreter.
func (gitConfigInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st := scan(leaf, schema, ctx)
	st.finish()
	if !st.scanned {
		return st.result()
	}
	pos := st.positionals()
	// A scope flag on a READ form stays unmodeled (insufficient), exactly as
	// before this interpreter existed: the pinned goldens keep `git config
	// --global <key>` abstaining, and P16 only speaks to WRITES.
	scoped := st.anyFlagSeen("--global", "--system", "--local", "--worktree", "--file", "-f")
	if st.anyFlagSeen("--get", "--get-all", "--get-regexp", "-l", "--list") {
		if len(pos) > 1 {
			st.fail("git config read form takes at most one operand, got %d", len(pos))
		} else if scoped {
			st.fail("git config scope flag on a read form is unmodeled")
		}
		return st.result()
	}
	switch {
	case len(pos) <= 1:
		if scoped {
			st.fail("git config scope flag on a read form is unmodeled")
		}
		return st.result() // bare read form: `git config <name>`
	case len(pos) > 3:
		st.fail("git config write form takes at most three operands, got %d", len(pos))
		return st.result()
	}
	key, value := pos[0], pos[1]
	if leaf.ArgIsLiveExpansion(key.idx) || leaf.ArgIsLiveExpansion(value.idx) {
		st.fail("git config key or value is a runtime expansion")
		return st.result()
	}
	scope := GitConfigScopeLocal
	switch {
	case st.anyFlagSeen("--system"):
		scope = GitConfigScopeSystem
	case st.anyFlagSeen("--global"):
		scope = GitConfigScopeGlobal
	case st.anyFlagSeen("--file", "-f"):
		scope = GitConfigScopeFile
	}
	class := LookupGitConfigKey(key.tok, value.tok, scope)
	st.effects = append(st.effects, Effect{
		Kind:        EffectConfigKey,
		ConfigKey:   key.tok,
		ConfigValue: value.tok,
		ConfigClass: class,
		ConfigScope: scope,
		Source:      fmt.Sprintf("arg %d", key.idx),
	})
	if class == GitConfigAliasShell {
		st.children = append(st.children, ChildInvocation{
			Dialect: "shell",
			Program: strings.TrimPrefix(value.tok, "!"),
			Source:  "git config " + key.tok,
		})
	}
	return st.result()
}
