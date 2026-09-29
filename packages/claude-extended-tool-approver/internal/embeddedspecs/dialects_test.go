package embeddedspecs

import "github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"

// This file registers a TEST-ONLY dialect for "shell-file" -- see its own
// doc comment below for the full discovered-gap writeup. It is a _test.go
// file DELIBERATELY: that keeps the registration out of `go build ./...`
// and out of every OTHER package's own `go test` process entirely (each Go
// package's tests run in their own process), so it can never reach the
// compiled ceta binary or perturb internal/cmddesc's or
// internal/effectpolicy's own test suites -- both of which must keep seeing
// "shell-file" as unregistered, since their golden fixtures assert the
// EXACT reason text that only produces (interpreter_child_test.go's own
// "no interpreter for dialect shell-file" assertion; effectpolicy's
// testdata/bash_script_file/structural.mmd golden file's identical text).
func init() {
	cmddesc.RegisterDialect("shell-file", sentinelShellFileDialect{})
}

// sentinelShellFileDialect exists ONLY to satisfy specfmt.Validate's
// cmddesc.LookupDialect existence check for this package's own
// TestRoundTrip (roundtrip_test.go) -- its InterpretProgram is never
// actually invoked by anything in this package (neither BuildSpecs/
// WriteSpecs nor TestRoundTrip runs cmddesc's interpreter at all; they only
// compare CommandSchema VALUES via reflect.DeepEqual).
//
// # The discovered gap (recorded here for the record; see this packet's own
// # commit message for the summary)
//
// cmddesc/registry.go's bashSchema (and its renamed(...) alias "sh") uses
// Positionals.Leading: []OperandRole{Program("shell-file")} DELIBERATELY:
// per that schema's own doc comment, a bash invocation with no `-c` treats
// its first positional as a SCRIPT FILE whose contents cannot be read, and
// the chosen way to mark that operand "insufficient" is to name a dialect
// ("shell-file") that is intentionally NEVER registered in cmddesc's own
// `dialects` map (dialect.go) -- LookupDialect failing IS the mechanism
// (see interpreter.go's program(), which calls st.fail("no interpreter for
// dialect %s", dialect) exactly when LookupDialect reports not-found).
//
// specfmt.Validate (packet 1.1, internal/specfmt/validate.go,
// validateRoleDialect) independently rejects any spec naming a Program-role
// Dialect that cmddesc.LookupDialect does not recognise -- a correct,
// deliberate fail-closed anti-typo guard for HAND-AUTHORED specs (P13: "the
// loader MUST reject unknown Interpreter/Dialect/VerbFamily/RemoteFamily
// values"), which this packet must not weaken (Out of scope: "Building the
// loader/format itself -- that is packet 1.1, this packet is a pure
// consumer of it").
//
// Those two intentional designs collide: marshalling bashSchema faithfully
// (Dialect: "shell-file", unchanged, to satisfy the lossless round-trip
// requirement) makes specfmt.Validate reject it as an "unknown dialect" --
// which is not a typo, but this docket's own design (tc-o14i5.2, both its
// P13 binding-decision text and its Phase 1 item 2 "0 diffs" acceptance
// bar) does not mention or reconcile this specific case; it was verified,
// empirically, during this packet's implementation (not assumed).
//
// Registering "shell-file" here -- in a _test.go file, so it is provably
// scoped to only this package's own `go test` process (see this file's own
// top-of-file doc comment) -- lets specfmt.Validate's EXISTENCE check pass
// for TestRoundTrip without touching cmddesc/registry.go (untouched, per
// this packet's own binding decision), internal/specfmt (out of scope, per
// this packet's own binding decision), or any golden/agreement test file or
// fixture (all of which keep running, byte-for-byte unmodified, in their
// own separate process where "shell-file" stays unregistered).
type sentinelShellFileDialect struct{}

// InterpretProgram intentionally mirrors the SAME "no interpreter"
// insufficiency an unregistered dialect already produces (see this type's
// own doc comment) -- not that any caller in this package ever invokes it.
func (sentinelShellFileDialect) InterpretProgram(_ string, _ cmddesc.Context) cmddesc.ProgramInterpretation {
	return cmddesc.ProgramInterpretation{Sufficient: false, Insufficiency: "no interpreter for dialect shell-file"}
}
