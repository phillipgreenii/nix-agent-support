package specfmt

import (
	"strings"
	"testing"
)

func validCommandSpec() Spec {
	return Spec{
		Version: FormatVersion,
		Kind:    KindCommand,
		Name:    "ok",
		Command: &CommandSpecV1{
			Name:       "ok",
			Provenance: "ok 1.0",
			Citations: map[string]Citation{
				citationKeyProvenance:  cite("test"),
				citationKeyStdin:       cite("test"),
				citationKeyStdout:      cite("test"),
				citationKeyUnknownFlag: cite("test"),
			},
			Positionals: PositionalSpecV1{Citation: cite("test")},
			Stdin:       stdinNever,
			Stdout:      stdoutNone,
			UnknownFlag: unknownFlagInsuf,
		},
	}
}

func TestValidate_WellFormedPasses(t *testing.T) {
	if err := Validate(validCommandSpec()); err != nil {
		t.Fatalf("Validate(well-formed spec) = %v, want nil", err)
	}
}

func TestValidate_UnknownInterpreterRejected(t *testing.T) {
	s := validCommandSpec()
	s.Command.Interpreter = "not-a-real-interpreter"
	s.Command.Citations[citationKeyInterpreter] = cite("test")
	err := Validate(s)
	if err == nil || !strings.Contains(err.Error(), "unknown interpreter") {
		t.Fatalf("Validate = %v, want an unknown-interpreter error", err)
	}
}

func TestValidate_UnknownDialectRejected(t *testing.T) {
	s := validCommandSpec()
	s.Command.Flags = map[string]FlagSpecV1{
		"--program": {
			Arity:    arityOne,
			Operand:  OperandRoleV1{Kind: "program", Dialect: "not-a-real-dialect"},
			Citation: cite("test"),
		},
	}
	err := Validate(s)
	if err == nil || !strings.Contains(err.Error(), "unknown dialect") {
		t.Fatalf("Validate = %v, want an unknown-dialect error", err)
	}
}

func TestValidate_KnownDialectAccepted(t *testing.T) {
	s := validCommandSpec()
	s.Command.Flags = map[string]FlagSpecV1{
		"--program": {
			Arity:    arityOne,
			Operand:  OperandRoleV1{Kind: "program", Dialect: "sed"},
			Citation: cite("test"),
		},
	}
	if err := Validate(s); err != nil {
		t.Fatalf("Validate(known dialect) = %v, want nil", err)
	}
}

func TestValidate_UnknownVerbFamilyRejected(t *testing.T) {
	s := validCommandSpec()
	s.Command.VerbFamily = "not-a-real-verb-family"
	err := Validate(s)
	if err == nil || !strings.Contains(err.Error(), "unknown verb family") {
		t.Fatalf("Validate = %v, want an unknown-verb-family error", err)
	}
}

func TestValidate_UnknownRemoteFamilyRejected(t *testing.T) {
	s := validCommandSpec()
	s.Command.ImplicitEffects = []ImplicitEffectV1{
		{Role: OperandRoleV1{Kind: "remote"}, RemoteFamily: "not-a-real-remote-family", Citation: cite("test")},
	}
	err := Validate(s)
	if err == nil || !strings.Contains(err.Error(), "unknown remote family") {
		t.Fatalf("Validate = %v, want an unknown-remote-family error", err)
	}
}

func TestValidate_KnownRemoteFamilyAccepted(t *testing.T) {
	s := validCommandSpec()
	s.Command.ImplicitEffects = []ImplicitEffectV1{
		{Role: OperandRoleV1{Kind: "remote"}, RemoteFamily: "kubectl", Citation: cite("test")},
	}
	if err := Validate(s); err != nil {
		t.Fatalf("Validate(known remote family) = %v, want nil", err)
	}
}

func TestValidate_MissingCitationRejected(t *testing.T) {
	s := validCommandSpec()
	delete(s.Command.Citations, citationKeyStdout)
	err := Validate(s)
	if err == nil || !strings.Contains(err.Error(), `missing citation for "stdout"`) {
		t.Fatalf("Validate = %v, want a missing-citation error for stdout", err)
	}
}

func TestValidate_MissingFlagCitationRejected(t *testing.T) {
	s := validCommandSpec()
	s.Command.Flags = map[string]FlagSpecV1{
		"--no-citation": {Arity: arityNone},
	}
	err := Validate(s)
	if err == nil || !strings.Contains(err.Error(), `flag "--no-citation": missing citation`) {
		t.Fatalf("Validate = %v, want a missing flag-citation error", err)
	}
}

func TestValidate_RejectsCommandKindWithoutPayload(t *testing.T) {
	s := Spec{Version: FormatVersion, Kind: KindCommand, Name: "no-payload"}
	err := Validate(s)
	if err == nil || !strings.Contains(err.Error(), "requires a command payload") {
		t.Fatalf("Validate = %v, want a missing-payload error", err)
	}
}

func TestValidate_ReservedKindsAcceptedEmpty(t *testing.T) {
	for _, k := range []SpecKind{KindPath, KindTarget} {
		s := Spec{Version: FormatVersion, Kind: k, Name: "reserved"}
		if err := Validate(s); err != nil {
			t.Fatalf("Validate(kind=%s, no payload) = %v, want nil (reserved, no fields defined yet)", k, err)
		}
	}
}

func TestValidate_UnknownKindRejected(t *testing.T) {
	s := Spec{Version: FormatVersion, Kind: SpecKind("bogus"), Name: "x"}
	err := Validate(s)
	if err == nil || !strings.Contains(err.Error(), "unknown spec kind") {
		t.Fatalf("Validate = %v, want an unknown-kind error", err)
	}
}

func TestValidate_UnsupportedVersionRejected(t *testing.T) {
	s := validCommandSpec()
	s.Version = "v99"
	err := Validate(s)
	if err == nil || !strings.Contains(err.Error(), "unsupported spec version") {
		t.Fatalf("Validate = %v, want an unsupported-version error", err)
	}
}

func TestValidate_SubcommandUnknownInterpreterRejected(t *testing.T) {
	s := validCommandSpec()
	sub := validCommandSpec().Command
	sub.Interpreter = "not-a-real-interpreter"
	sub.Citations[citationKeyInterpreter] = cite("test")
	s.Command.Subcommands = map[string]CommandSpecV1{"sub": *sub}
	err := Validate(s)
	if err == nil || !strings.Contains(err.Error(), "unknown interpreter") {
		t.Fatalf("Validate(subcommand) = %v, want an unknown-interpreter error", err)
	}
}
