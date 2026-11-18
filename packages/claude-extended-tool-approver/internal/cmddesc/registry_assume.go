package cmddesc

// assumeSchema describes the AWS role-assumption helper `assume`. Assuming a
// role is the one step that turns a session into a production-capable one, and
// the operator ruled that an agent never performs it (operator ruling, Phillip,
// 2026-11-17: "as long as assume isn't allowed to be called by the agent, then
// it would only be able to access prod systems if it was run in an assumed
// environment which is only possible if i have explicitly done so. so, the
// block/ask is not on using prod tools or accessing prod systems, but on the
// initial auth/envar setup to allow the access.").
//
// The schema states that fact as one implicit remote effect, operation
// "assume-role", which effectpolicy.RemoteMutation judges Forbidden. It does
// not depend on the arguments, so every spelling carries it: a role operand,
// --exec <command> (a flag that still performs the assume, then runs the
// command), --exec=<command>, or any flag the schema does not know. The unknown
// flags are inert for exactly that reason: no flag can remove the effect, and
// the verdict is a rejection either way.
var assumeSchema = CommandSchema{
	Name:        "assume",
	Provenance:  "operator ruling, Phillip, 2026-11-17: an agent must never run assume (no --help citation: assume is an operator-installed role helper, not packaged here)",
	Flags:       map[string]FlagSpec{"-h": inert, "--help": inert},
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: Remote("assume-role"), Target: "aws"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInert,
	EndOfOptions: true,
}

// sourceSchema describes the `source` / `.` shell builtins, modeled only far
// enough to see that the script they run is `assume`: `source assume ...` and
// `. assume ...` run the assume helper's script in the current shell, which is
// the same role assumption (the usual spelling, since assume must export
// credentials into the calling shell). The script name is the subcommand key,
// so the arguments after it are interpreted by assumeSchema. Sourcing any other
// script stays an unmodeled subcommand, which abstains exactly as the missing
// schema did before.
var sourceSchema = CommandSchema{
	Name:       "source",
	Provenance: "bash builtin, help source: \"source: source filename [arguments]\"; the assume form per operator ruling, Phillip, 2026-11-17",
	Subcommands: map[string]CommandSchema{
		"assume": assumeSchema,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}
