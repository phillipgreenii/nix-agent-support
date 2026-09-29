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

// minimalTarget builds a well-formed (passes Validate) target Spec — P8,
// docket tc-o14i5.3, packet tc-o14i5.3.4.
func minimalTarget(name string, kind TargetKind, class TargetClass) Spec {
	return Spec{
		Version: FormatVersion,
		Kind:    KindTarget,
		Name:    name,
		Target: &TargetSpecV1{
			TargetKind: kind,
			Class:      class,
			Citation:   cite("test fixture"),
		},
	}
}

// TestRepository_TargetLayeringAndCrossKindNamespacing exercises the SAME
// three-layer Repository loader Commands already used (round-trip load of
// embedded+user+repo layers, precedence, undeclared-override Conflict) for
// KindTarget specs — this packet's own "reuse, don't duplicate" contract —
// plus the one thing genuinely NEW to target specs: two different
// TargetKinds sharing a Name ("prod" as both a kube context and an ssh
// host) merge as two DISTINCT entries rather than colliding (TargetKind's
// own doc comment; repository.go's key.SubKind).
func TestRepository_TargetLayeringAndCrossKindNamespacing(t *testing.T) {
	embedded := mapFSFor(t, map[string]Spec{
		"kinfra.json": minimalTarget("kinfra", TargetKindKubeContext, TargetClassTrustedDev),
	})
	userDir := t.TempDir()
	writeSpecFile(t, userDir, "kinfra.json", minimalTarget("kinfra", TargetKindKubeContext, TargetClassProduction))
	writeSpecFile(t, userDir, "prod-ssh.json", minimalTarget("prod", TargetKindSSHHost, TargetClassProduction))
	writeSpecFile(t, userDir, "prod-kube.json", minimalTarget("prod", TargetKindKubeContext, TargetClassTrustedDev))

	repo := NewRepository(embedded, userDir, "")
	merged, err := repo.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Undeclared override: user's kinfra.json replaces embedded's without
	// Overrides set.
	if len(merged.Conflicts) != 1 {
		t.Fatalf("Conflicts = %#v, want exactly 1", merged.Conflicts)
	}
	if got := merged.Targets[TargetKey{Kind: TargetKindKubeContext, Name: "kinfra"}].Class; got != TargetClassProduction {
		t.Fatalf("kinfra kube-context class = %q, want %q (user layer wins)", got, TargetClassProduction)
	}

	// Cross-kind namespacing: "prod" as an ssh-host and as a kube-context
	// are two DISTINCT entries, neither shadowing the other and neither
	// producing a Conflict.
	sshProd, ok := merged.Targets[TargetKey{Kind: TargetKindSSHHost, Name: "prod"}]
	if !ok || sshProd.Class != TargetClassProduction {
		t.Fatalf("ssh-host %q = %#v, ok=%v, want production", "prod", sshProd, ok)
	}
	kubeProd, ok := merged.Targets[TargetKey{Kind: TargetKindKubeContext, Name: "prod"}]
	if !ok || kubeProd.Class != TargetClassTrustedDev {
		t.Fatalf("kube-context %q = %#v, ok=%v, want trusted-dev", "prod", kubeProd, ok)
	}
	if len(merged.Targets) != 3 {
		t.Fatalf("Targets = %#v, want exactly 3 entries", merged.Targets)
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
