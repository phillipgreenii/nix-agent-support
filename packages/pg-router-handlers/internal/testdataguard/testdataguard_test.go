// Package testdataguard holds a build-hygiene test: no production binary this
// module ships may depend, at compile time, on a package that embeds a
// testdata/ path.
//
// Why: the nix build of a shipped binary filters test data (testdata/ and
// _test.go files) out of the package source. A non-test package that
// //go:embed's testdata/... would then fail to compile in that build, and
// even before the filter it makes test fixtures a build input of production
// code. Fixture loaders belong in a leaf test-support package no production
// binary imports (see conformance/golden).
//
// The scope is the dependency closure of each shipped main (the package
// build's subPackages in default.nix), NOT every package in the module: the
// test-support package conformance/golden is a non-test file embedding
// testdata/ and must keep passing because no shipped main imports it.
package testdataguard

import (
	"os/exec"
	"strings"
	"testing"
)

// shippedMains lists the main packages default.nix installs (subPackages),
// relative to the module root. Keep in sync with default.nix.
var shippedMains = []string{"./cmd/pg-router-handler-ccpool", "./cmd/pg-router-handler-command"}

func TestShippedBinariesEmbedNoTestdata(t *testing.T) {
	for _, main := range shippedMains {
		t.Run(main, func(t *testing.T) {
			// One line per dependency: "<import path>\t<embed patterns>\t<embed files>".
			cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f",
				`{{.ImportPath}}{{"\t"}}{{join .EmbedPatterns ","}}{{"\t"}}{{join .EmbedFiles ","}}`, main)
			cmd.Dir = "../.."
			out, err := cmd.Output()
			if err != nil {
				var stderr string
				if ee, ok := err.(*exec.ExitError); ok {
					stderr = string(ee.Stderr)
				}
				t.Fatalf("go list -deps %s: %v\n%s", main, err, stderr)
			}
			checked := 0
			for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
				checked++
				fields := strings.Split(line, "\t")
				if len(fields) != 3 {
					t.Fatalf("unexpected go list line %q", line)
				}
				for _, embedded := range fields[1:] {
					if embedded == "" {
						continue
					}
					for _, e := range strings.Split(embedded, ",") {
						if strings.Contains(e, "testdata") {
							t.Errorf("%s depends on %s, which embeds %q: move the fixture loader into a leaf test-support package no shipped main imports", main, fields[0], e)
						}
					}
				}
			}
			if checked < 2 {
				t.Fatalf("go list -deps %s returned %d packages; expected the full closure", main, checked)
			}
		})
	}
}
