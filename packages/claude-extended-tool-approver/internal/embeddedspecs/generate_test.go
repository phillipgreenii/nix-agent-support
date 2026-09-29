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
		if err := WriteSpecs(dataDir, specs); err != nil {
			t.Fatalf("WriteSpecs: %v", err)
		}
		t.Logf("embeddedspecs: wrote %d spec files to %s", len(specs), dataDir)
	} else {
		t.Logf("embeddedspecs: %s not set -- skipping (re)write of %s/*.json (see embeddedSpecsWriteEnv doc comment)", embeddedSpecsWriteEnv, dataDir)
	}
}
