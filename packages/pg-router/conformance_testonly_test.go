package pgrouter_test

import (
	"sort"
	"strings"
	"testing"
)

// The conformance packages are test support: only tests (and the conformance
// subtree itself, whose driver is the harness) import them. Production code
// reaches the shared exit codes and lifecycle types through api/participant and
// schema checking through schemas.
func TestConformanceIsImportedOnlyByTests(t *testing.T) {
	var bad []string
	for _, p := range listPackages(t) {
		from, ok := rel(p.ImportPath)
		if !ok || under(from, "conformance") {
			continue
		}
		for _, imp := range p.Imports { // Imports excludes test imports.
			if to, in := rel(imp); in && under(to, "conformance") {
				bad = append(bad, p.ImportPath+" imports "+imp)
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("production code imports a conformance package:\n%s", strings.Join(bad, "\n"))
	}
}
