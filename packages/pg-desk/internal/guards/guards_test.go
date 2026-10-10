package guards

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minScannedFiles is the non-vacuity floor for the real scan. pg-desk has well
// over this many non-test .go files; a scan that sees fewer is looking at the
// wrong directory.
const minScannedFiles = 100

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

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := FindModuleRoot(wd)
	if root == "" {
		t.Fatalf("no go.mod found above %s: the guard walks to the module's own go.mod and MUST NOT skip", wd)
	}
	return root
}

// TestNoDecisionLogicInPgDesk is guard G5: pg-desk production code carries no
// decision logic (no focus-item or dedup_key literal, no exec of bd, no
// connector write verb). It walks to the module's own go.mod (never
// flake.nix), so it RUNS under the nix build instead of skipping.
func TestNoDecisionLogicInPgDesk(t *testing.T) {
	root := moduleRoot(t)
	res, err := ScanDecisionLogic(root, PreCutoverAllowlist)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	t.Logf("scanned %d non-test .go files under %s (floor %d); %d allowlisted pre-cutover violation(s)",
		res.GoFiles, root, minScannedFiles, len(res.Allowed))
	if res.GoFiles <= minScannedFiles {
		t.Fatalf("vacuous scan: %d non-test .go files under %s, want more than %d", res.GoFiles, root, minScannedFiles)
	}
	for _, v := range res.Violations {
		t.Errorf("%s:%d: %s", v.File, v.Line, v.Detail)
	}
	// Every allowlist entry must still earn its place: a stale entry means the
	// cutover removed the file and the entry (and its comment) must go too.
	hit := map[string]bool{}
	for _, v := range res.Allowed {
		hit[v.File] = true
	}
	for _, a := range PreCutoverAllowlist {
		if !hit[a] {
			t.Errorf("allowlist entry %s no longer violates G5: delete it from PreCutoverAllowlist", a)
		}
	}
}

// scanFiles scans a synthetic module tree built from files.
func scanFiles(t *testing.T, files map[string]string, allow []string) ScanResult {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/m\n")
	for rel, content := range files {
		write(t, root, rel, content)
	}
	res, err := ScanDecisionLogic(root, allow)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestG5GuardCatchesFocusItemLiteral plants the focus-item literal in a
// non-test file and expects a violation; removing the planted file makes the
// scan clean.
func TestG5GuardCatchesFocusItemLiteral(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/m\n")
	write(t, root, "internal/ok/ok.go", "package ok\n\nconst Kind = \"focus\"\n")
	planted := "internal/planted/planted.go"
	write(t, root, planted, "package planted\n\nconst Kind = \"focus-item\"\n")

	res, err := ScanDecisionLogic(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) != 1 || res.Violations[0].File != planted ||
		!strings.Contains(res.Violations[0].Detail, "focus-item") {
		t.Fatalf("want exactly one focus-item violation in %s, got %+v", planted, res.Violations)
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(planted))); err != nil {
		t.Fatal(err)
	}
	res, err = ScanDecisionLogic(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("scan after removing the planted file: want clean, got %+v", res.Violations)
	}
}

// TestG5GuardCatchesWriteVerbExec plants an exec of a connector write verb and
// of bd, and expects a violation for each.
func TestG5GuardCatchesWriteVerbExec(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"exec call args", "package p\n\nimport \"os/exec\"\n\nfunc f() { _ = exec.Command(\"pg-connector\", \"issue\", \"create\", \"--title\", \"x\") }\n", `"issue", "create"`},
		{"argv slice update", "package p\n\nvar argv = []string{\"issue\", \"update\", \"ID\"}\n", `"issue", "update"`},
		{"argv slice comment", "package p\n\nvar argv = []string{\"issue\", \"comment\", \"ID\"}\n", `"issue", "comment"`},
		{"argv slice close", "package p\n\nvar argv = []string{\"issue\", \"close\", \"ID\"}\n", `"issue", "close"`},
		{"exec of bd", "package p\n\nimport \"os/exec\"\n\nfunc f() { _ = exec.Command(\"bd\", \"list\") }\n", `"bd"`},
		{"exec of bd via context", "package p\n\nimport (\n\t\"context\"\n\t\"os/exec\"\n)\n\nfunc f(ctx context.Context) { _ = exec.CommandContext(ctx, \"bd\", \"list\") }\n", `"bd"`},
		{"dedup_key literal", "package p\n\nconst k = \"dedup_key\"\n", "dedup_key"},
		{"raw string literal", "package p\n\nconst k = `the focus-item kind`\n", "focus-item"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := scanFiles(t, map[string]string{"internal/p/p.go": tc.src}, nil)
			if len(res.Violations) != 1 || !strings.Contains(res.Violations[0].Detail, tc.want) {
				t.Fatalf("want one violation mentioning %s, got %+v", tc.want, res.Violations)
			}
		})
	}
}

// TestG5GuardAllowSideStaysLegal proves `pg-desk issue refresh` and the
// pg-connector read verbs stay legal, and that comments never trip the scan.
func TestG5GuardAllowSideStaysLegal(t *testing.T) {
	res, err := ScanDecisionLogic(filepath.Join("testdata", "allow"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.GoFiles != 1 {
		t.Fatalf("allow-side fixture scanned %d files, want 1", res.GoFiles)
	}
	if len(res.Violations) != 0 || len(res.Allowed) != 0 {
		t.Fatalf("allow-side fixture must be clean, got %+v / %+v", res.Violations, res.Allowed)
	}
}

// TestG5GuardIgnoresTestFilesAndGuardPackage checks the scan scope: test
// files, testdata and the guard's own directory are not scanned.
func TestG5GuardIgnoresTestFilesAndGuardPackage(t *testing.T) {
	banned := "package p\n\nconst k = \"focus-item\"\n"
	res := scanFiles(t, map[string]string{
		"internal/p/p_test.go":            banned,
		"internal/p/testdata/fixture.go":  banned,
		"internal/guards/fixture_scan.go": banned,
		"internal/p/ok.go":                "package p\n",
	}, nil)
	if res.GoFiles != 1 || len(res.Violations) != 0 {
		t.Fatalf("want 1 scanned file and no violations, got %d files, %+v", res.GoFiles, res.Violations)
	}
}

// TestG5GuardAllowlist proves the allowlist suppresses only the named file.
func TestG5GuardAllowlist(t *testing.T) {
	src := "package p\n\nvar argv = []string{\"issue\", \"create\"}\n"
	files := map[string]string{"internal/old/old.go": src, "internal/new/new.go": src}
	res := scanFiles(t, files, []string{"internal/old/old.go"})
	if len(res.Allowed) != 1 || res.Allowed[0].File != "internal/old/old.go" {
		t.Fatalf("want old.go allowlisted, got %+v", res.Allowed)
	}
	if len(res.Violations) != 1 || res.Violations[0].File != "internal/new/new.go" {
		t.Fatalf("want new.go reported, got %+v", res.Violations)
	}
}

// TestPreCutoverAllowlistIsMinimal pins the allowlist to the one known entry.
func TestPreCutoverAllowlistIsMinimal(t *testing.T) {
	if len(PreCutoverAllowlist) != 1 || PreCutoverAllowlist[0] != "internal/sync/connector.go" {
		t.Fatalf("PreCutoverAllowlist = %v, want exactly internal/sync/connector.go", PreCutoverAllowlist)
	}
}

// TestG5DedupKeyExemption pins the single documented exemption: the literal
// dedup_key is legal only as the value of const MetaDedupKey in
// internal/focus/exclusions.go, checked by name.
func TestG5DedupKeyExemption(t *testing.T) {
	const decl = "package focus\n\n// MetaDedupKey is the read-only candidacy-exclusion key.\nconst MetaDedupKey = \"dedup_key\"\n"
	cases := []struct {
		name  string
		files map[string]string
		want  int // expected violations
	}{
		{"exempt pair is legal", map[string]string{"internal/focus/exclusions.go": decl}, 0},
		{
			"second occurrence in the exempt file fails",
			map[string]string{"internal/focus/exclusions.go": decl + "\nvar other = \"dedup_key\"\n"},
			1,
		},
		{
			"different constant name in the exempt file fails",
			map[string]string{"internal/focus/exclusions.go": "package focus\n\nconst Other = \"dedup_key\"\n"},
			1,
		},
		{
			"same constant in another file fails",
			map[string]string{"internal/focus/rank.go": decl},
			1,
		},
		{
			"constant bound to another value is not exempted",
			map[string]string{"internal/focus/exclusions.go": "package focus\n\nconst MetaDedupKey = \"focus-item\"\n"},
			1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := scanFiles(t, tc.files, nil)
			if len(res.Violations) != tc.want {
				t.Fatalf("want %d violation(s), got %+v", tc.want, res.Violations)
			}
		})
	}
}

func TestScanMissingRootIsAnError(t *testing.T) {
	if _, err := ScanDecisionLogic(filepath.Join(t.TempDir(), "absent"), nil); err == nil {
		t.Fatal("want an error for a missing root")
	}
}

func TestFindModuleRoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/m\n")
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindModuleRoot(deep); got != root {
		t.Fatalf("FindModuleRoot = %q, want %q", got, root)
	}
}
