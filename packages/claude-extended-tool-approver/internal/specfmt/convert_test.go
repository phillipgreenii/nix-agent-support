package specfmt

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
)

// allRoleKinds lists every cmddesc.RoleKind this package's convert.go must
// round-trip, built the same way roleKindByName is (a contiguous iota range)
// so a new RoleKind added to cmddesc automatically shows up here too instead
// of silently going untested.
func allRoleKinds() []cmddesc.RoleKind {
	var ks []cmddesc.RoleKind
	for k := cmddesc.KindLiteral; k <= cmddesc.KindKeyMaterial; k++ {
		ks = append(ks, k)
	}
	return ks
}

func allTransformKinds() []cmddesc.TransformKind {
	var ks []cmddesc.TransformKind
	for k := cmddesc.TransformNone; k <= cmddesc.TransformDeleteRef; k++ {
		ks = append(ks, k)
	}
	return ks
}

func cite(s string) Citation { return Citation{Source: s} }

// kitchenSinkSchema builds a CommandSchema exercising every RoleKind, every
// TransformKind, every Arity, every positional form (Leading/Rest/Trailing,
// both Skipped-by-flags variants, LeadingOptional, RestOverride, StdinToken),
// and an ImplicitEffects entry for every When*/Dynamic/RemoteFamily
// combination this package's convert.go handles, plus a Subcommands+
// DefaultSubcommand shape and a VerbFamily+DefaultVerb shape — the fixture
// TestRoundTrip (below) marshals through specfmt and back and checks the
// result is byte-for-byte the same schema.
func kitchenSinkSchema() (cmddesc.CommandSchema, map[string]Citation) {
	citations := map[string]Citation{
		citationKeyProvenance:  cite("man:kitchen-sink(1)"),
		citationKeyStdin:       cite("help: reads stdin when no FILE operand given"),
		citationKeyStdout:      cite("help: prints file contents"),
		citationKeyUnknownFlag: cite("help: no undocumented flags observed"),
		citationKeyInterpreter: cite("source-line: uses xargs semantics"),
		"positionals":          cite("help synopsis: kitchen-sink [OPTS] FILE...  DEST"),
	}

	flags := map[string]cmddesc.FlagSpec{}
	for _, rk := range allRoleKinds() {
		name := "--role-" + rk.String()
		flags[name] = cmddesc.FlagSpec{Arity: cmddesc.ArityOne, Operand: cmddesc.OperandRole{Kind: rk}}
		citations["flag:"+name] = cite("help: " + name)
	}
	for _, tk := range allTransformKinds() {
		name := "--transform-" + tk.String()
		flags[name] = cmddesc.FlagSpec{Arity: cmddesc.ArityNone, Transform: cmddesc.EffectTransform{Kind: tk}}
		citations["flag:"+name] = cite("help: " + name)
	}
	flags["--arity-none"] = cmddesc.FlagSpec{Arity: cmddesc.ArityNone}
	citations["flag:--arity-none"] = cite("help: --arity-none")
	flags["--arity-one"] = cmddesc.FlagSpec{Arity: cmddesc.ArityOne, Operand: cmddesc.PathRead}
	citations["flag:--arity-one"] = cite("help: --arity-one FILE")
	flags["-i"] = cmddesc.FlagSpec{Arity: cmddesc.ArityOptionalGlued, Operand: cmddesc.PathCreate}
	citations["flag:-i"] = cite("help: -i[SUFFIX]")
	flags["--arg"] = cmddesc.FlagSpec{Arity: cmddesc.ArityN, Operands: []cmddesc.OperandRole{cmddesc.Literal, cmddesc.PathRead}}
	citations["flag:--arg"] = cite("help: --arg NAME FILE")
	flags["--program"] = cmddesc.FlagSpec{Arity: cmddesc.ArityOne, Operand: cmddesc.Program("sed")}
	citations["flag:--program"] = cite("help: --program SCRIPT")
	flags["--remote"] = cmddesc.FlagSpec{Arity: cmddesc.ArityOne, Operand: cmddesc.Remote("push")}
	citations["flag:--remote"] = cite("help: --remote NAME")
	flags["--list"] = cmddesc.FlagSpec{Arity: cmddesc.ArityNone}
	citations["flag:--list"] = cite("help: --list")

	implicit := []cmddesc.ImplicitEffect{
		{Role: cmddesc.PathRead, Target: ".", WhenNoPositionals: true},
		{Role: cmddesc.Chdir, Target: "-", Dynamic: true},
		{Role: cmddesc.Remote("push"), Target: "origin", WhenNoRestPositionals: true, RemoteFamily: "kubectl"},
		{Role: cmddesc.Literal, Target: "x", WhenFlags: []string{"-r", "-R"}},
		{Role: cmddesc.Exec, Target: "go test"},
	}
	implicitCitations := []string{
		"help: bare invocation reads cwd",
		"help: cd - returns to previous dir",
		"help: default remote push",
		"help: -r/-R recurse from .",
		"source-line: TrustedCheckoutExec",
	}
	for idx, c := range implicitCitations {
		citations[fmt.Sprintf("implicitEffect:%d", idx)] = cite(c)
	}

	schema := cmddesc.CommandSchema{
		Name:       "kitchen-sink",
		Provenance: "kitchen-sink 1.0",
		Flags:      flags,
		Positionals: cmddesc.PositionalSpec{
			Leading:                []cmddesc.OperandRole{cmddesc.Program("sed")},
			LeadingSkippedByFlags:  []string{"-e", "-f"},
			LeadingOptional:        true,
			Rest:                   cmddesc.PathRead,
			RestOverride:           cmddesc.RestOverride{Flags: []string{"-l", "--list"}, Role: cmddesc.Literal},
			MinRest:                1,
			Trailing:               []cmddesc.OperandRole{cmddesc.PathCreate},
			TrailingSkippedByFlags: []string{"-t"},
			StdinToken:             "-",
		},
		ImplicitEffects:       implicit,
		Stdin:                 cmddesc.StdinWhenNoPathOperands,
		Stdout:                cmddesc.StdoutContent,
		UnknownFlag:           cmddesc.UnknownFlagInsufficient,
		EndOfOptions:          true,
		PositionalsEndOptions: false,
		Interpreter:           "xargs",
	}

	return schema, citations
}

func TestRoundTrip_KitchenSink(t *testing.T) {
	schema, citations := kitchenSinkSchema()

	wire, err := FromSchema(schema, citations)
	if err != nil {
		t.Fatalf("FromSchema: %v", err)
	}

	// The wire form must marshal/unmarshal through JSON cleanly (it is,
	// after all, an ON-DISK format).
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var roundTripped CommandSpecV1
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	back, err := ToSchema(roundTripped)
	if err != nil {
		t.Fatalf("ToSchema: %v", err)
	}

	if !reflect.DeepEqual(schema, back) {
		t.Fatalf("round trip mismatch:\n  original: %#v\n  got:      %#v", schema, back)
	}
}

func TestRoundTrip_Subcommands(t *testing.T) {
	schema := cmddesc.CommandSchema{
		Name: "git",
		Subcommands: map[string]cmddesc.CommandSchema{
			"push": {
				Name:  "push",
				Stdin: cmddesc.StdinNever,
				Positionals: cmddesc.PositionalSpec{
					Leading:         []cmddesc.OperandRole{cmddesc.Remote("push")},
					LeadingOptional: true,
				},
			},
			"branch": {
				Name:  "branch",
				Stdin: cmddesc.StdinNever,
				Positionals: cmddesc.PositionalSpec{
					Rest: cmddesc.Unmodeled,
				},
			},
		},
		DefaultSubcommand: "push",
	}
	citations := map[string]Citation{
		citationKeyProvenance:                         cite("man:git(1)"),
		citationKeyStdin:                              cite("git has no top-level stdin behavior"),
		citationKeyStdout:                             cite("git has no top-level stdout behavior"),
		citationKeyUnknownFlag:                        cite("n/a"),
		"positionals":                                 cite("n/a"),
		"subcommand:push:" + citationKeyProvenance:    cite("man:git-push(1)"),
		"subcommand:push:" + citationKeyStdin:         cite("git push never reads stdin"),
		"subcommand:push:" + citationKeyStdout:        cite("git push status to stdout"),
		"subcommand:push:" + citationKeyUnknownFlag:   cite("n/a"),
		"subcommand:push:positionals":                 cite("help: git push [remote] [refspec...]"),
		"subcommand:branch:" + citationKeyProvenance:  cite("man:git-branch(1)"),
		"subcommand:branch:" + citationKeyStdin:       cite("git branch never reads stdin"),
		"subcommand:branch:" + citationKeyStdout:      cite("git branch listing to stdout"),
		"subcommand:branch:" + citationKeyUnknownFlag: cite("n/a"),
		"subcommand:branch:positionals":               cite("help: git branch [pattern]"),
	}

	wire, err := FromSchema(schema, citations)
	if err != nil {
		t.Fatalf("FromSchema: %v", err)
	}
	back, err := ToSchema(wire)
	if err != nil {
		t.Fatalf("ToSchema: %v", err)
	}
	if !reflect.DeepEqual(schema, back) {
		t.Fatalf("subcommand round trip mismatch:\n  original: %#v\n  got:      %#v", schema, back)
	}
}

func TestRoundTrip_VerbFamily(t *testing.T) {
	schema := cmddesc.CommandSchema{
		Name:        "just",
		VerbFamily:  "just",
		DefaultVerb: "",
		Stdin:       cmddesc.StdinNever,
	}
	citations := map[string]Citation{
		citationKeyProvenance:  cite("man:just(1)"),
		citationKeyStdin:       cite("just never reads stdin"),
		citationKeyStdout:      cite("just lists recipes"),
		citationKeyUnknownFlag: cite("n/a"),
		"positionals":          cite("n/a"),
	}

	wire, err := FromSchema(schema, citations)
	if err != nil {
		t.Fatalf("FromSchema: %v", err)
	}
	if wire.VerbFamily != "just" {
		t.Fatalf("VerbFamily = %q, want %q", wire.VerbFamily, "just")
	}
	back, err := ToSchema(wire)
	if err != nil {
		t.Fatalf("ToSchema: %v", err)
	}
	if !reflect.DeepEqual(schema, back) {
		t.Fatalf("verb family round trip mismatch:\n  original: %#v\n  got:      %#v", schema, back)
	}
}
