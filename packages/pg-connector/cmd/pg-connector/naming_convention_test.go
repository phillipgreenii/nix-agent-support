package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capabilityPackages is the exact capability-token list design §3's
// acceptance criteria names: "every exported interface's name and method
// set corresponds to exactly one capability (pr/issue/ci/scm/attention/
// search)". attention/search do not exist as pkg/provider subpackages yet
// (Appendix A: not yet built) — evaluateCapabilityNaming simply finds
// nothing to check for a capability with no directory yet, which is not a
// violation of anything.
var capabilityPackages = []string{"pr", "issue", "ci", "scm", "attention", "search"}

// systemNamingTokens is the "names no backend/system (github/jira/slack/…)"
// half of design §3's acceptance criteria. It is deliberately a curated
// list, not derived from this module's own cmd/ directory names: deriving
// it from on-disk backends would only catch a system name AFTER a backend
// for it already exists, which is exactly backwards for a naming-convention
// guard meant to catch the mistake at review time, before or independent of
// any concrete backend landing. The three names are the design doc's own
// literal examples (§3's "github/jira/slack"); "beads" and "gitlab" are
// added because they are, respectively, an actual backend in this module
// (pg-connector-issue-beads) and a common enough system name that a future
// SCM/issue backend is plausible. Every entry here is matched as a
// case-insensitive SUBSTRING (see containsSystemToken below), deliberately
// not a whole-word match: Go identifiers write a brand name like "GitHub"
// in PascalCase as "GitHub" (capital G, capital H — e.g. "ShowGitHubPR"),
// which a camelCase word-boundary splitter would incorrectly break into
// "Git"+"Hub", never matching a whole-word "github" token at all. The
// deliberately excluded short/generic token is "git" on its own (as
// opposed to "github"): as a bare 3-letter substring it collides with
// ordinary English words ("digit", "legit") and with "gitenv" — an
// existing, entirely legitimate package name in this module's own
// backends — so including it would produce exactly the false positives a
// substring approach is otherwise safe from at this list's actual token
// lengths (>= 4 characters each). Recognized as an accepted scope
// limitation: this list can go stale as new backend systems are added; it
// is a naming-convention backstop, not the primary enforcement (the
// primary enforcement is design/code review at the time a new backend's
// interface is authored).
var systemNamingTokens = []string{"github", "jira", "slack", "beads", "gitlab", "bitbucket"}

// containsSystemToken reports whether ident contains any systemNamingTokens
// entry as a case-insensitive substring.
func containsSystemToken(ident string) (string, bool) {
	lower := strings.ToLower(ident)
	for _, tok := range systemNamingTokens {
		if strings.Contains(lower, tok) {
			return tok, true
		}
	}
	return "", false
}

// evaluateCapabilityNaming parses every non-test .go file directly in dir
// (no recursion — dir is expected to be one pkg/provider/<capability>
// package) and returns one violation string per exported interface whose
// own name, or any of whose method names, names a backend/system per
// containsSystemToken.
//
// This mechanizes the "names no backend/system" half of design §3's
// acceptance criteria. The "corresponds to exactly one capability" half is
// enforced structurally rather than by this function: each capability gets
// exactly one Go package (one directory, one Provider interface) by
// construction — the compiler itself rejects two conflicting top-level
// "Provider" declarations in the same package — so there is no separate
// mechanical check for it beyond "this function found a capability
// package to look at" (TestCapabilityNamingConvention below iterates the
// fixed §3 capability list precisely to keep that structural fact visible
// in the test, not just true by accident of the directory layout).
func evaluateCapabilityNaming(dir, capability string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var violations []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				iface, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					continue
				}
				if tok, bad := containsSystemToken(ts.Name.Name); bad {
					violations = append(violations, fmt.Sprintf(
						"%s: capability %q interface %q names a backend/system (%q) — capability interfaces MUST be scoped by capability, never by system [design: §3]",
						entry.Name(), capability, ts.Name.Name, tok,
					))
				}
				if iface.Methods == nil {
					continue
				}
				for _, m := range iface.Methods.List {
					for _, name := range m.Names {
						if tok, bad := containsSystemToken(name.Name); bad {
							violations = append(violations, fmt.Sprintf(
								"%s: capability %q interface %q method %q names a backend/system (%q) — capability interfaces MUST be scoped by capability, never by system [design: §3]",
								entry.Name(), capability, ts.Name.Name, name.Name, tok,
							))
						}
					}
				}
			}
		}
	}
	return violations, nil
}

// TestCapabilityNamingConvention is design §3's naming/convention check: it
// confirms every exported interface under pkg/provider/<capability> (for
// each of the fixed capability tokens pr/issue/ci/scm/attention/search)
// names no backend/system.
func TestCapabilityNamingConvention(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	found := 0
	for _, capability := range capabilityPackages {
		dir := filepath.Join(moduleRoot, "pkg", "provider", capability)
		if _, err := os.Stat(dir); err != nil {
			// Not built yet (attention/search — Appendix A). Nothing to
			// check; not a violation.
			continue
		}
		found++
		violations, err := evaluateCapabilityNaming(dir, capability)
		if err != nil {
			t.Fatalf("evaluateCapabilityNaming(%s): %v", dir, err)
		}
		for _, v := range violations {
			t.Error(v)
		}
	}
	if found == 0 {
		t.Fatal("no pkg/provider/<capability> directories found at all — capabilityPackages or the module layout has drifted")
	}
}

// TestCapabilityNamingConvention_DetectsSystemNamedMethod is a
// test-of-a-test: design §3's naming check has never had anything to
// reject in the current, already-compliant tree, so this proves
// evaluateCapabilityNaming actually flags a violation rather than only
// ever passing vacuously. It writes a synthetic capability package to a
// temp directory — never the real tree — with an interface whose method
// name embeds a backend/system name, and asserts the checker rejects it.
func TestCapabilityNamingConvention_DetectsSystemNamedMethod(t *testing.T) {
	dir := t.TempDir()
	src := `package pr

// Provider is deliberately non-compliant: ShowGitHubPR names the "github"
// system, which capability-scoped interfaces must never do.
type Provider interface {
	ShowGitHubPR(id string) error
}
`
	if err := os.WriteFile(filepath.Join(dir, "iface.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	violations, err := evaluateCapabilityNaming(dir, "pr")
	if err != nil {
		t.Fatalf("evaluateCapabilityNaming: %v", err)
	}
	if len(violations) == 0 {
		t.Fatal("evaluateCapabilityNaming did not flag ShowGitHubPR as naming a backend/system")
	}
	found := false
	for _, v := range violations {
		if strings.Contains(v, "ShowGitHubPR") && strings.Contains(v, `"github"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("evaluateCapabilityNaming's violations did not identify ShowGitHubPR/github specifically: %v", violations)
	}
}

// TestCapabilityNamingConvention_AllowsCleanInterface guards against the
// opposite failure mode (a checker that flags everything): a compliant
// interface over the same shape as the fixture above, minus the
// system-named method, must produce zero violations.
func TestCapabilityNamingConvention_AllowsCleanInterface(t *testing.T) {
	dir := t.TempDir()
	src := `package pr

type Provider interface {
	Show(id string) error
}
`
	if err := os.WriteFile(filepath.Join(dir, "iface.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	violations, err := evaluateCapabilityNaming(dir, "pr")
	if err != nil {
		t.Fatalf("evaluateCapabilityNaming: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("evaluateCapabilityNaming flagged a compliant interface: %v", violations)
	}
}
