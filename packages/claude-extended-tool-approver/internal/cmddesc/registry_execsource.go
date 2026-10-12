package cmddesc

// Packet tc-o14i5.3.5 (docket tc-o14i5.3, design item e; P13's config-source
// and exec-program effect kinds): schema entries for the tools whose command
// line names a CONFIG SOURCE or an EXEC PROGRAM the engine cannot otherwise
// see. Per P14 these are facts with a citation each; effectpolicy's
// ConfigExecSourcePolicy supplies every verdict.

// nixExecSettingFlags are the nix flags that name a program nix runs: the
// generic `--option NAME VALUE` (VALUE routed through optionTables
// "nix-settings") and the four hook settings spelled as their own flag (nix
// accepts every setting as `--<name> <value>`; verified on this host's Nix
// 2.34.8 via `nix show-config`, which lists build-hook, diff-hook,
// post-build-hook and pre-build-hook). Deliberately NOT included: any other
// setting, which stays an unknown flag (Insufficient) as before.
var nixExecSettingFlags = map[string]FlagSpec{
	"--option":          {Arity: ArityN, Operands: []OperandRole{OptionName("nix-settings"), OptionValue("nix-settings")}},
	"--post-build-hook": {Arity: ArityOne, Operand: ExecOpaque},
	"--pre-build-hook":  {Arity: ArityOne, Operand: ExecOpaque},
	"--build-hook":      {Arity: ArityOne, Operand: ExecOpaque},
	"--diff-hook":       {Arity: ArityOne, Operand: ExecOpaque},
}

// nixDevelopSchema: `nix develop [flags] [installable]` (nix develop --help,
// Nix 2.34.8: "Run a bash shell that provides the build environment of a
// derivation"; default installable is the current directory's flake). The
// installable is a FlakeRef: its flake's shellHook and inputs are code the
// engine cannot see, judged only by whether the reference is local
// (ConfigExecSourcePolicy). `-c/--command` is deliberately absent (Insufficient):
// it runs an arbitrary trailing argv.
var nixDevelopSchema = CommandSchema{
	Name:       "develop",
	Provenance: "nix develop --help (Nix 2.34.8), this host 2026-10-11",
	Flags: mergeFlags(nixExecSettingFlags, map[string]FlagSpec{
		"-L": inert, "--print-build-logs": inert, "--help": inert,
	}),
	Positionals: PositionalSpec{Rest: FlakeRef},
	ImplicitEffects: []ImplicitEffect{
		{Role: FlakeRef, Target: ".", WhenNoPositionals: true},
		{Role: Unmodeled, Target: "nix develop without -c starts an interactive shell"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// nixShellSchema: `nix shell [flags] installables...` (nix shell --help, Nix
// 2.34.8: "Run a shell in which the specified packages are available").
// Same treatment as nixDevelopSchema; `-c/--command` absent.
var nixShellSchema = CommandSchema{
	Name:       "shell",
	Provenance: "nix shell --help (Nix 2.34.8), this host 2026-10-11",
	Flags: mergeFlags(nixExecSettingFlags, map[string]FlagSpec{
		"-L": inert, "--print-build-logs": inert, "--help": inert,
	}),
	Positionals: PositionalSpec{Rest: FlakeRef},
	ImplicitEffects: []ImplicitEffect{
		{Role: Unmodeled, Target: "nix shell without -c starts an interactive shell"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// sqlite3Schema: `sqlite3 [OPTIONS] [FILENAME [SQL...]]` (sqlite3 command-line
// shell, https://sqlite.org/cli.html "Command-line options": `-init FILE`
// "Read and process commands from FILE", `-cmd COMMAND` "run COMMAND before
// reading stdin"). No sqlite3 binary exists on this host to capture --help
// from, so the option list below is the documented one and is kept to the
// flags the packet names plus a handful of boolean output/safety switches
// from that same page.
//
// -init is a config source (FILE is a script of sqlite3 dot-commands and SQL,
// including `.shell`/`.system`); -cmd is exec program text in a language this
// engine has no interpreter for (ExecOpaque, never a child). The database
// FILENAME is a PathModify (sqlite3 creates the file when absent); the SQL
// operands are Unmodeled, so every sqlite3 invocation that carries SQL
// abstains - only the flags' own effects are classified here.
var sqlite3Schema = CommandSchema{
	Name:       "sqlite3",
	Provenance: "sqlite3 command-line shell documentation, https://sqlite.org/cli.html (options -init, -cmd, -readonly, -bail, -batch, -echo, -header, -noheader); no sqlite3 binary on this host to verify against, 2026-10-11",
	Flags: map[string]FlagSpec{
		"-init":     {Arity: ArityOne, Operand: ConfigSource},
		"-cmd":      {Arity: ArityOne, Operand: ExecOpaque},
		"-readonly": inert, "-bail": inert, "-batch": inert, "-echo": inert,
		"-header": inert, "-noheader": inert,
	},
	Positionals: PositionalSpec{
		Leading:         []OperandRole{PathModify},
		LeadingOptional: true,
		Rest:            Unmodeled,
	},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: false,
}
