package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ampleFreeBytes is the injected free-space figure for tests that open with
// Rebuild set but are not testing the preflight itself, so the unit gate never
// reads the host's real free disk space.
func ampleFreeBytes(string) (int64, error) { return 1 << 40, nil }

// TestRebuildTestsInjectFreeBytes guards the seam: every test source line that
// opens with Rebuild: true must also set FreeBytes.
func TestRebuildTestsInjectFreeBytes(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(files))
	}
	re := regexp.MustCompile(`Rebuild:\s*true`)
	for _, f := range files {
		if f == "freebytes_test.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if re.MatchString(line) && !strings.Contains(line, "FreeBytes:") {
				t.Errorf("%s:%d opens with Rebuild: true without injecting FreeBytes", f, i+1)
			}
		}
	}
}
