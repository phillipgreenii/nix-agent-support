package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Relocated from
// cmd/pg-connector-pr-github/internal/github/stack_readonly_test.go (bead
// pg2-lh3c4, design §9.1's acceptance criteria) alongside its sibling
// TestGHExecChokePoint (chokepoint_test.go, same package, same reason: both
// walked the whole module from inside one backend's own test package,
// which would have silently dropped this guard had pr-github been deleted
// or restructured first).

// stackMutatingVerbs are the `gh stack <verb>` subcommands that MUTATE a
// native GitHub stack -- author it, extend it, or tear it down -- as opposed
// to `gh stack view`/`gh stack view --json`, which is read-only and outside
// this guard's concern.
//
// Both verbs named here are documented, in the vendored gh-stack skill, as
// remote-first and API-driven: "link" needs no local tracking state at all
// (its own heading is "no local tracking"), and "unstack <number>" is
// explicitly "a remote-first API wrapper ... safe for non-interactive use ...
// from anywhere in the repo, tracked locally or not". So a headless pg-pr
// daemon reaching either one is not a hypothetical this guard hedges
// against for no reason -- "we cannot reach the CLI headlessly" is
// specifically NOT a safety argument for them, which is exactly why bead
// pg2-4dz88.3 (the umbrella this leaf implements) names both explicitly
// rather than leaving "no stack authoring" as an unenforced intention.
var stackMutatingVerbs = map[string]bool{
	"link":    true,
	"unstack": true,
}

// TestNoGHStackMutatingArgv is the mechanical half of pg-pr's read-only
// promise for stacked-PR identification (bead pg2-4dz88.3.8): pg-pr may
// IDENTIFY a native GitHub stack (internal/prdeps, fed from PR.StackEntry /
// PR.NativeUpstreamHead) but must never author, link, or unstack one. It
// copies TestGHExecChokePoint's enforcement STYLE exactly -- walk the module
// from moduleRoot(t), skip .git/vendor/testdata, fail with the offending
// file:line -- applied to a different pattern set: a `gh stack <verb>`
// argument vector for a mutating verb, and a GraphQL mutation that names a
// stack.
//
// Two things make this scan structurally, not just conventionally, immune to
// tripping on this very file's own doc comments (which must name both verbs
// explicitly, per the bead's own acceptance criteria) or on legitimate test
// fixtures:
//
//  1. It scans go/ast string literals (*ast.BasicLit, token.STRING), not raw
//     source text. parser.ParseFile is called here in its default mode,
//     which never attaches comments to the tree at all, so nothing a "//" or
//     "/* */" comment says -- including this paragraph -- is ever visible to
//     the walk as a candidate literal.
//  2. It excludes every "_test.go" file from the scanned set, in addition to
//     the testdata directory chokepoint already skips. That is "test
//     fixtures" from this test's own acceptance criteria ("outside test
//     fixtures/comments"): a fixture asserting something else about a
//     stack-shaped string is not itself a mutating call, and this file is
//     one such "_test.go" file.
func TestNoGHStackMutatingArgv(t *testing.T) {
	root := moduleRoot(t)

	var offenders []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}

		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}

		// Every string literal in the file, in source order, with its line
		// number. Building this list first (rather than reacting inline)
		// keeps the two checks below independent of AST traversal order.
		type lit struct {
			value string
			line  int
		}
		var lits []lit
		ast.Inspect(file, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			v, uerr := strconv.Unquote(bl.Value)
			if uerr != nil {
				// A raw (backtick) multi-line string with characters
				// strconv.Unquote balks at (e.g. an embedded backtick some
				// other way) -- fall back to the delimited literal text
				// itself so the GraphQL-mutation substring check below still
				// has something to search.
				v = bl.Value
			}
			lits = append(lits, lit{value: v, line: fset.Position(bl.Pos()).Line})
			return true
		})

		// Check 1: a `gh stack <mutating-verb>` argument vector. This
		// module's convention (ghexec.go, github.CLI.Run/RunStdin) passes gh
		// arguments as a sequence of plain string literals -- "stack",
		// "link", ... or "stack", "unstack", <number>, ... -- so two
		// adjacent string literals spelling "stack" then a mutating verb is
		// exactly that shape, in a []string{...} composite literal or a
		// Run/RunStdin call's argument list alike, on one line or spread
		// across several.
		for i := 0; i+1 < len(lits); i++ {
			if lits[i].value != "stack" {
				continue
			}
			if verb := lits[i+1].value; stackMutatingVerbs[verb] {
				offenders = append(offenders, fmt.Sprintf("%s:%d: `gh stack %s` argument vector", rel, lits[i].line, verb))
			}
		}

		// Check 2: a GraphQL mutation naming a stack. This module's existing
		// mutations (github.go's addPullRequestReviewThreadReplyMutation,
		// resolveReviewThreadMutation, minimizeCommentMutation) are each one
		// backtick raw-string constant whose body contains the literal
		// keyword "mutation" followed by the GraphQL operation it invokes.
		// No stack-naming mutation exists in the module today, so this
		// checks the SHAPE a future one would take: a single string literal
		// containing both "mutation" and "stack" (case-insensitive) is
		// exactly what a `mutation($id: ID!) { linkStack(...) }`-style
		// addition would look like, and is narrow enough that it does not
		// fire on any of the three existing mutation constants (none
		// mentions a stack) or on unrelated call-stack/type-stack prose,
		// since that prose lives in comments, which this scan never sees.
		for _, l := range lits {
			lower := strings.ToLower(l.value)
			if strings.Contains(lower, "mutation") && strings.Contains(lower, "stack") {
				offenders = append(offenders, fmt.Sprintf("%s:%d: GraphQL string literal names both a mutation and a stack", rel, l.line))
			}
		}

		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk module: %v", walkErr)
	}
	if len(offenders) > 0 {
		t.Fatalf("pg-pr must never author, link, or unstack a native GitHub stack "+
			"(it may only IDENTIFY one -- see internal/prdeps), but found:\n  %s\n\n"+
			"Both `gh stack link` and `gh stack unstack <number>` are remote-first, "+
			"API-driven verbs the vendored gh-stack skill documents as reachable "+
			"without a local checkout, so there is no headless-CLI argument for "+
			"leaving either one reachable.",
			strings.Join(offenders, "\n  "))
	}
}
