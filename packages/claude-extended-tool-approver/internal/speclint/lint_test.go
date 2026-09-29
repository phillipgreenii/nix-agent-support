package speclint

import (
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
)

const thinCite = "internal/cmddesc/registry.go: cmddesc.DefaultRegistry()[\"x\"]"

func realCite(s string) specfmt.Citation { return specfmt.Citation{Source: s} }

// minimalCommand builds a well-formed CommandSpecV1 (every mandatory
// citation populated with a REAL, non-thin citation) that this test file's
// cases mutate from.
func minimalCommand() specfmt.CommandSpecV1 {
	return specfmt.CommandSpecV1{
		Name: "x",
		Citations: map[string]specfmt.Citation{
			"provenance":  realCite("man x(1)"),
			"stdin":       realCite("man x(1), STDIN section"),
			"stdout":      realCite("man x(1), STDOUT section"),
			"unknownFlag": realCite("man x(1): reviewed full flag list"),
		},
		Positionals: specfmt.PositionalSpecV1{Citation: realCite("man x(1) synopsis")},
		Stdin:       "never",
		Stdout:      "none",
		UnknownFlag: "insufficient",
	}
}

func TestLintCommand_CitationPresence(t *testing.T) {
	t.Run("thin citation on a builtin spec is WARN", func(t *testing.T) {
		c := minimalCommand()
		c.Citations["provenance"] = realCite(thinCite)
		findings := LintCommand("x", c, true /* builtin */)
		found := findFinding(findings, CheckCitationPresence, "provenance")
		if found == nil {
			t.Fatalf("expected a citation-presence finding, got %#v", findings)
		}
		if found.Severity != SeverityWarn {
			t.Errorf("severity = %v, want %v", found.Severity, SeverityWarn)
		}
	})

	t.Run("thin citation on a non-builtin spec is HARD", func(t *testing.T) {
		c := minimalCommand()
		c.Citations["provenance"] = realCite(thinCite)
		findings := LintCommand("x", c, false /* builtin */)
		found := findFinding(findings, CheckCitationPresence, "provenance")
		if found == nil {
			t.Fatalf("expected a citation-presence finding, got %#v", findings)
		}
		if found.Severity != SeverityHard {
			t.Errorf("severity = %v, want %v", found.Severity, SeverityHard)
		}
	})

	t.Run("a real (non-thin) citation is never flagged", func(t *testing.T) {
		c := minimalCommand()
		findings := LintCommand("x", c, false)
		if found := findFinding(findings, CheckCitationPresence, ""); found != nil {
			t.Errorf("unexpected citation-presence finding: %#v", found)
		}
	})

	t.Run("flag and implicit-effect and positionals citations are all checked", func(t *testing.T) {
		c := minimalCommand()
		c.Flags = map[string]specfmt.FlagSpecV1{
			"--quiet": {Arity: "none", Citation: realCite(thinCite)},
		}
		c.ImplicitEffects = []specfmt.ImplicitEffectV1{
			{Citation: realCite(thinCite)},
		}
		c.Positionals.Citation = realCite(thinCite)
		findings := LintCommand("x", c, false)
		wantFields := []string{"flag --quiet", "implicitEffects[0]", "positionals"}
		for _, field := range wantFields {
			if findFinding(findings, CheckCitationPresence, field) == nil {
				t.Errorf("missing citation-presence finding for field %q in %#v", field, findings)
			}
		}
	})

	t.Run("recurses into subcommands", func(t *testing.T) {
		c := minimalCommand()
		sub := minimalCommand()
		sub.Citations["provenance"] = realCite(thinCite)
		c.Subcommands = map[string]specfmt.CommandSpecV1{"sub": sub}
		findings := LintCommand("x", c, false)
		if findFinding(findings, CheckCitationPresence, "provenance") == nil {
			t.Fatalf("expected a subcommand citation-presence finding, got %#v", findings)
		}
		for _, f := range findings {
			if f.Spec != "x/sub" {
				t.Errorf("finding %#v has Spec %q, want \"x/sub\"", f, f.Spec)
			}
		}
	})
}

func TestLintCommand_DangerFlagRole(t *testing.T) {
	t.Run("value-taking danger flag with a literal role is HARD", func(t *testing.T) {
		c := minimalCommand()
		c.Flags = map[string]specfmt.FlagSpecV1{
			"--exec": {
				Arity:    "one",
				Operand:  specfmt.OperandRoleV1{Kind: "literal"},
				Citation: realCite("man x(1), --exec"),
			},
		}
		findings := LintCommand("x", c, false)
		if findFinding(findings, CheckDangerFlagRole, "flag --exec") == nil {
			t.Fatalf("expected a danger-flag-role finding, got %#v", findings)
		}
	})

	t.Run("value-taking danger flag with a real effect role is not flagged", func(t *testing.T) {
		c := minimalCommand()
		c.Flags = map[string]specfmt.FlagSpecV1{
			"--exec": {
				Arity:    "one",
				Operand:  specfmt.OperandRoleV1{Kind: "program", Dialect: "shell"},
				Citation: realCite("man x(1), --exec"),
			},
		}
		findings := LintCommand("x", c, false)
		if found := findFinding(findings, CheckDangerFlagRole, "flag --exec"); found != nil {
			t.Errorf("unexpected danger-flag-role finding: %#v", found)
		}
	})

	t.Run("boolean danger flag wired only via transform is not flagged", func(t *testing.T) {
		c := minimalCommand()
		c.Flags = map[string]specfmt.FlagSpecV1{
			"--force": {
				Arity:     "none",
				Transform: specfmt.EffectTransformV1{Kind: "force"},
				Citation:  realCite("man x(1), --force"),
			},
		}
		findings := LintCommand("x", c, false)
		if found := findFinding(findings, CheckDangerFlagRole, "flag --force"); found != nil {
			t.Errorf("unexpected danger-flag-role finding: %#v", found)
		}
	})

	t.Run("boolean danger flag with no transform and no elsewhere-participation is HARD", func(t *testing.T) {
		c := minimalCommand()
		c.Flags = map[string]specfmt.FlagSpecV1{
			"--force": {
				Arity:    "none",
				Citation: realCite("man x(1), --force"),
			},
		}
		findings := LintCommand("x", c, false)
		if findFinding(findings, CheckDangerFlagRole, "flag --force") == nil {
			t.Fatalf("expected a danger-flag-role finding, got %#v", findings)
		}
	})

	t.Run("boolean danger flag wired via RestOverride.Flags is not flagged", func(t *testing.T) {
		c := minimalCommand()
		c.Flags = map[string]specfmt.FlagSpecV1{
			"--all": {Arity: "none", Citation: realCite("man x(1), --all")},
		}
		c.Positionals.RestOverride = specfmt.RestOverrideV1{Flags: []string{"--all"}, Role: specfmt.OperandRoleV1{Kind: "literal"}}
		findings := LintCommand("x", c, false)
		if found := findFinding(findings, CheckDangerFlagRole, "flag --all"); found != nil {
			t.Errorf("unexpected danger-flag-role finding: %#v", found)
		}
	})

	t.Run("a non-danger-shaped flag with a literal role is never flagged", func(t *testing.T) {
		c := minimalCommand()
		c.Flags = map[string]specfmt.FlagSpecV1{
			"--verbose": {Arity: "none", Citation: realCite("man x(1), --verbose")},
		}
		findings := LintCommand("x", c, false)
		if found := findFinding(findings, CheckDangerFlagRole, "flag --verbose"); found != nil {
			t.Errorf("unexpected danger-flag-role finding: %#v", found)
		}
	})

	t.Run("skipped entirely for a builtin spec", func(t *testing.T) {
		c := minimalCommand()
		c.Flags = map[string]specfmt.FlagSpecV1{
			"--exec": {Arity: "one", Operand: specfmt.OperandRoleV1{Kind: "literal"}, Citation: realCite("man x(1), --exec")},
		}
		findings := LintCommand("x", c, true /* builtin */)
		if found := findFinding(findings, CheckDangerFlagRole, ""); found != nil {
			t.Errorf("expected danger-flag-role to be skipped for a builtin spec, got %#v", found)
		}
	})
}

func TestIsDangerShaped(t *testing.T) {
	cases := map[string]bool{
		"-o":                 true,
		"--output":           true,
		"--output-error":     true,
		"--exec":             true,
		"--exec-path":        true,
		"-c":                 true,
		"--config":           true,
		"--config-file":      true,
		"--command":          true,
		"-e":                 true,
		"--receive-pack":     true,
		"--upload-pack":      true,
		"--prune":            true,
		"--mirror":           true,
		"--all":              true,
		"--delete":           true,
		"-f":                 true,
		"--force":            true,
		"--force-with-lease": true,
		"-r":                 true,
		"-R":                 true,
		"--recursive":        true,
		"--upload-hook-x":    true,
		"--foo-program":      true,
		"--verbose":          false,
		"--quiet":            false,
		"-v":                 false,
	}
	for name, want := range cases {
		if got := isDangerShaped(name); got != want {
			t.Errorf("isDangerShaped(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestLintCommand_UnknownFlagInertJustification(t *testing.T) {
	t.Run("inert with a thin citation on a non-builtin spec is HARD", func(t *testing.T) {
		c := minimalCommand()
		c.UnknownFlag = "inert"
		c.Citations["unknownFlag"] = realCite(thinCite)
		findings := LintCommand("x", c, false)
		if findFinding(findings, CheckUnknownFlagInert, "unknownFlag") == nil {
			t.Fatalf("expected an unknown-flag-inert-justification finding, got %#v", findings)
		}
	})

	t.Run("inert with a real justification citation is not flagged", func(t *testing.T) {
		c := minimalCommand()
		c.UnknownFlag = "inert"
		c.Citations["unknownFlag"] = realCite("man x(1): reviewed every flag, none adds an effect")
		findings := LintCommand("x", c, false)
		if found := findFinding(findings, CheckUnknownFlagInert, ""); found != nil {
			t.Errorf("unexpected unknown-flag-inert-justification finding: %#v", found)
		}
	})

	t.Run("insufficient (not inert) is never flagged", func(t *testing.T) {
		c := minimalCommand()
		c.UnknownFlag = "insufficient"
		c.Citations["unknownFlag"] = realCite(thinCite)
		findings := LintCommand("x", c, false)
		if found := findFinding(findings, CheckUnknownFlagInert, ""); found != nil {
			t.Errorf("unexpected finding for UnknownFlagInsufficient: %#v", found)
		}
	})

	t.Run("skipped entirely for a builtin spec", func(t *testing.T) {
		c := minimalCommand()
		c.UnknownFlag = "inert"
		c.Citations["unknownFlag"] = realCite(thinCite)
		findings := LintCommand("x", c, true /* builtin */)
		if found := findFinding(findings, CheckUnknownFlagInert, ""); found != nil {
			t.Errorf("expected unknown-flag-inert check to be skipped for a builtin spec, got %#v", found)
		}
	})
}

func TestIsSkillGenerated(t *testing.T) {
	// Phase 1 documented stub: always false (see doc.go and lint.go).
	if IsSkillGenerated("anything") {
		t.Errorf("IsSkillGenerated must be a Phase-1 no-op stub returning false")
	}
}

func TestLintConflicts(t *testing.T) {
	conflicts := []specfmt.Conflict{
		{Kind: specfmt.KindCommand, Name: "ls", LosingLayer: specfmt.LayerEmbedded, LosingPath: "data/ls.json", WinningLayer: specfmt.LayerRepo, WinningPath: "ls.json"},
	}
	findings := LintConflicts(conflicts)
	if len(findings) != 1 {
		t.Fatalf("len(findings) = %d, want 1", len(findings))
	}
	if findings[0].Severity != SeverityHard {
		t.Errorf("severity = %v, want %v", findings[0].Severity, SeverityHard)
	}
	if findings[0].Check != CheckOverridesConflict {
		t.Errorf("check = %v, want %v", findings[0].Check, CheckOverridesConflict)
	}
}

func TestLintInvalid_IsWarnNotHard(t *testing.T) {
	invalid := []specfmt.InvalidSpec{
		{Layer: specfmt.LayerEmbedded, Path: "data/bash.json", Err: errFixture{"unknown dialect \"shell-file\""}},
	}
	findings := LintInvalid(invalid)
	if len(findings) != 1 {
		t.Fatalf("len(findings) = %d, want 1", len(findings))
	}
	if findings[0].Severity != SeverityWarn {
		t.Errorf("severity = %v, want %v (packet's Contract calls this surfacing optional; the loader already performed the real enforcement)", findings[0].Severity, SeverityWarn)
	}
}

type errFixture struct{ msg string }

func (e errFixture) Error() string { return e.msg }

// TestLintRepository_Embedded is a light smoke test over a synthetic
// embedded-style Repository (not the real 45 built-ins — that live corpus
// is exercised by this packet's own live/manual CLI runs, not a unit test)
// confirming builtin=true both stages citation-presence to WARN and skips
// checks 2/3 end to end through LintRepository, not just LintCommand.
func TestLintRepository_Embedded(t *testing.T) {
	spec := specfmt.Spec{
		Version: specfmt.FormatVersion,
		Kind:    specfmt.KindCommand,
		Name:    "fake",
		Command: func() *specfmt.CommandSpecV1 {
			c := minimalCommand()
			c.Name = "fake"
			c.Citations["provenance"] = realCite(thinCite)
			c.UnknownFlag = "inert"
			c.Citations["unknownFlag"] = realCite(thinCite)
			c.Flags = map[string]specfmt.FlagSpecV1{
				"--exec": {Arity: "one", Operand: specfmt.OperandRoleV1{Kind: "literal"}, Citation: realCite(thinCite)},
			}
			return &c
		}(),
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	embFS := fstest.MapFS{"fake.json": &fstest.MapFile{Data: data}}

	repo := specfmt.NewRepository(embFS, "", "")
	report, err := LintRepository(repo, true /* builtin */)
	if err != nil {
		t.Fatalf("LintRepository: %v", err)
	}
	if report.HasHard() {
		t.Errorf("builtin report has a HARD finding, want none (checks 2/3 skipped, citation-presence WARN-staged): %#v", report.Findings)
	}
	if findFinding(report.Findings, CheckCitationPresence, "provenance") == nil {
		t.Errorf("expected a WARN citation-presence finding, got %#v", report.Findings)
	}

	nonBuiltinReport, err := LintRepository(specfmt.NewRepository(embFS, "", ""), false)
	if err != nil {
		t.Fatalf("LintRepository (non-builtin): %v", err)
	}
	if !nonBuiltinReport.HasHard() {
		t.Errorf("non-builtin report over the SAME fixture has no HARD finding, want checks 2/3 to fire: %#v", nonBuiltinReport.Findings)
	}
}

// findFinding returns the first finding matching check (and, if field != "",
// matching Field too), or nil.
func findFinding(findings []Finding, check Check, field string) *Finding {
	for i := range findings {
		if findings[i].Check != check {
			continue
		}
		if field != "" && findings[i].Field != field {
			continue
		}
		return &findings[i]
	}
	return nil
}
