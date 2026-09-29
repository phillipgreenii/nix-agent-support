package specfmt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// minimalCommand builds a well-formed (passes Validate) command Spec with
// the given name and provenance, and no other content — the tests below
// only care about layering/override/conflict behavior, not the command's
// own fields.
func minimalCommand(name, provenance string, overrides bool) Spec {
	return Spec{
		Version:   FormatVersion,
		Kind:      KindCommand,
		Name:      name,
		Overrides: overrides,
		Command: &CommandSpecV1{
			Name:       name,
			Provenance: provenance,
			Citations: map[string]Citation{
				citationKeyProvenance:  cite("test fixture"),
				citationKeyStdin:       cite("test fixture"),
				citationKeyStdout:      cite("test fixture"),
				citationKeyUnknownFlag: cite("test fixture"),
			},
			Positionals: PositionalSpecV1{Citation: cite("test fixture")},
			Stdin:       stdinNever,
			Stdout:      stdoutNone,
			UnknownFlag: unknownFlagInsuf,
		},
	}
}

func writeSpecFile(t *testing.T, dir, filename string, spec Spec) {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal fixture spec: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

func mapFSFor(t *testing.T, specs map[string]Spec) fstest.MapFS {
	t.Helper()
	fs := fstest.MapFS{}
	for filename, spec := range specs {
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatalf("marshal embedded fixture %s: %v", filename, err)
		}
		fs[filename] = &fstest.MapFile{Data: data}
	}
	return fs
}

func TestRepository_LayeringPrecedenceAndUndeclaredOverride(t *testing.T) {
	embedded := mapFSFor(t, map[string]Spec{
		"echo.json":          minimalCommand("echo", "embedded echo 1.0", false),
		"only-embedded.json": minimalCommand("only-embedded", "embedded only", false),
	})

	userDir := t.TempDir()
	writeSpecFile(t, userDir, "echo.json", minimalCommand("echo", "user echo 1.0", true))

	repoDir := t.TempDir()
	writeSpecFile(t, repoDir, "echo.json", minimalCommand("echo", "repo echo 1.0", false))

	repo := NewRepository(embedded, userDir, repoDir)
	merged, err := repo.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := merged.Commands["echo"].Provenance; got != "repo echo 1.0" {
		t.Fatalf("echo provenance = %q, want repo-layer value (repo has highest precedence)", got)
	}
	if got := merged.Commands["only-embedded"].Provenance; got != "embedded only" {
		t.Fatalf("only-embedded provenance = %q, want embedded value (untouched by other layers)", got)
	}

	if len(merged.Conflicts) != 1 {
		t.Fatalf("Conflicts = %#v, want exactly 1 (repo overriding user's echo without declaring Overrides)", merged.Conflicts)
	}
	c := merged.Conflicts[0]
	if c.Name != "echo" || c.WinningLayer != LayerRepo || c.LosingLayer != LayerUser {
		t.Fatalf("unexpected conflict shape: %#v", c)
	}
}

func TestRepository_DeclaredOverrideNoConflict(t *testing.T) {
	embedded := mapFSFor(t, map[string]Spec{
		"echo.json": minimalCommand("echo", "embedded echo 1.0", false),
	})
	userDir := t.TempDir()
	writeSpecFile(t, userDir, "echo.json", minimalCommand("echo", "user echo 1.0", true))

	repo := NewRepository(embedded, userDir, "")
	merged, err := repo.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(merged.Conflicts) != 0 {
		t.Fatalf("Conflicts = %#v, want none (user declared Overrides)", merged.Conflicts)
	}
	if got := merged.Commands["echo"].Provenance; got != "user echo 1.0" {
		t.Fatalf("echo provenance = %q, want user-layer value", got)
	}
}

func TestRepository_MissingLayerDirsAreEmpty(t *testing.T) {
	repo := NewRepository(nil, filepath.Join(t.TempDir(), "does-not-exist"), filepath.Join(t.TempDir(), "also-missing"))
	merged, err := repo.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(merged.Commands) != 0 || len(merged.Conflicts) != 0 || len(merged.Invalid) != 0 {
		t.Fatalf("expected an entirely empty MergedSet, got %#v", merged)
	}
}

func TestRepository_InvalidSpecExcludedNotSilentlyMerged(t *testing.T) {
	bad := minimalCommand("bad", "bad tool 1.0", false)
	bad.Command.Interpreter = "not-a-real-interpreter"
	// Interpreter citation intentionally omitted too, so this spec fails
	// Validate for two independent reasons.

	good := minimalCommand("good", "good tool 1.0", false)

	embedded := mapFSFor(t, map[string]Spec{
		"bad.json":  bad,
		"good.json": good,
	})

	repo := NewRepository(embedded, "", "")
	merged, err := repo.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := merged.Commands["bad"]; ok {
		t.Fatalf("invalid spec %q was merged into Commands, want excluded", "bad")
	}
	if _, ok := merged.Commands["good"]; !ok {
		t.Fatalf("valid spec %q was NOT merged into Commands", "good")
	}
	if len(merged.Invalid) != 1 || merged.Invalid[0].Path != "bad.json" {
		t.Fatalf("Invalid = %#v, want exactly one entry for bad.json", merged.Invalid)
	}
}
