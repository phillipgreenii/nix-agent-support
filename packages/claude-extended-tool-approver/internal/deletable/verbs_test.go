package deletable

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestJustfileVerbs covers the recognized recipe-header shapes and the
// documented exclusions (comments, attribute lines, variable/alias
// assignments, indented body lines) in one scan.
func TestJustfileVerbs(t *testing.T) {
	root := scratchOutsideTemp(t)
	writeFile(t, root, "justfile", `# a leading comment: with a colon in it
set dotenv-load
version := "1.0"
alias b := build

[private]
_hidden:
    echo hidden

build target="release":
    go build ./...

test: build lint
    go test ./...

@quiet-recipe:
    echo quiet

lint
`)
	got := justfileVerbs(root)
	want := []string{"_hidden", "build", "test", "quiet-recipe"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("justfileVerbs = %v, want %v", got, want)
	}
}

// TestJustfileVerbsMarkerNames confirms every marker name `just` itself
// recognizes is discoverable, using the FIRST one found when root has only
// one.
func TestJustfileVerbsMarkerNames(t *testing.T) {
	for _, name := range []string{"justfile", "Justfile", ".justfile", ".Justfile"} {
		t.Run(name, func(t *testing.T) {
			root := scratchOutsideTemp(t)
			writeFile(t, root, name, "check:\n    echo ok\n")
			got := justfileVerbs(root)
			want := []string{"check"}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("justfileVerbs(%s) = %v, want %v", name, got, want)
			}
		})
	}
}

// TestJustfileVerbsAbsentOrMalformed: no justfile at all, and an empty
// justfile, both yield nil — never a guess.
func TestJustfileVerbsAbsentOrMalformed(t *testing.T) {
	root := scratchOutsideTemp(t)
	if got := justfileVerbs(root); got != nil {
		t.Errorf("no justfile: got %v, want nil", got)
	}
	writeFile(t, root, "justfile", "# just a comment\nset export := true\n")
	if got := justfileVerbs(root); got != nil {
		t.Errorf("justfile with no recipes: got %v, want nil", got)
	}
}

// TestPackageJSONVerbs: scripts object keys, sorted; a malformed file or
// one with no scripts object yields nil.
func TestPackageJSONVerbs(t *testing.T) {
	root := scratchOutsideTemp(t)
	writeFile(t, root, "package.json", `{"name":"x","scripts":{"test":"jest","build":"tsc","lint":"eslint ."}}`)
	got := packageJSONVerbs(root)
	want := []string{"build", "lint", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("packageJSONVerbs = %v, want %v", got, want)
	}
}

func TestPackageJSONVerbsAbsentOrMalformed(t *testing.T) {
	root := scratchOutsideTemp(t)
	if got := packageJSONVerbs(root); got != nil {
		t.Errorf("no package.json: got %v, want nil", got)
	}
	writeFile(t, root, "package.json", `{"name":"x"}`)
	if got := packageJSONVerbs(root); got != nil {
		t.Errorf("package.json with no scripts: got %v, want nil", got)
	}
	writeFile(t, root, "package.json", `not json`)
	if got := packageJSONVerbs(root); got != nil {
		t.Errorf("malformed package.json: got %v, want nil", got)
	}
}

// TestDevboxVerbs: shell.scripts keys, sorted, string and array command
// shapes both discovered by name alone.
func TestDevboxVerbs(t *testing.T) {
	root := scratchOutsideTemp(t)
	writeFile(t, root, "devbox.json", `{"packages":["go"],"shell":{"scripts":{"test":"go test ./...","ci":["go build ./...","go test ./..."]}}}`)
	got := devboxVerbs(root)
	want := []string{"ci", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("devboxVerbs = %v, want %v", got, want)
	}
}

func TestDevboxVerbsAbsentOrMalformed(t *testing.T) {
	root := scratchOutsideTemp(t)
	if got := devboxVerbs(root); got != nil {
		t.Errorf("no devbox.json: got %v, want nil", got)
	}
	writeFile(t, root, "devbox.json", `{"packages":["go"]}`)
	if got := devboxVerbs(root); got != nil {
		t.Errorf("devbox.json with no shell.scripts: got %v, want nil", got)
	}
}

// TestDiscoveredVerbs: a root with both a justfile and a package.json
// reports both kinds' VerbSets, deepest-root-first (equal depth here, so
// registry order — just before npm — is the tie-break); a subdirectory
// resolves the same ancestor root; a sibling tree with neither marker
// reports nothing.
func TestDiscoveredVerbs(t *testing.T) {
	root := scratchOutsideTemp(t)
	writeFile(t, root, "justfile", "check:\n    echo ok\n")
	writeFile(t, root, "package.json", `{"scripts":{"build":"tsc"}}`)
	mkdirs(t, root, "src")

	got := DiscoveredVerbs(DefaultKinds(), root)
	want := []VerbSet{
		{Kind: "just", Root: root, Verbs: []string{"check"}},
		{Kind: "npm", Root: root, Verbs: []string{"build"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoveredVerbs(root) = %+v, want %+v", got, want)
	}

	// A subdirectory resolves the same ancestor root.
	gotSub := DiscoveredVerbs(DefaultKinds(), filepath.Join(root, "src"))
	if !reflect.DeepEqual(gotSub, want) {
		t.Errorf("DiscoveredVerbs(root/src) = %+v, want %+v", gotSub, want)
	}

	// A sibling tree with neither marker discovers nothing.
	other := scratchOutsideTemp(t)
	if got := DiscoveredVerbs(DefaultKinds(), other); len(got) != 0 {
		t.Errorf("DiscoveredVerbs(no markers) = %+v, want empty", got)
	}
}

// TestDiscoveredVerbsEmptyFileContributesNoVerbSet: a marker file that
// discovers zero verbs (e.g. a justfile with no recipes) contributes no
// VerbSet at all — "Silent means absent from the result", matching
// Resolve's CatSilent / NonSecretWith's skip-on-no-opinion convention.
func TestDiscoveredVerbsEmptyFileContributesNoVerbSet(t *testing.T) {
	root := scratchOutsideTemp(t)
	writeFile(t, root, "justfile", "# no recipes here\n")
	if got := DiscoveredVerbs(DefaultKinds(), root); len(got) != 0 {
		t.Errorf("DiscoveredVerbs(empty justfile) = %+v, want empty", got)
	}
}

// TestJustNpmDevboxKindsSilentOnPathClassification: the three new kinds
// declare no Rules/Classify, so they must never surface as the DECIDING
// kind for Resolve — Resolve stays Silent for a path under a justfile-only
// root with no other kind present.
func TestJustNpmDevboxKindsSilentOnPathClassification(t *testing.T) {
	root := scratchOutsideTemp(t)
	writeFile(t, root, "justfile", "check:\n    echo ok\n")
	touch(t, root, "src/main.go")
	res := Resolve(DefaultKinds(), filepath.Join(root, "src", "main.go"))
	if res.Category != CatSilent {
		t.Errorf("Resolve under justfile-only root = %+v, want CatSilent", res)
	}
}
