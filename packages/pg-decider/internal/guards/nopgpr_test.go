package guards

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardedPackageDirs are the decision-flow packages that MUST NOT depend on
// packages/pg-pr (design guard G8: the flow MUST NOT depend on pg-pr).
// packages/pg-router-source-pg-desk is included because the Phase 7
// new-binary ruling put it in the flow.
var guardedPackageDirs = []string{
	"packages/pg-router",
	"packages/pg-router-source-pg-desk",
	"packages/pg-desk",
	"packages/pg-decider",
}

// findRepoRoot walks up from start to the directory holding flake.nix. It
// returns "" when none is found.
func findRepoRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "flake.nix")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// TestNoPackageImportsPgPr is guard G8: no Go package under the decision-flow
// packages imports packages/pg-pr, and no go.mod there requires or replaces
// it. It inspects real import declarations and go.mod directives, never raw
// text, because the string "pg-pr" legitimately occurs in comments and string
// literals of those trees.
func TestNoPackageImportsPgPr(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := findRepoRoot(wd)
	if root == "" {
		t.Skip("flake.nix not found above the test directory: this guard runs in the " +
			"repository checkout (commit-time and pre-land hooks), not inside the isolated nix build")
	}
	res, err := ScanForPgPr(root, guardedPackageDirs)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.GoFiles == 0 || res.GoMods == 0 {
		t.Fatalf("vacuous scan: %d .go files and %d go.mod files inspected under %v",
			res.GoFiles, res.GoMods, guardedPackageDirs)
	}
	for _, v := range res.Violations {
		t.Errorf("%s: %s", v.File, v.Detail)
	}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const modPath = "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr"

// TestScanForPgPrHasTeeth proves the scanner reports a real violation and
// ignores pg-pr mentions that are not imports or go.mod directives.
func TestScanForPgPrHasTeeth(t *testing.T) {
	cases := []struct {
		name      string
		files     map[string]string
		wantFiles []string // substrings expected in violation file names; nil = none
	}{
		{
			name: "go import of pg-pr module path",
			files: map[string]string{
				"packages/a/go.mod": "module example.test/a\n\ngo 1.25.0\n",
				"packages/a/a.go":   "package a\n\nimport _ \"" + modPath + "/internal/store\"\n",
			},
			wantFiles: []string{"packages/a/a.go"},
		},
		{
			name: "grouped aliased import of pg-pr",
			files: map[string]string{
				"packages/a/go.mod": "module example.test/a\n",
				"packages/a/sub/a.go": "package sub\n\nimport (\n\t\"fmt\"\n\tpr \"" +
					modPath + "\"\n)\n\nvar _ = fmt.Sprint\n",
			},
			wantFiles: []string{"packages/a/sub/a.go"},
		},
		{
			name: "pg-pr only in comment and string",
			files: map[string]string{
				"packages/a/go.mod": "module example.test/a\n\n// see packages/pg-pr for history\n",
				"packages/a/a.go": "// Package a is ported from packages/pg-pr.\npackage a\n\n" +
					"// import \"" + modPath + "\"\nconst note = \"" + modPath + "\"\n",
			},
		},
		{
			name: "go.mod require of pg-pr",
			files: map[string]string{
				"packages/a/go.mod": "module example.test/a\n\nrequire " + modPath + " v0.0.0\n",
			},
			wantFiles: []string{"packages/a/go.mod"},
		},
		{
			name: "go.mod replace block pointing at pg-pr directory",
			files: map[string]string{
				"packages/a/go.mod": "module example.test/a\n\nreplace (\n\texample.test/x => ../pg-pr\n)\n",
			},
			wantFiles: []string{"packages/a/go.mod"},
		},
		{
			name: "go.mod comment and unrelated module only",
			files: map[string]string{
				"packages/a/go.mod": "module example.test/a\n\nrequire example.test/pg-prx v1.0.0 // not pg-pr\n",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tc.files {
				write(t, root, rel, content)
			}
			res, err := ScanForPgPr(root, []string{"packages/a"})
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.wantFiles) == 0 {
				if len(res.Violations) != 0 {
					t.Fatalf("want no violations, got %+v", res.Violations)
				}
				return
			}
			if len(res.Violations) != len(tc.wantFiles) {
				t.Fatalf("want %d violation(s), got %+v", len(tc.wantFiles), res.Violations)
			}
			for i, want := range tc.wantFiles {
				if !strings.Contains(filepath.ToSlash(res.Violations[i].File), want) {
					t.Errorf("violation %d file = %q, want it to contain %q", i, res.Violations[i].File, want)
				}
			}
		})
	}
}

// TestScanForPgPrMissingRoot makes a missing guarded directory an error
// rather than a silent pass.
func TestScanForPgPrMissingRoot(t *testing.T) {
	if _, err := ScanForPgPr(t.TempDir(), []string{"packages/absent"}); err == nil {
		t.Fatal("want an error for a missing guarded directory")
	}
}

func TestFindRepoRoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "flake.nix", "{}\n")
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findRepoRoot(deep); got != root {
		t.Fatalf("findRepoRoot = %q, want %q", got, root)
	}
}
