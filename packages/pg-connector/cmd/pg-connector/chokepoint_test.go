package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Relocated from the removed pg-connector-pr-github/internal/github/chokepoint_test.go
// (bead pg2-lh3c4's acceptance criteria): TestGHExecChokePoint
// and its sibling TestNoGHStackMutatingArgv (stack_readonly_test.go, same
// package) already walked the WHOLE module from inside one backend's own
// test package — so deleting or restructuring pr-github ahead of this move
// would have silently dropped the module's only cross-backend `gh`
// choke-point and stack-mutation guards. This umbrella package
// (cmd/pg-connector) already hosts every other module-wide mechanical/
// convention check (layout_convention_test.go, dependency_direction_test.go,
// backend_internal_sync_test.go) — none of
// them tied to any single backend's own lifecycle — so it is this guard's
// new home too. moduleRoot(t) is unchanged from the original: it walks up
// from the test's own working directory to the nearest go.mod, so it needs
// no adjustment for this package's different depth.

// The guard is AST-based (bead pg2-9df7aq). Its first form was a line regexp
// over `exec.Command(Context)?("gh"`, which a) never matched
// pg-connector-github, whose gh binary comes from configuration (so no exec
// call there names the literal), b) missed a `const bin = "gh"` indirection,
// a call split over lines and an aliased `os/exec` import, and c) is only as
// wide as the text it can see. ghExecFindings parses every .go file under the
// root with go/parser, which ignores build constraints, so a file behind
// `//go:build integration` or `contract` (or a GOOS suffix) is scanned too.
//
// Judgment call (bead pg2-lh3c4): the rule stays scoped to "gh" alone rather
// than generalizing to "git" or other per-backend binaries. The defect
// class this guard exists for is specific to gh: an unauthenticated `gh`
// invocation pops an INTERACTIVE auth prompt under a headless daemon (the
// launchd sync agent) — a failure mode tied to gh's own OAuth/token
// preflight, which ghexec.go's choke point exists to intercept before any
// `gh` subprocess runs. `git` has no analogous interactive-auth-prompt
// failure mode to gate here; its own hermeticity concern (environment
// leakage via GIT_DIR/GIT_WORK_TREE et al., not authentication) is a
// different problem with its own dedicated guard (the gitenv package plus
// TestBackendInternalGitenvAndGithubHelpersSync's content-hash pinning in
// backend_internal_sync_test.go, same package) — folding "git" into this
// rule would conflate two unrelated concerns behind one name. Other
// per-backend binaries (dolt, bd) have no auth-preflight of this shape
// either. A future backend that shells to `gh` still needs its own
// ghexec.go/token.go pair added to ghChokePoints below — Go's internal/
// visibility rule makes each backend's copy independent — which is the
// existing, working extension point for this guard; scm-git and
// issue-beads correctly have no entry today because neither shells to gh.

// ghChokeFile is one allowlisted choke-point file. A nil funcs set allows every
// exec in the file; a non-nil set allows only an exec inside one of the named
// top-level functions ("Recv.Method" for a method, "name" for a function).
type ghChokeFile struct{ funcs map[string]bool }

// ghChokePoints maps a module-relative file to what may exec gh in it.
//
//   - ghexec.go — the token-protected choke point (resolve, then exec).
//   - token.go  — the token RESOLVER, the one sanctioned exception; routing it
//     through the preflight would be circular.
//
// The pg-connector-ci-github-actions pair was added when that backend
// carried over this same mechanism into its own cmd/<binary>/internal/github
// tree [bead pg2-2j5ac.10; INV-AUTH-1] — Go's internal/ visibility
// rule makes each backend's copy independent, so each backend's own
// ghexec.go/token.go pair is allowlisted separately rather than shared.
//
// pg-connector-github's own copy of the pr-github pair (bead pg2-z5fax.2.1) is
// allowlisted FUNCTION by function: its binary path and GH_HOST are
// configurable, so no exec there names the literal, and the file-level
// allowance the other two pairs carry would let a second exec of the
// configured binary slip into the same file unseen.
var ghChokePoints = map[string]ghChokeFile{
	filepath.Join("cmd", "pg-connector-github", "internal", "github", "ghexec.go"): {
		funcs: map[string]bool{"cliGHRunner.command": true},
	},
	filepath.Join("cmd", "pg-connector-github", "internal", "github", "token.go"): {
		funcs: map[string]bool{"ghAuthTokenCommand": true},
	},
}

// ghConfiguredBinaryPkgs are the package directories whose gh binary is
// CONFIGURED (reaches exec as a field, not a literal). Every exec of any kind
// in one of them MUST sit inside its choke-point functions: that is what makes
// `exec.Command(r.bin, ...)` in a new file or a new method a finding even
// though no name in the call says "gh".
var ghConfiguredBinaryPkgs = []string{
	filepath.Join("cmd", "pg-connector-github", "internal", "github"),
}

// ghSelectorNames name the configured gh binary where it is read as a value
// (config.GH is `gh_path`, github.Options.Binary, the cliGHRunner.binary()
// accessor and the two defaults). They apply to selector expressions and, for
// the defaults, bare identifiers; a CALL of a selector is not matched (except
// binary()), so servetest.Binary(t, ...) — the daemon's OWN binary — is not
// mistaken for gh.
var ghSelectorNames = map[string]bool{
	"GH": true, "GHPath": true, "DefaultBinary": true,
	"DefaultGHPath": true, "GHBinary": true,
}

// ghContextSelectorNames are too common to mean gh anywhere (the conformance
// harness's Backend.Binary is a connector's OWN binary), so they count only in
// a file that imports a backend's internal/github package or sits in one:
// github.Options.Binary and the cliGHRunner.binary() accessor.
var ghContextSelectorNames = map[string]bool{"Binary": true, "binary": true}

// ghIdentRE matches an identifier that is plainly "the gh binary".
var ghIdentRE = regexp.MustCompile(`(?i)^(gh|ghbin|ghbinary|ghpath|ghexe|defaultbinary|defaultghpath)$`)

// ghPathFuncs are path helpers through which an argument keeps naming gh.
var ghPathFuncs = map[string]bool{"Join": true, "Clean": true, "FromSlash": true, "Abs": true}

// ghPkg is the parsed form of one directory's Go files.
type ghPkg struct {
	dir   string // module-relative
	files []*ghFile
	decls map[string][]ast.Expr // package-level const/var/func-var name -> RHS
}

type ghFile struct {
	rel     string
	src     []byte
	fset    *token.FileSet
	ast     *ast.File
	execPkg string          // local name of "os/exec"; "" when not imported
	ghCtx   bool            // imports (or is in) a backend's internal/github package
	aliases map[string]bool // names bound to exec.Command / exec.CommandContext
}

// ghExecFindings returns one line per exec of gh outside the choke points
// under root: "rel:line: reason: source". Output is sorted.
func ghExecFindings(root string) ([]string, error) {
	pkgs := map[string]*ghPkg{}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		dir := filepath.Dir(rel)
		pk := pkgs[dir]
		if pk == nil {
			pk = &ghPkg{dir: dir, decls: map[string][]ast.Expr{}}
			pkgs[dir] = pk
		}
		gf := &ghFile{rel: rel, src: src, fset: fset, ast: f, aliases: map[string]bool{}}
		gf.ghCtx = path.Base(filepath.ToSlash(dir)) == "github" && strings.Contains(filepath.ToSlash(dir), "internal/")
		for _, imp := range f.Imports {
			v, _ := strconv.Unquote(imp.Path.Value)
			if v == "os/exec" {
				gf.execPkg = "exec"
				if imp.Name != nil {
					gf.execPkg = imp.Name.Name
				}
			}
			if strings.HasSuffix(v, "/internal/github") {
				gf.ghCtx = true
			}
		}
		pk.files = append(pk.files, gf)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	var out []string
	for _, pk := range pkgs {
		// Package-level names are shared by every file of the package, so a
		// `const bin = "gh"` in one file taints an exec in another.
		for _, gf := range pk.files {
			for _, d := range gf.ast.Decls {
				if gd, ok := d.(*ast.GenDecl); ok {
					recordGenDecl(gd, pk.decls)
				}
			}
			if gf.execPkg != "" {
				collectExecAliases(gf)
			}
		}
		strict := false
		for _, s := range ghConfiguredBinaryPkgs {
			if pk.dir == s {
				strict = true
			}
		}
		for _, gf := range pk.files {
			if gf.execPkg == "" || gf.execPkg == "_" || gf.execPkg == "." {
				continue
			}
			choke, chokeOK := ghChokePoints[gf.rel]
			if chokeOK && choke.funcs == nil {
				continue
			}
			for _, d := range gf.ast.Decls {
				fn := ""
				locals := map[string][]ast.Expr{}
				switch d := d.(type) {
				case *ast.FuncDecl:
					fn = funcKey(d)
					if d.Body != nil {
						collectLocals(d.Body, locals)
					}
				case *ast.GenDecl:
					// Package-level initializers run with no enclosing function.
				default:
					continue
				}
				allowed := chokeOK && choke.funcs[fn]
				ast.Inspect(d, func(n ast.Node) bool {
					if n == nil {
						return false
					}
					reason := ""
					pos := n.Pos()
					switch n := n.(type) {
					case *ast.CallExpr:
						idx, ok := gf.execArgIndex(n)
						if !ok {
							return true
						}
						switch {
						case strict && !allowed:
							reason = "exec in the configured-gh-binary package outside its choke-point function"
						case !allowed && idx < len(n.Args) && pk.isGH(gf, n.Args[idx], locals, map[string]bool{}):
							reason = "exec of gh"
						}
					case *ast.CompositeLit:
						if sel, ok := n.Type.(*ast.SelectorExpr); ok && gf.isExecPkg(sel.X) && sel.Sel.Name == "Cmd" {
							for _, el := range n.Elts {
								kv, ok := el.(*ast.KeyValueExpr)
								if !ok {
									continue
								}
								if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Path" && !allowed &&
									(strict || pk.isGH(gf, kv.Value, locals, map[string]bool{})) {
									reason = "exec.Cmd literal naming gh"
									pos = kv.Pos()
								}
							}
						}
					}
					if reason != "" {
						out = append(out, fmt.Sprintf("%s:%d: %s: %s", gf.rel, gf.fset.Position(pos).Line, reason, gf.snippet(n)))
					}
					return true
				})
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func funcKey(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	t := d.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + d.Name.Name
	}
	return d.Name.Name
}

func (gf *ghFile) snippet(n ast.Node) string {
	a, b := gf.fset.Position(n.Pos()).Offset, gf.fset.Position(n.End()).Offset
	if a < 0 || b > len(gf.src) || a >= b {
		return ""
	}
	return strings.Join(strings.Fields(string(gf.src[a:b])), " ")
}

func (gf *ghFile) isExecPkg(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == gf.execPkg
}

// execFuncIndex returns the argument index of the binary for exec.Command (0)
// and exec.CommandContext (1), and whether e names one of them.
func (gf *ghFile) execFuncIndex(e ast.Expr) (int, bool) {
	switch e := e.(type) {
	case *ast.SelectorExpr:
		if gf.isExecPkg(e.X) {
			switch e.Sel.Name {
			case "Command":
				return 0, true
			case "CommandContext":
				return 1, true
			}
		}
	case *ast.Ident:
		// An alias such as `run := exec.Command`; the index travels in the map key.
		if gf.aliases[e.Name+"#0"] {
			return 0, true
		}
		if gf.aliases[e.Name+"#1"] {
			return 1, true
		}
	case *ast.ParenExpr:
		return gf.execFuncIndex(e.X)
	}
	return 0, false
}

func (gf *ghFile) execArgIndex(c *ast.CallExpr) (int, bool) { return gf.execFuncIndex(c.Fun) }

// collectExecAliases records every identifier bound to exec.Command or
// exec.CommandContext (`run := exec.Command`, `var run = exec.CommandContext`).
func collectExecAliases(gf *ghFile) {
	bind := func(name *ast.Ident, rhs ast.Expr) {
		if idx, ok := gf.execFuncIndex(rhs); ok && name.Name != "_" {
			gf.aliases[name.Name+"#"+strconv.Itoa(idx)] = true
		}
	}
	ast.Inspect(gf.ast, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						bind(id, n.Rhs[i])
					}
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for i, id := range n.Names {
					bind(id, n.Values[i])
				}
			}
		}
		return true
	})
}

func recordGenDecl(gd *ast.GenDecl, into map[string][]ast.Expr) {
	for _, sp := range gd.Specs {
		if vs, ok := sp.(*ast.ValueSpec); ok && len(vs.Names) == len(vs.Values) {
			for i, id := range vs.Names {
				into[id.Name] = append(into[id.Name], vs.Values[i])
			}
		}
	}
}

// collectLocals records name -> assigned expressions for one function body. It
// is flow-insensitive on purpose: a reassignment to "gh" anywhere in the
// function makes the name suspect.
func collectLocals(body ast.Node, into map[string][]ast.Expr) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						into[id.Name] = append(into[id.Name], n.Rhs[i])
					}
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for i, id := range n.Names {
					into[id.Name] = append(into[id.Name], n.Values[i])
				}
			}
		}
		return true
	})
}

// isGH reports whether e evaluates to (a path of) the gh binary: the literal,
// a name that holds it, or the configured-binary value.
func (pk *ghPkg) isGH(gf *ghFile, e ast.Expr, locals map[string][]ast.Expr, seen map[string]bool) bool {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return false
		}
		v, err := strconv.Unquote(e.Value)
		return err == nil && path.Base(v) == "gh"
	case *ast.ParenExpr:
		return pk.isGH(gf, e.X, locals, seen)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return false
		}
		if l, ok := foldString(e); ok {
			return path.Base(l) == "gh"
		}
		return pk.isGH(gf, e.X, locals, seen) || pk.isGH(gf, e.Y, locals, seen)
	case *ast.Ident:
		if ghIdentRE.MatchString(e.Name) {
			return true
		}
		if seen[e.Name] {
			return false
		}
		seen[e.Name] = true
		defer delete(seen, e.Name)
		rhs, ok := locals[e.Name]
		if !ok {
			rhs = pk.decls[e.Name]
		}
		for _, r := range rhs {
			if pk.isGH(gf, r, locals, seen) {
				return true
			}
		}
	case *ast.SelectorExpr:
		return ghSelectorNames[e.Sel.Name] || (gf.ghCtx && ghContextSelectorNames[e.Sel.Name])
	case *ast.CallExpr:
		switch f := e.Fun.(type) {
		case *ast.SelectorExpr:
			if f.Sel.Name == "binary" && len(e.Args) == 0 && gf.ghCtx {
				return true
			}
			if ghPathFuncs[f.Sel.Name] {
				for _, a := range e.Args {
					if pk.isGH(gf, a, locals, seen) {
						return true
					}
				}
			}
		case *ast.Ident:
			if f.Name == "binary" && len(e.Args) == 0 && gf.ghCtx {
				return true
			}
			if f.Name == "string" && len(e.Args) == 1 {
				return pk.isGH(gf, e.Args[0], locals, seen)
			}
		}
	}
	return false
}

// foldString folds a `"a" + "b"` of string literals.
func foldString(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			v, err := strconv.Unquote(e.Value)
			return v, err == nil
		}
	case *ast.ParenExpr:
		return foldString(e.X)
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			l, ok1 := foldString(e.X)
			r, ok2 := foldString(e.Y)
			return l + r, ok1 && ok2
		}
	}
	return "", false
}

// TestGHExecChokePoint is the module-wide half of bead pg2-ilzq9's guarantee:
// the four sites fixed there were fixed one at a time, and nothing stopped a
// fifth from appearing. It asserts that `gh` is spawned from exactly the
// choke points in ghChokePoints, one pair per backend that ports this
// mechanism, and (bead pg2-9df7aq) that it is caught however the binary is
// named: the literal, a const or var holding it, a multi-line or aliased
// call, and — in pg-connector-github, whose binary is configured — ANY exec
// outside the choke-point functions or any exec of a configured-binary value.
//
// Every other package reaches gh through a backend's own github.CLI
// (Run/RunStdin, or Command for sites that wire up their own
// stdout/stderr/dir), so a new direct exec here means an unauthenticated gh
// became reachable again — which is what popped interactive auth screens
// under the launchd sync agent.
func TestGHExecChokePoint(t *testing.T) {
	offenders, err := ghExecFindings(moduleRoot(t))
	if err != nil {
		t.Fatalf("scan module: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("gh is exec'd directly outside the protected choke point:\n  %s\n\n"+
			"Route it through github.NewCLI(): Run/RunStdin for a whole invocation, or\n"+
			"Command when the call site needs the *exec.Cmd (working directory, own\n"+
			"stderr capture). See ghexec.go.",
			strings.Join(offenders, "\n  "))
	}
}

// moduleRoot walks up from the test's working directory to the directory holding
// go.mod. Shared by TestGHExecChokePoint above and TestNoGHStackMutatingArgv
// (stack_readonly_test.go, same package).
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the test's working directory")
		}
		dir = parent
	}
}

// ghFixtureBase is a clean mini-module: the real choke-point files of the
// configured-binary backend (each exec inside its allowlisted function), plus
// execs the guard MUST leave alone (git, a daemon binary, a call named Binary).
// The planted violations below are written on top of it in a temp directory,
// never in the real tree.
func ghFixtureBase() map[string]string {
	const pkg = "cmd/pg-connector-github/internal/github/"
	return map[string]string{
		pkg + "ghexec.go": `package github

import (
	"context"
	"os/exec"
)

type cliGHRunner struct{ bin string }

func (r *cliGHRunner) binary() string { return r.bin }

func (r *cliGHRunner) command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, r.binary(), args...)
}
`,
		pkg + "token.go": `package github

import (
	"context"
	"os/exec"
)

func ghAuthTokenCommand(ctx context.Context, bin string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, "auth", "token")
}
`,
		"cmd/pg-connector-github/internal/gitenv/gitenv.go": `package gitenv

import (
	"context"
	"os/exec"
)

func Git(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "git", "status") }
`,
		"cmd/pg-connector-github/daemon_test.go": `package main

import (
	"os/exec"
	"testing"
)

func Binary(t *testing.T) string { return "daemon" }

type backend struct{ Binary string }

func TestDaemon(t *testing.T) {
	b := backend{Binary: "x"}
	_ = exec.Command(Binary(t), "serve")
	_ = exec.Command(b.Binary, "serve")
	bin := "daemon"
	_ = exec.Command(bin, "--config", "c")
}
`,
	}
}

func writeGHTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGHExecChokePointDetectsEveryForm is the regression test for bead
// pg2-9df7aq (R2): it plants one violation per way of naming the gh binary and
// shows the guard fails on each, and passes on the clean fixture. The fixture
// is a temp tree; nothing here touches the real module.
func TestGHExecChokePointDetectsEveryForm(t *testing.T) {
	clean, err := ghExecFindings(writeGHTree(t, ghFixtureBase()))
	if err != nil {
		t.Fatal(err)
	}
	if len(clean) != 0 {
		t.Fatalf("clean fixture must have no finding, got:\n  %s", strings.Join(clean, "\n  "))
	}

	const ghPkgDir = "cmd/pg-connector-github/internal/github/"
	cases := []struct {
		name  string
		files map[string]string // planted on top of the base, keyed by relative path
		want  string            // "<rel>:<line>" of the planted exec
	}{
		{
			"literal",
			map[string]string{"cmd/x/a.go": "package x\n\nimport \"os/exec\"\n\nfunc f() { _ = exec.Command(\"gh\", \"pr\") }\n"},
			"cmd/x/a.go:5",
		},
		{
			"context literal",
			map[string]string{"cmd/x/a.go": "package x\n\nimport (\n\t\"context\"\n\t\"os/exec\"\n)\n\nfunc f(ctx context.Context) { _ = exec.CommandContext(ctx, \"gh\") }\n"},
			"cmd/x/a.go:8",
		},
		{
			"const indirection",
			map[string]string{"cmd/x/a.go": "package x\n\nimport \"os/exec\"\n\nconst tool = \"gh\"\n\nfunc f() { _ = exec.Command(tool, \"pr\") }\n"},
			"cmd/x/a.go:7",
		},
		{
			"const in another file of the package",
			map[string]string{
				"cmd/x/a.go": "package x\n\nvar tool = \"gh\"\n",
				"cmd/x/b.go": "package x\n\nimport \"os/exec\"\n\nfunc f() { _ = exec.Command(tool) }\n",
			},
			"cmd/x/b.go:5",
		},
		{
			"local var chain",
			map[string]string{"cmd/x/a.go": "package x\n\nimport \"os/exec\"\n\nfunc f() {\n\tb := \"gh\"\n\tc := b\n\t_ = exec.Command(c)\n}\n"},
			"cmd/x/a.go:8",
		},
		{
			"multi-line call",
			map[string]string{"cmd/x/a.go": "package x\n\nimport (\n\t\"context\"\n\t\"os/exec\"\n)\n\nfunc f(ctx context.Context) {\n\t_ = exec.CommandContext(\n\t\tctx,\n\t\t\"gh\",\n\t\t\"pr\",\n\t)\n}\n"},
			"cmd/x/a.go:9",
		},
		{
			"aliased import",
			map[string]string{"cmd/x/a.go": "package x\n\nimport osexec \"os/exec\"\n\nfunc f() { _ = osexec.Command(\"gh\") }\n"},
			"cmd/x/a.go:5",
		},
		{
			"function-valued alias",
			map[string]string{"cmd/x/a.go": "package x\n\nimport \"os/exec\"\n\nvar run = exec.Command\n\nfunc f() { _ = run(\"gh\") }\n"},
			"cmd/x/a.go:7",
		},
		{
			"absolute path",
			map[string]string{"cmd/x/a.go": "package x\n\nimport \"os/exec\"\n\nfunc f() { _ = exec.Command(\"/opt/homebrew/bin/gh\") }\n"},
			"cmd/x/a.go:5",
		},
		{
			"joined path",
			map[string]string{"cmd/x/a.go": "package x\n\nimport (\n\t\"os/exec\"\n\t\"path/filepath\"\n)\n\nfunc f(d string) { _ = exec.Command(filepath.Join(d, \"gh\")) }\n"},
			"cmd/x/a.go:8",
		},
		{
			"concatenated literal",
			map[string]string{"cmd/x/a.go": "package x\n\nimport \"os/exec\"\n\nfunc f() { _ = exec.Command(\"g\" + \"h\") }\n"},
			"cmd/x/a.go:5",
		},
		{
			"exec.Cmd literal",
			map[string]string{"cmd/x/a.go": "package x\n\nimport \"os/exec\"\n\nfunc f() *exec.Cmd {\n\treturn &exec.Cmd{\n\t\tPath: \"gh\",\n\t}\n}\n"},
			"cmd/x/a.go:7",
		},
		{
			"integration-tagged file",
			map[string]string{"cmd/x/a_integration_test.go": "//go:build integration\n\npackage x\n\nimport \"os/exec\"\n\nfunc f() { _ = exec.Command(\"gh\") }\n"},
			"cmd/x/a_integration_test.go:7",
		},
		{
			"contract-tagged GOOS file",
			map[string]string{"cmd/x/a_linux.go": "//go:build contract && linux\n\npackage x\n\nimport \"os/exec\"\n\nfunc f() { _ = exec.Command(\"gh\") }\n"},
			"cmd/x/a_linux.go:7",
		},
		{
			"configured binary: config field",
			map[string]string{"cmd/pg-connector-github/internal/run/a.go": "package run\n\nimport \"os/exec\"\n\ntype cfg struct{ GH string }\n\nfunc f(c cfg) { _ = exec.Command(c.GH, \"pr\") }\n"},
			"cmd/pg-connector-github/internal/run/a.go:7",
		},
		{
			"configured binary: parameter",
			map[string]string{"cmd/pg-connector-github/internal/run/a.go": "package run\n\nimport \"os/exec\"\n\nfunc f(gh string) { _ = exec.Command(gh, \"pr\") }\n"},
			"cmd/pg-connector-github/internal/run/a.go:5",
		},
		{
			"configured binary: default constant",
			map[string]string{"cmd/pg-connector-github/internal/run/a.go": "package run\n\nimport (\n\t\"os/exec\"\n\n\t\"example.invalid/m/cmd/pg-connector-github/internal/github\"\n)\n\nfunc f() { _ = exec.Command(github.DefaultBinary) }\n"},
			"cmd/pg-connector-github/internal/run/a.go:9",
		},
		{
			"configured binary: Options.Binary",
			map[string]string{"cmd/pg-connector-github/internal/run/a.go": "package run\n\nimport (\n\t\"os/exec\"\n\n\t\"example.invalid/m/cmd/pg-connector-github/internal/github\"\n)\n\nfunc f(o github.Options) { _ = exec.Command(o.Binary) }\n"},
			"cmd/pg-connector-github/internal/run/a.go:9",
		},
		{
			"configured binary: new method beside the choke point",
			map[string]string{ghPkgDir + "extra.go": "package github\n\nimport (\n\t\"context\"\n\t\"os/exec\"\n)\n\nfunc (r *cliGHRunner) other(ctx context.Context) *exec.Cmd {\n\treturn exec.CommandContext(ctx, r.bin, \"api\")\n}\n"},
			ghPkgDir + "extra.go:9",
		},
		{
			"configured binary: accessor in a new function",
			map[string]string{ghPkgDir + "extra.go": "package github\n\nimport \"os/exec\"\n\nfunc raw(r *cliGHRunner) *exec.Cmd { return exec.Command(r.binary(), \"api\") }\n"},
			ghPkgDir + "extra.go:5",
		},
		{
			"configured binary: second exec in ghexec.go outside command()",
			map[string]string{ghPkgDir + "ghexec.go": strings.Replace(ghFixtureBase()[ghPkgDir+"ghexec.go"], "func (r *cliGHRunner) command(", "func (r *cliGHRunner) sneak(ctx context.Context) *exec.Cmd {\n\treturn exec.CommandContext(ctx, r.bin)\n}\n\nfunc (r *cliGHRunner) command(", 1)},
			ghPkgDir + "ghexec.go:13",
		},
		{
			"configured binary: second exec in token.go outside the resolver",
			map[string]string{ghPkgDir + "token.go": ghFixtureBase()[ghPkgDir+"token.go"] + "\nfunc another(ctx context.Context, bin string) *exec.Cmd { return exec.CommandContext(ctx, bin) }\n"},
			ghPkgDir + "token.go:12",
		},
		{
			"configured binary: test file in the package",
			map[string]string{ghPkgDir + "x_test.go": "package github\n\nimport \"os/exec\"\n\nfunc g(r *cliGHRunner) { _ = exec.Command(r.bin) }\n"},
			ghPkgDir + "x_test.go:5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := ghFixtureBase()
			for k, v := range tc.files {
				files[k] = v
			}
			got, err := ghExecFindings(writeGHTree(t, files))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !strings.HasPrefix(got[0], tc.want+":") {
				t.Fatalf("want exactly one finding at %s, got %d:\n  %s", tc.want, len(got), strings.Join(got, "\n  "))
			}
		})
	}
}

// TestGHExecChokePointRealChokeFilesAreClean anchors the fixtures to the real
// code: pg-connector-github's own ghexec.go and token.go, copied into a temp
// tree, produce no finding (their two execs sit in the allowlisted functions),
// and a copy of ghexec.go with an exec of the configured binary added in a
// second function produces exactly that one finding. This is what would break
// if a refactor renamed command or ghAuthTokenCommand without updating
// ghChokePoints.
func TestGHExecChokePointRealChokeFilesAreClean(t *testing.T) {
	const dir = "cmd/pg-connector-github/internal/github/"
	real := map[string]string{}
	for _, f := range []string{"ghexec.go", "token.go"} {
		b, err := os.ReadFile(filepath.Join(moduleRoot(t), filepath.FromSlash(dir+f)))
		if err != nil {
			t.Fatal(err)
		}
		real[dir+f] = string(b)
	}
	got, err := ghExecFindings(writeGHTree(t, real))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("the real choke-point files must be clean, got:\n  %s", strings.Join(got, "\n  "))
	}

	real[dir+"ghexec.go"] += "\nfunc (r *cliGHRunner) sneak(ctx context.Context) *exec.Cmd {\n\treturn exec.CommandContext(ctx, r.bin, \"api\")\n}\n"
	got, err = ghExecFindings(writeGHTree(t, real))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "ghexec.go:") || !strings.Contains(got[0], "r.bin") {
		t.Fatalf("want exactly the planted exec flagged, got:\n  %s", strings.Join(got, "\n  "))
	}
}
