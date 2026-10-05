package embeddedspecs

import (
	"os"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
)

// dataDir is this package's own data/ directory, relative to this package's
// directory (the same relative-path convention internal/legacyextract's
// extract_test.go uses for its own checked-in output paths).
const dataDir = "data"

// embeddedSpecsWriteEnv gates (re)writing the checked-in data/*.json files,
// mirroring internal/legacyextract's legacyExtractWriteEnv convention
// exactly (see that const's own doc comment for the full rationale: a bare
// `go test ./...` -- this repo's pre-commit `run-unit-tests` hook runs
// exactly that -- must not mutate tracked files as a side effect, since
// doing so would fight the same commit's treefmt/prek hook reformatting the
// generated JSON differently from json.MarshalIndent's own output).
// Regenerating is therefore an explicit, opt-in action:
// `CETA_EMBEDDEDSPECS_WRITE=1 go test ./internal/embeddedspecs/... -run
// TestGenerate`, or `go run ./cmd/genspecs` -- either way, run this repo's
// formatter/prek over data/ afterward before committing.
const embeddedSpecsWriteEnv = "CETA_EMBEDDEDSPECS_WRITE"

// TestGenerate is this packet's (tc-o14i5.2.2) own re-runnable generator
// entry point (the packet's Contract requires "a documented, re-runnable
// generation path ... for regenerating the embedded layer from a future
// DefaultRegistry() change" -- a go generate directive, a one-off go run
// tool, or a test helper all satisfy this; this repo already has a sibling
// precedent for the test-helper shape, internal/legacyextract's
// TestLegacyExtract, which this test mirrors). It always builds and sanity
// checks the specs; it writes them to disk only when embeddedSpecsWriteEnv
// is set. See cmd/genspecs for the non-test-runner equivalent.
func TestGenerate(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	names := reg.Names()

	specs, err := BuildSpecs(reg)
	if err != nil {
		t.Fatalf("BuildSpecs: %v", err)
	}
	if len(specs) != len(names) {
		t.Fatalf("BuildSpecs produced %d specs, want %d (DefaultRegistry().Names())", len(specs), len(names))
	}
	for _, name := range names {
		spec, ok := specs[name]
		if !ok {
			t.Errorf("BuildSpecs missing entry for %q", name)
			continue
		}
		if spec.Command == nil {
			t.Errorf("spec %q: Command is nil", name)
		}
	}

	if os.Getenv(embeddedSpecsWriteEnv) != "" {
		prior, err := LoadSpecs(dataDir)
		if err != nil {
			t.Fatalf("LoadSpecs: %v", err)
		}
		specs, err = BuildSpecsPreserving(reg, prior)
		if err != nil {
			t.Fatalf("BuildSpecsPreserving: %v", err)
		}
		if err := WriteSpecs(dataDir, specs); err != nil {
			t.Fatalf("WriteSpecs: %v", err)
		}
		t.Logf("embeddedspecs: wrote %d spec files to %s", len(specs), dataDir)
	} else {
		t.Logf("embeddedspecs: %s not set -- skipping (re)write of %s/*.json (see embeddedSpecsWriteEnv doc comment)", embeddedSpecsWriteEnv, dataDir)
	}
}

// TestBuildSpecsPreservingKeepsRealCitations: regenerating must re-marshal the
// registry's facts WITHOUT discarding the real per-fact citations already on
// disk (tc-o14i5.4.3's back-fill, or a ceta-spec-gen author's work) — only a
// fact with no prior real citation comes out thin. pg2-cjfpy.2: the generator
// used to stamp every fact thin and remove the whole data directory, so the
// documented regeneration step destroyed the back-fill and help-hashes.
func TestBuildSpecsPreservingKeepsRealCitations(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	prior, err := LoadSpecs(dataDir)
	if err != nil {
		t.Fatalf("LoadSpecs: %v", err)
	}
	if len(prior) != len(reg.Names()) {
		t.Fatalf("LoadSpecs found %d specs, want %d (one per registry name)", len(prior), len(reg.Names()))
	}
	got, err := BuildSpecsPreserving(reg, prior)
	if err != nil {
		t.Fatalf("BuildSpecsPreserving: %v", err)
	}
	for name, sp := range got {
		old := prior[name]
		if sp.Command.Citations["provenance"] != old.Command.Citations["provenance"] {
			t.Errorf("%s: provenance citation changed by a no-op regeneration:\n got %+v\nwant %+v", name, sp.Command.Citations["provenance"], old.Command.Citations["provenance"])
		}
		for flag, f := range sp.Command.Flags {
			if isThinCitation(f.Citation) {
				t.Errorf("%s flag %s: regeneration left a thin citation (the checked-in data has a real one)", name, flag)
			}
		}
	}
}

// TestWriteSpecsNeverRemovesHelpHashes: the committed help-hashes*.json files
// are not spec files and are owned by `spec-drift-check --record`; WriteSpecs
// removes only STALE command-spec files.
func TestWriteSpecsNeverRemovesHelpHashes(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"help-hashes.json", "help-hashes.darwin.json"} {
		if err := os.WriteFile(dir+"/"+f, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := subsetRegistry(t, "jq", "cat")
	specs, err := BuildSpecs(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSpecs(dir, specs); err != nil {
		t.Fatalf("WriteSpecs: %v", err)
	}
	// A second run with a smaller registry removes the stale spec file but not the hashes.
	smaller, err := BuildSpecs(subsetRegistry(t, "jq"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSpecs(dir, smaller); err != nil {
		t.Fatalf("WriteSpecs (smaller): %v", err)
	}
	for _, f := range []string{"help-hashes.json", "help-hashes.darwin.json", "jq.json"} {
		if _, err := os.Stat(dir + "/" + f); err != nil {
			t.Errorf("%s was removed: %v", f, err)
		}
	}
	if _, err := os.Stat(dir + "/cat.json"); !os.IsNotExist(err) {
		t.Errorf("stale cat.json still present (err=%v)", err)
	}
}

// subsetRegistry builds a registry of just the named built-in schemas.
func subsetRegistry(t *testing.T, names ...string) cmddesc.Registry {
	t.Helper()
	var schemas []cmddesc.CommandSchema
	for _, n := range names {
		s, ok := cmddesc.DefaultRegistry().Lookup(n)
		if !ok {
			t.Fatalf("no built-in schema %q", n)
		}
		schemas = append(schemas, s)
	}
	return cmddesc.NewRegistry(schemas...)
}
