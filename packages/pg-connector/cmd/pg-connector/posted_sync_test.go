package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The posted-review sidecar package exists twice, on purpose. The old backend
// (pg-connector-pr-github) owns it under its own internal/ tree, and the
// daemon-backed binary (pg-connector-github) carries a COPY, because Go's
// internal/ rule stops the new binary importing the old tree and the layout
// convention (layout_convention_test.go) allows only generic, interface-level
// packages to be shared. Until the cleanup phase moves the records into the
// daemon's store, the sidecar files and their flock are the single source of
// truth for both binaries, so a rollback to the old binary sees every review the
// daemon posted. A copy that drifted would write or read a record the other
// binary cannot, silently losing the "already posted" memory in one direction.
// This guard is what keeps the two copies from drifting.
//
// Unlike the content-hash pins of backend_internal_sync_test.go, whose copies
// legitimately differ, these copies MUST be byte-identical: the new directory's
// non-test .go files are the old directory's, file for file. Test files are not
// compared (the new copy adds its own), and neither are non-Go files. A change
// to one copy MUST be made to the other in the SAME change; the cleanup phase
// that retires the old backend deletes this guard with it.
const (
	postedOldDir = "cmd/pg-connector-pr-github/internal/posted"
	postedNewDir = "cmd/pg-connector-github/internal/posted"
)

// postedNonTestGoFiles returns the sorted names of the non-test .go files that
// sit directly in dir.
func postedNonTestGoFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// firstDifference is the offset of the first byte at which a and b differ (the
// length of the shorter one when it is a prefix of the other).
func firstDifference(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// comparePostedCopies compares every non-test .go file of the two package
// directories byte for byte, in name order, and returns an error naming the
// FIRST file that differs (or that only one directory has). It returns nil only
// when both directories hold the same set of files with the same bytes, and it
// refuses to pass vacuously when neither holds any.
func comparePostedCopies(dirA, dirB string) error {
	namesA, err := postedNonTestGoFiles(dirA)
	if err != nil {
		return fmt.Errorf("list %s: %w", dirA, err)
	}
	namesB, err := postedNonTestGoFiles(dirB)
	if err != nil {
		return fmt.Errorf("list %s: %w", dirB, err)
	}
	seen := map[string]bool{}
	var names []string
	for _, n := range append(append([]string{}, namesA...), namesB...) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("neither %s nor %s holds a non-test .go file, so the comparison would pass vacuously", dirA, dirB)
	}
	for _, name := range names {
		a, errA := os.ReadFile(filepath.Join(dirA, name))
		b, errB := os.ReadFile(filepath.Join(dirB, name))
		switch {
		case errors.Is(errA, os.ErrNotExist):
			return fmt.Errorf("%s exists in %s but not in %s", name, dirB, dirA)
		case errors.Is(errB, os.ErrNotExist):
			return fmt.Errorf("%s exists in %s but not in %s", name, dirA, dirB)
		case errA != nil:
			return fmt.Errorf("read %s: %w", filepath.Join(dirA, name), errA)
		case errB != nil:
			return fmt.Errorf("read %s: %w", filepath.Join(dirB, name), errB)
		case !bytes.Equal(a, b):
			return fmt.Errorf("%s differs between %s (%d bytes) and %s (%d bytes); the first difference is at byte offset %d",
				name, dirA, len(a), dirB, len(b), firstDifference(a, b))
		}
	}
	return nil
}

// writePostedFixture writes files (name to content) into a fresh temporary
// directory and returns it.
func writePostedFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// TestPostedSidecarCopiesAreByteIdentical fails, naming the first file that
// differs, when either copy of the posted-review sidecar package changes without
// the other. The sub-tests prove the comparison itself can fail: the real trees
// are identical today, so on their own they could never show it.
func TestPostedSidecarCopiesAreByteIdentical(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	oldDir := filepath.Join(moduleRoot, filepath.FromSlash(postedOldDir))
	newDir := filepath.Join(moduleRoot, filepath.FromSlash(postedNewDir))
	if err := comparePostedCopies(oldDir, newDir); err != nil {
		t.Errorf("the posted-review sidecar copies have drifted: %v\n"+
			"Both copies MUST stay byte-identical: apply the same change to %s and %s in the SAME change.\n"+
			"A copy that differs reads or writes records the other binary cannot, and a rollback to the old binary would lose the daemon's posted-review memory.",
			err, postedOldDir, postedNewDir)
	}

	base := map[string]string{
		"alpha.go":   "package posted\n\nconst A = 1\n",
		"bravo.go":   "package posted\n\nconst B = 2\n",
		"charlie.go": "package posted\n\nconst C = 3\n",
	}
	clone := func(overrides map[string]string, drop ...string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range overrides {
			out[k] = v
		}
		for _, k := range drop {
			delete(out, k)
		}
		return out
	}

	t.Run("identical directories pass", func(t *testing.T) {
		if err := comparePostedCopies(writePostedFixture(t, base), writePostedFixture(t, clone(nil))); err != nil {
			t.Fatalf("identical directories must pass: %v", err)
		}
	})

	t.Run("one file differing by one byte fails naming that file", func(t *testing.T) {
		a := writePostedFixture(t, base)
		// Same length, one byte changed: "const B = 2" becomes "const B = 3".
		b := writePostedFixture(t, clone(map[string]string{"bravo.go": "package posted\n\nconst B = 3\n"}))
		err := comparePostedCopies(a, b)
		if err == nil {
			t.Fatal("a one-byte difference must fail the comparison")
		}
		if !strings.Contains(err.Error(), "bravo.go") {
			t.Errorf("the failure must name the differing file bravo.go, got: %v", err)
		}
		for _, other := range []string{"alpha.go", "charlie.go"} {
			if strings.Contains(err.Error(), other) {
				t.Errorf("the failure must not name the identical file %s, got: %v", other, err)
			}
		}
	})

	t.Run("a missing trailing byte fails", func(t *testing.T) {
		a := writePostedFixture(t, base)
		b := writePostedFixture(t, clone(map[string]string{"charlie.go": "package posted\n\nconst C = 3"}))
		if err := comparePostedCopies(a, b); err == nil || !strings.Contains(err.Error(), "charlie.go") {
			t.Fatalf("a dropped final newline must fail naming charlie.go, got: %v", err)
		}
	})

	t.Run("the first differing file in name order is the one named", func(t *testing.T) {
		a := writePostedFixture(t, base)
		b := writePostedFixture(t, clone(map[string]string{
			"charlie.go": "package posted\n\nconst C = 4\n",
			"bravo.go":   "package posted\n\nconst B = 4\n",
		}))
		err := comparePostedCopies(a, b)
		if err == nil || !strings.Contains(err.Error(), "bravo.go") || strings.Contains(err.Error(), "charlie.go") {
			t.Fatalf("want a failure naming bravo.go only, got: %v", err)
		}
	})

	t.Run("a file only one directory has fails naming it", func(t *testing.T) {
		withExtra := clone(map[string]string{"delta.go": "package posted\n"})
		for name, tc := range map[string]struct{ a, b map[string]string }{
			"extra in the second":  {base, withExtra},
			"extra in the first":   {withExtra, base},
			"missing from a first": {clone(nil, "alpha.go"), base},
		} {
			err := comparePostedCopies(writePostedFixture(t, tc.a), writePostedFixture(t, tc.b))
			want := "delta.go"
			if name == "missing from a first" {
				want = "alpha.go"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: want a failure naming %s, got: %v", name, want, err)
			}
		}
	})

	t.Run("test files and non-Go files are not compared", func(t *testing.T) {
		a := writePostedFixture(t, clone(map[string]string{"alpha_test.go": "package posted\n// one\n", "NOTES.md": "one\n"}))
		b := writePostedFixture(t, clone(map[string]string{"alpha_test.go": "package posted\n// two\n", "other_test.go": "package posted\n"}))
		if err := comparePostedCopies(a, b); err != nil {
			t.Fatalf("only the non-test .go files are compared: %v", err)
		}
	})

	t.Run("an empty or missing directory does not pass vacuously", func(t *testing.T) {
		if err := comparePostedCopies(t.TempDir(), t.TempDir()); err == nil {
			t.Error("two empty directories must not pass")
		}
		if err := comparePostedCopies(writePostedFixture(t, base), filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Error("a missing directory must fail")
		}
	})
}
