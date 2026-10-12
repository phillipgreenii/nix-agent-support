package cmddesc

import (
	"fmt"
	"strings"
)

// Config-source / exec-program operand handling (docket tc-o14i5.3, packet
// tc-o14i5.3.5, design item e and P13). Per P14 everything here is FACT: it
// records WHICH file a tool will load as configuration and WHICH command or
// path it will run, never a verdict (effectpolicy.ConfigExecSourcePolicy
// judges). Per P9 nothing here executes or opens the named source - the
// classification reads the argv text only.

// The Dialect values an EffectExecProgram carries.
const (
	// ExecDialectShell: Program is a command line, ALSO handed to the graph
	// builder as a shell child so its own effects are judged.
	ExecDialectShell = "shell"
	// ExecDialectPath: Program is a program or directory path the tool runs
	// or loads code from; the engine cannot see the code, only where it is.
	ExecDialectPath = "path"
	// ExecDialectOpaque ("") : text the engine has no interpreter for.
)

// RemoteProgramDetail is the Detail of an EffectExecProgram that names a
// program the tool runs on the remote end because the command line chose it
// (git push --receive-pack / --exec): effectpolicy rejects it whatever the
// path, because the program is chosen by the caller, not by repository state.
const RemoteProgramDetail = "remote-program"

// FlakeRefDetail is the Detail of an EffectConfigSource that names a nix flake
// reference rather than a file.
const FlakeRefDetail = "flake-ref"

func isSourceRole(k RoleKind) bool {
	switch k {
	case KindConfigSource, KindExecCommand, KindExecPath, KindExecOpaque, KindFlakeRef,
		KindOptionPair, KindOptionName, KindOptionValue, KindExecRemote:
		return true
	}
	return false
}

// sourceOperand emits the effect(s) for one operand under a source role.
func (st *interpState) sourceOperand(op pendingOp, role OperandRole, live bool, source string) {
	label := st.childSource(op)
	switch role.Kind {
	case KindConfigSource:
		st.configSource(op.tok, live, source, "")
	case KindFlakeRef:
		st.configSource(op.tok, live, source, FlakeRefDetail)
	case KindExecCommand:
		st.execProgram(op.tok, ExecDialectShell, live, source, label, "")
	case KindExecPath:
		st.execProgram(op.tok, ExecDialectPath, live, source, label, "")
	case KindExecOpaque:
		st.execProgram(op.tok, "", live, source, label, "")
	case KindExecRemote:
		st.execProgram(op.tok, ExecDialectPath, live, source, label, RemoteProgramDetail)
	case KindOptionName:
		st.optName, st.optNameLive = op.tok, live
	case KindOptionValue:
		if st.optNameLive {
			st.fail("option name at %s is a runtime expansion", source)
			return
		}
		st.optionAction(role.Set, st.optName, op.tok, live, source, label, op.flag == "--config-env")
	case KindOptionPair:
		if live {
			st.fail("option pair at %s is a runtime expansion", source)
			return
		}
		key, val := splitOptionPair(op.tok, role.Set)
		st.optionAction(role.Set, key, val, live, source, label, op.flag == "--config-env")
	}
}

// configSource emits an EffectConfigSource. `-` is stdin, whose content is
// not a path the engine can locate: Dynamic.
func (st *interpState) configSource(tok string, live bool, source, detail string) {
	dynamic := live
	if !live && tok == "-" && detail == "" {
		dynamic, detail = true, "stdin"
	}
	st.effects = append(st.effects, Effect{Kind: EffectConfigSource, Path: tok, Dynamic: dynamic, Source: source, Detail: detail})
}

// execProgram emits an EffectExecProgram; for a shell command line that is
// statically known it also records the child invocation so the text's own
// effects are classified.
func (st *interpState) execProgram(text, dialect string, live bool, source, label, detail string) {
	st.effects = append(st.effects, Effect{Kind: EffectExecProgram, Program: text, Dialect: dialect, Dynamic: live, Source: source, Detail: detail})
	if dialect == ExecDialectShell && !live {
		st.children = append(st.children, ChildInvocation{Dialect: "shell", Program: text, Source: label})
	}
}

// optionAction kinds.
type optionAction int

const (
	optInert optionAction = iota
	optConfigSource
	optExecCommand
	optExecPath
	optExecOpaque
	optUnmodeled
)

type optionTable struct {
	byKey map[string]optionAction // lower-cased key
	other optionAction            // action for a key not in byKey
	cite  string
}

// optionTables names the key tables KindOptionPair/KindOptionName resolve
// through. Per P14 each row cites its source.
var optionTables = map[string]optionTable{
	// ssh -o <keyword>=<value>: ssh_config(5) (OpenSSH 10.5p1) keywords whose
	// value is a command line (ProxyCommand: "Specifies the command to use to
	// connect to the server"; LocalCommand: "Specifies a command to execute on
	// the local machine after successfully connecting"; KnownHostsCommand:
	// "Specifies a command to use to obtain a list of host keys"), a shared
	// library path (PKCS11Provider, SecurityKeyProvider) or another config
	// file (Include). Every other keyword keeps the pre-existing inert
	// treatment of `-o` values.
	"ssh-config": {
		byKey: map[string]optionAction{
			"proxycommand":        optExecCommand,
			"localcommand":        optExecCommand,
			"knownhostscommand":   optExecCommand,
			"pkcs11provider":      optExecPath,
			"securitykeyprovider": optExecPath,
			"include":             optConfigSource,
		},
		other: optInert,
		cite:  "ssh_config(5), OpenSSH 10.5p1 (ProxyCommand, LocalCommand, KnownHostsCommand, PKCS11Provider, SecurityKeyProvider, Include)",
	},
	// nix --option NAME VALUE: the four settings that name a program nix runs
	// (verified on this host's Nix 2.34.8: `nix show-config` lists build-hook,
	// diff-hook, post-build-hook, pre-build-hook). They run with the daemon's
	// privileges, not as checkout code, so they are opaque exec sources and
	// never boundary-vouched. Any other setting is unmodeled (as before).
	"nix-settings": {
		byKey: map[string]optionAction{
			"post-build-hook": optExecOpaque,
			"pre-build-hook":  optExecOpaque,
			"build-hook":      optExecOpaque,
			"diff-hook":       optExecOpaque,
		},
		other: optUnmodeled,
		cite:  "nix 2.34.8 `nix show-config`; nix.conf(5)",
	},
}

// splitOptionPair splits `key=value` or `key value` (ssh_config's two
// spellings) at the first '=' or blank. Only ssh-config accepts the blank.
func splitOptionPair(tok, table string) (key, val string) {
	cut := strings.IndexByte(tok, '=')
	if table != "ssh-config" {
		// git -c / --config-env: the value is everything after the first '='.
		if cut < 0 {
			return tok, ""
		}
		return tok[:cut], tok[cut+1:]
	}
	if i := strings.IndexAny(tok, " \t"); i >= 0 && (cut < 0 || i < cut) {
		cut = i
	}
	if cut < 0 {
		return strings.TrimSpace(tok), ""
	}
	return strings.TrimSpace(tok[:cut]), stripWordQuotes(strings.TrimLeft(strings.TrimSpace(tok[cut+1:]), "= \t"))
}

// optionAction applies the table row for key to value.
func (st *interpState) optionAction(table, key, val string, live bool, source, label string, fromEnv bool) {
	if table == "git-config" {
		st.gitConfigPair(key, val, live, source, fromEnv)
		return
	}
	t, ok := optionTables[table]
	if !ok {
		st.fail("option at %s names unknown table %q", source, table)
		return
	}
	act, found := t.byKey[strings.ToLower(key)]
	if !found {
		act = t.other
	}
	switch act {
	case optInert:
	case optConfigSource:
		st.configSource(val, live, source, "")
	case optExecCommand:
		st.execProgram(val, ExecDialectShell, live, source, label+" "+key, "")
	case optExecPath:
		st.execProgram(val, ExecDialectPath, live, source, label+" "+key, "")
	case optExecOpaque:
		st.execProgram(val, "", live, source, label+" "+key, "")
	default:
		st.fail("option %q at %s is not modeled (%s)", key, source, t.cite)
	}
}

// gitConfigPair is git's global `-c key=value` (git help git, "-c <name>=
// <value>") and `--config-env=<name>=<envvar>`: one transient EffectConfigKey
// carrying the P16 FACT class LookupGitConfigKey assigns (K9's table; nothing
// is re-decided here). For --config-env the value lives in the environment
// and is unknowable, so the lookup runs with an empty value (the key's
// default class) and the effect says so in Detail.
func (st *interpState) gitConfigPair(key, val string, live bool, source string, fromEnv bool) {
	if live {
		st.fail("git config pair at %s is a runtime expansion", source)
		return
	}
	if key == "" {
		st.fail("git config pair at %s has no key", source)
		return
	}
	detail := "git -c"
	if fromEnv {
		detail = "git --config-env, value from $" + val
		val = ""
	}
	class := LookupGitConfigKey(key, val, GitConfigScopeLocal)
	st.effects = append(st.effects, Effect{
		Kind: EffectConfigKey, ConfigKey: key, ConfigValue: val,
		ConfigClass: class, ConfigScope: GitConfigScopeLocal,
		Source: source, Detail: detail,
	})
	if class == GitConfigAliasShell {
		st.children = append(st.children, ChildInvocation{
			Dialect: "shell", Program: strings.TrimPrefix(val, "!"),
			Source: fmt.Sprintf("git -c %s", key),
		})
	}
}

// ExecSourceEnvEffect classifies an environment-variable ASSIGNMENT whose
// NAME makes the invoked tool run something: GIT_EXEC_PATH (git help git:
// "path to wherever your core Git programs are installed" - git runs its
// sub-programs from there) and SSH_ASKPASS / SSH_ASKPASS_REQUIRE (ssh(1):
// "the program named by SSH_ASKPASS is run to read a passphrase";
// SSH_ASKPASS_REQUIRE gates whether it is used). This is the exec-SOURCE
// axis; it is deliberately independent of the PATH-hijack env allowlist
// (effectpolicy.EnvAssignment). value is the assignment's verbatim text and
// dynamic is true when it contains a runtime expansion.
func ExecSourceEnvEffect(name, value string, dynamic bool) (Effect, bool) {
	var dialect string
	switch {
	case name == "GIT_EXEC_PATH", name == "SSH_ASKPASS":
		dialect = ExecDialectPath
	case strings.HasPrefix(name, "SSH_ASKPASS"):
		dialect = "" // SSH_ASKPASS_REQUIRE: a gate value, not a program.
	default:
		return Effect{}, false
	}
	if n := len(value); n >= 2 && (value[0] == '"' || value[0] == '\'') && value[n-1] == value[0] {
		value = value[1 : n-1]
	}
	return Effect{
		Kind: EffectExecProgram, Program: value, Dialect: dialect, Dynamic: dynamic,
		Source: "env " + name, Detail: "environment exec source " + name,
	}, true
}

// stripWordQuotes removes ONE pair of matching surrounding quotes. cmdparse
// unquotes a whole-word quote but leaves a quote that opens mid-word
// (`ProxyCommand='nc %h %p'`) in the token text, and ssh_config(5) itself
// treats a quoted value as the unquoted one.
func stripWordQuotes(s string) string {
	if n := len(s); n >= 2 && (s[0] == '"' || s[0] == '\'') && s[n-1] == s[0] {
		return s[1 : n-1]
	}
	return s
}
