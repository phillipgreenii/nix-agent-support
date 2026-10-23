package store

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// packageDirEntries lists the names directly inside dir, sorted.
func packageDirEntries(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names, nil
}

// strayEntries is every name in after that was not in before.
func strayEntries(before, after []string) []string {
	var out []string
	for _, n := range after {
		if !slices.Contains(before, n) {
			out = append(out, n)
		}
	}
	return out
}

// TestMain is the guard against a test leaking a file into the package
// directory (the test binary runs with it as the working directory). A
// database opened through a mis-built DSN once left a file named after the
// DSN's query string there (pg2-her14), which blocked removing the worktree.
// Every test uses t.TempDir for its files, so the directory MUST look the
// same after the run as before it.
func TestMain(m *testing.M) {
	before, err := packageDirEntries(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "store tests: list package dir:", err)
		os.Exit(1)
	}
	code := m.Run()
	after, err := packageDirEntries(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "store tests: list package dir:", err)
		os.Exit(1)
	}
	if stray := strayEntries(before, after); len(stray) > 0 {
		fmt.Fprintf(os.Stderr, "store tests leaked files into the package directory: %q\n", stray)
		code = 1
	}
	os.Exit(code)
}

func TestStrayEntriesReportsOnlyNewNames(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before, err := packageDirEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strayEntries(before, before); len(got) != 0 {
		t.Errorf("unchanged dir reported %q", got)
	}
	leak := "&_pragma=synchronous(OFF)"
	if err := os.WriteFile(filepath.Join(dir, leak), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "a.go")); err != nil {
		t.Fatal(err)
	}
	after, err := packageDirEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strayEntries(before, after); !slices.Equal(got, []string{leak}) {
		t.Errorf("strayEntries = %q, want only %q (a removed file is not a stray)", got, leak)
	}
}

func TestOpenRefusesEmptyPath(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	s, _, err := Open("", fastOpts())
	if err == nil {
		_ = s.Close()
		t.Fatal("Open(\"\") succeeded; it would open an anonymous database")
	}
	if left, _ := packageDirEntries(cwd); len(left) != 0 {
		t.Errorf("Open(\"\") left %q in the working directory", left)
	}
}

// TestOpenTreatsQueryCharactersInPathAsFileName proves a '?', '#' or '%' in
// the path is part of the file name, not the start of the DSN's query or
// fragment: the database lands at exactly that path and nothing else is
// created next to it or in the working directory.
func TestOpenTreatsQueryCharactersInPathAsFileName(t *testing.T) {
	for _, name := range []string{"has?query.db", "has#fragment.db", "has%41escape.db", "a?b&_pragma=synchronous(OFF).db"} {
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			t.Chdir(cwd)
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			s, _ := mustOpen(t, path, fastOpts())
			if err := s.Checkpoint(); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("database not at %s: %v", path, err)
			}
			if left, _ := packageDirEntries(cwd); len(left) != 0 {
				t.Errorf("Open left %q in the working directory", left)
			}
			ents, err := packageDirEntries(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range ents {
				if !strings.HasPrefix(n, name) {
					t.Errorf("unexpected file %q next to the database", n)
				}
			}
		})
	}
}

func TestUriPathEscapesOnlyReservedCharacters(t *testing.T) {
	if got, want := uriPath("/tmp/a b/c.db"), "/tmp/a b/c.db"; got != want {
		t.Errorf("plain path changed: %q", got)
	}
	if got, want := uriPath("/x/%?#"), "/x/%25%3f%23"; got != want {
		t.Errorf("uriPath = %q, want %q", got, want)
	}
}
