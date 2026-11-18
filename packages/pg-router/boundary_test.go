package pgrouter_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// The import-boundary rule for the folded members inside the router module.
// Go's internal/ rule no longer separates members once they share a module, so
// the rule is tested here. members.json is the allowlist (the shared set and
// each member's cmd and internal subtrees); the nix filesets read the same
// file.
//
//  1. A member package (cmd/<member binary>, internal/<member>/...) may import
//     the standard library, third-party and workspace modules, its own member's
//     subtree and the shared set. It must not import another member's subtree
//     or a router engine package.
//  2. The shared set is exactly the listed packages. Adding to it is a
//     deliberate edit of members.json; it must not grow to include an engine
//     package (the closure check below fails when a shared package reaches one).
//  3. An engine package (any other package of the module) must not import a
//     member subtree.
//  4. A member marked stdlibOnly imports the standard library only.
//  5. No package imports a cmd/ package.
//
// The same test asserts that each member binary's dependency closure within
// the module lies inside the member's fileset (own cmd, own internal, shared).

const modulePath = "github.com/phillipgreenii/pg-router"

type membersFile struct {
	Shared  []string          `json:"shared"`
	Members map[string]member `json:"members"`
}

type member struct {
	Cmd        []string `json:"cmd"`
	Internal   []string `json:"internal"`
	StdlibOnly bool     `json:"stdlibOnly"`
}

type listedPackage struct {
	ImportPath   string
	Standard     bool
	Imports      []string
	TestImports  []string
	XTestImports []string
}

func loadMembers(t *testing.T) membersFile {
	t.Helper()
	b, err := os.ReadFile("members.json")
	if err != nil {
		t.Fatal(err)
	}
	var m membersFile
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("members.json: %v", err)
	}
	return m
}

func listPackages(t *testing.T) []listedPackage {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-e", "-deps", "-json", "./...")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	var pkgs []listedPackage
	dec := json.NewDecoder(&out)
	for dec.More() {
		var p listedPackage
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// rel returns the module-relative path of an in-module import path.
func rel(importPath string) (string, bool) {
	if importPath == modulePath {
		return "", true
	}
	if r, ok := strings.CutPrefix(importPath, modulePath+"/"); ok {
		return r, true
	}
	return "", false
}

func under(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// owner classifies a module-relative path: ("member", name), ("shared", ""),
// or ("engine", "").
func (m membersFile) owner(path string) (string, string) {
	for _, s := range m.Shared {
		if under(path, s) {
			return "shared", ""
		}
	}
	names := make([]string, 0, len(m.Members))
	for n := range m.Members {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		mem := m.Members[n]
		for _, c := range mem.Cmd {
			if under(path, "cmd/"+c) {
				return "member", n
			}
		}
		for _, i := range mem.Internal {
			if under(path, "internal/"+i) {
				return "member", n
			}
		}
	}
	return "engine", ""
}

// violations returns one line per broken rule, each naming the import edge.
func violations(m membersFile, pkgs []listedPackage) []string {
	var v []string
	std := map[string]bool{}
	byPath := map[string]listedPackage{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
		std[p.ImportPath] = p.Standard
	}
	for _, p := range pkgs {
		from, ok := rel(p.ImportPath)
		if !ok {
			continue
		}
		fromKind, fromMember := m.owner(from)
		imports := append(append(append([]string{}, p.Imports...), p.TestImports...), p.XTestImports...)
		for _, imp := range imports {
			if to, inModule := rel(imp); inModule {
				if strings.HasPrefix(to, "cmd/") || to == "cmd" {
					v = append(v, fmt.Sprintf("rule 5: %s imports %s (cmd packages are never imported)", p.ImportPath, imp))
				}
				toKind, toMember := m.owner(to)
				switch fromKind {
				case "member":
					if toKind == "engine" {
						v = append(v, fmt.Sprintf("rule 1: member %s package %s imports router engine package %s", fromMember, p.ImportPath, imp))
					}
					if toKind == "member" && toMember != fromMember {
						v = append(v, fmt.Sprintf("rule 1: member %s package %s imports member %s package %s", fromMember, p.ImportPath, toMember, imp))
					}
				case "engine":
					if toKind == "member" {
						v = append(v, fmt.Sprintf("rule 3: engine package %s imports member %s package %s", p.ImportPath, toMember, imp))
					}
				}
			}
			if fromKind == "member" && m.Members[fromMember].StdlibOnly {
				_, own := rel(imp)
				if own {
					// Rule 1 already judges in-module edges; the member's own subtree is fine.
					continue
				}
				if !isStd(imp, std) {
					v = append(v, fmt.Sprintf("rule 4: stdlib-only member %s package %s imports %s", fromMember, p.ImportPath, imp))
				}
			}
		}
	}
	for name, mem := range m.Members {
		for _, c := range mem.Cmd {
			v = append(v, closureViolations(m, name, modulePath+"/cmd/"+c, byPath)...)
		}
	}
	sort.Strings(v)
	return v
}

func isStd(imp string, std map[string]bool) bool {
	if s, known := std[imp]; known {
		return s
	}
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

// closureViolations walks the non-test import closure of one member binary and
// reports every module package outside the member's fileset.
func closureViolations(m membersFile, name, root string, byPath map[string]listedPackage) []string {
	var v []string
	seen := map[string]bool{}
	var walk func(path, via string)
	walk = func(path, via string) {
		if seen[path] {
			return
		}
		seen[path] = true
		p, ok := byPath[path]
		if !ok {
			return
		}
		if r, inModule := rel(path); inModule {
			kind, owner := m.owner(r)
			if kind == "engine" || (kind == "member" && owner != name) {
				v = append(v, fmt.Sprintf("closure: binary %s reaches %s (via %s), outside its fileset", root, path, via))
				return
			}
		}
		for _, imp := range p.Imports {
			walk(imp, path)
		}
	}
	walk(root, "(root)")
	return v
}

func TestMemberBoundary(t *testing.T) {
	m := loadMembers(t)
	pkgs := listPackages(t)
	// Liveness: an empty or mis-scoped listing must fail, not pass vacuously.
	found := map[string]bool{}
	for _, p := range pkgs {
		found[p.ImportPath] = true
	}
	for name, mem := range m.Members {
		for _, c := range mem.Cmd {
			if !found[modulePath+"/cmd/"+c] {
				t.Fatalf("member %s binary cmd/%s not found by go list", name, c)
			}
		}
	}
	if !found[modulePath+"/schemas"] {
		t.Fatal("shared package schemas not found by go list")
	}
	for _, line := range violations(m, pkgs) {
		t.Error(line)
	}
}

// The negative controls run the rule over a synthetic package list, so the
// test proves it detects each violation kind without touching real sources.
func TestMemberBoundaryDetectsViolations(t *testing.T) {
	m := membersFile{
		Shared: []string{"schemas"},
		Members: map[string]member{
			"a": {Cmd: []string{"bin-a"}, Internal: []string{"a"}},
			"b": {Cmd: []string{"bin-b"}, Internal: []string{"b"}, StdlibOnly: true},
		},
	}
	p := func(path string, imports ...string) listedPackage {
		full := modulePath + "/" + path
		for i := range imports {
			if imports[i] != "fmt" && !strings.Contains(imports[i], ".") {
				imports[i] = modulePath + "/" + imports[i]
			}
		}
		return listedPackage{ImportPath: full, Imports: imports}
	}
	base := []listedPackage{
		{ImportPath: "fmt", Standard: true},
		{ImportPath: "github.com/third/party"},
		p("schemas"),
		p("internal/core", "fmt"),
		p("internal/b", "fmt"),
		p("internal/a", "schemas", "internal/a/sub", "github.com/third/party"),
		p("internal/a/sub", "fmt"),
		p("cmd/bin-a", "internal/a", "schemas"),
		p("cmd/bin-b", "internal/b"),
		p("cmd/router", "internal/core"),
	}
	if got := violations(m, base); len(got) != 0 {
		t.Fatalf("clean synthetic listing reported violations: %v", got)
	}
	cases := []struct {
		name string
		edit func([]listedPackage) []listedPackage
		want string
	}{
		{"member imports engine", func(l []listedPackage) []listedPackage {
			l = append(l, p("internal/a/bad", "internal/core"))
			return l
		}, "rule 1: member a package " + modulePath + "/internal/a/bad imports router engine package " + modulePath + "/internal/core"},
		{"member imports other member", func(l []listedPackage) []listedPackage {
			return append(l, p("internal/a/bad", "internal/b"))
		}, "rule 1: member a package " + modulePath + "/internal/a/bad imports member b package " + modulePath + "/internal/b"},
		{"engine imports member", func(l []listedPackage) []listedPackage {
			return append(l, p("internal/core2", "internal/a"))
		}, "rule 3: engine package " + modulePath + "/internal/core2 imports member a package " + modulePath + "/internal/a"},
		{"stdlib-only member imports third party", func(l []listedPackage) []listedPackage {
			return append(l, p("internal/b/bad", "github.com/third/party"))
		}, "rule 4: stdlib-only member b package " + modulePath + "/internal/b/bad imports github.com/third/party"},
		{"cmd imported", func(l []listedPackage) []listedPackage {
			return append(l, p("internal/core2", "cmd/router"))
		}, "rule 5: " + modulePath + "/internal/core2 imports " + modulePath + "/cmd/router (cmd packages are never imported)"},
		{"closure reaches engine through shared", func(l []listedPackage) []listedPackage {
			out := append([]listedPackage{}, l...)
			for i := range out {
				if out[i].ImportPath == modulePath+"/schemas" {
					out[i].Imports = []string{modulePath + "/internal/core"}
				}
			}
			return out
		}, "closure: binary " + modulePath + "/cmd/bin-a reaches " + modulePath + "/internal/core (via " + modulePath + "/schemas), outside its fileset"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := violations(m, c.edit(append([]listedPackage{}, base...)))
			for _, line := range got {
				if line == c.want {
					return
				}
			}
			t.Fatalf("want violation %q, got %v", c.want, got)
		})
	}
}
