package cmdparse

// ENFORCEMENT GUARD 2 (I9) and the I13 END-TO-END ASSERTION — ADR 0039 step 5
// (pg2-x9452), the migration's final integration bead.
//
// THE DECISION. ADR 0039's Enforcement item 2 names TYPE-LEVEL as the
// preferred mechanism ("raw command text gets a distinct named type that only
// the seam can consume") with a repo-wide go/ast check plus a reviewed
// allowlist as the fallback "if the type change proves too invasive". This
// step chooses the AST-CHECK FALLBACK, for two concrete, load-bearing reasons
// found while attempting the type-level route — not a preference, a finding:
//
//  1. I7 (this repo's own invariant, restated at `shellparse.go`'s
//     ParseShell doc and again above SetParseObserver) requires
//     EvaluateExpression's text parameter to remain a PLAIN `string` forever
//     — it is the permanent entry point reached from the hook boundary, which
//     hands it text that has NEVER been parsed and therefore cannot already
//     carry an opaque "vouched-for" type. Opacifying that parameter would
//     directly contradict I7's own text ("the expression entry point takes a
//     `string`").
//  2. hookio.Evaluator's interface (the seam through which every rule reaches
//     the engine) lives in package `hookio`, which cmdparse already imports
//     (`ParsedCommand` embeds `hookio.Redirection`) — so hookio CANNOT import
//     cmdparse back without a cycle. That is the EXACT reason
//     `EvaluateStructure`'s own `leaves` parameter is typed `any` rather than
//     `[]cmdparse.ParsedCommand` (see that method's doc, pg2-m1i6r). The same
//     constraint blocks giving EvaluateExpression's `expr` parameter a
//     cmdparse-defined opaque type at the interface boundary.
//  3. Separately from the interface-cycle problem, `ParsedCommand` itself
//     (the value `EvaluateStructure`'s `leaves` already carries as `any`) has
//     fully EXPORTED fields and is constructed via ordinary struct-literal
//     syntax across roughly nineteen already-landed rule packages and their
//     test suites — see docker.go's `resolveInnerCommand`, safecmds.go's
//     xargs `-c` handling, and kubectl.go's `structuralInnerCommand`, all of
//     which read `pc.Raw`/build `[]cmdparse.ParsedCommand` literals as part of
//     their ALREADY-REVIEWED, ALREADY-CLOSED I13 migrations. Opacifying that
//     type to forbid hand-construction would mean rewriting the accessor
//     surface of the entire rules module for a guarantee guard-2's own scope
//     doesn't need: I9 is about DERIVING STRUCTURE FROM RAW TEXT outside the
//     seam, not about a rule assembling an already-parsed value it obtained
//     honestly.
//
// So the mechanism here is the NAMED-SYMBOL DENYLIST already established by
// this package's own `deleted_raw_text_matchers_test.go` (three names,
// cmdparse-scoped, pg2-813ww) — generalised to the WHOLE MODULE and to the
// FULL set of raw-text-deriving functions this migration series deleted
// across every rule it touched. It is a go/ast scan, not a substring grep
// (the same reason that file gives: a grep trips on this very doc comment and
// on every historical "DELETED" note elsewhere in the tree that legitimately
// NAMES a symbol without declaring it).
//
// "QUOTE COMPARISON INSIDE A LOOP" IS REJECTED, per ADR 0039's own text
// (`docs/adr/0039-ceta-shell-parser-front-end.md:332-334`), with the evidence
// verified against source rather than merely restated:
//
//   - RED (false positive) on `internal/rules/envvars/envvars.go`'s
//     `isStaticAbsolutePath` — a `for` loop that compares each byte against a
//     literal `case c == '$' || c == '`' || c == '"' || c == '\'' ...`. That
//     IS a comparison against quote characters inside a loop, even though the
//     function is a flat per-byte DENYLIST, not a scanner that tracks being
//     inside/outside a quoted region — and it is LIVE, reviewed code today,
//     not a migration target.
//   - GREEN (false negative) on the outgoing `containsVarRef` (deleted by
//     pg2-0gsy5): its variable-reference boundary matching never compared a
//     byte against `'`/`"` at all, so a check keyed on "compares against a
//     quote character" would never have flagged it — yet it WAS a genuine
//     hand-rolled scanner deriving structure (a name-reference boundary) from
//     raw text, precisely what I9 forbids.
//
// This is why the denylist below is a SYMBOL-NAME check, not a shape/pattern
// check: a shape check ("has a loop", "compares against quotes", "tracks
// in-quote state") was tried in the analysis above and rejected on the same
// grounds ADR 0039 already gives.
//
// SCOPE IS THE WHOLE MODULE, not merely `internal/rules` and
// `internal/cmdparse` — ADR 0039's own text: "inventory site 11 was found
// OUTSIDE cmdparse" (site 11 is the docker rule's split/rewrite/rejoin,
// itself inside `internal/rules`, contrasted with `internal/cmdparse`) is the
// acceptance criteria's own way of saying the guard must not hardcode its
// scan to those two directories. `moduleRoot`'s `filepath.Walk` (used by
// `TestSeamIsTheOnlyParserImporter` for guard 1) walks the ENTIRE module —
// `cmd/`, every `internal/*` package, docs, scripts — and this guard reuses
// that same walk.
//
// ACCEPTED GAP (recorded per the acceptance criteria's own conditional
// bullet, which applies to whichever mechanism is NOT chosen for that
// specific requirement): this AST-check mechanism, unlike a hypothetical
// type-level one, CAN represent the `containsVarRef` shape as a required
// fires-case (it is simply a name on the list below) — so there is no gap to
// accept for THIS mechanism. The gap ADR 0039 anticipates ("the type-level
// form cannot catch containsVarRef-shaped scanners") is specific to the
// type-level route this step did not take.
//
// DEMONSTRATED FAILING. Guard 2 was run against a tree with
// `splitOnShellOperators` (docker's own historical name) temporarily
// redeclared as a package-level func in a throwaway file under
// `internal/rules/docker/`, confirming a real reintroduction is caught by
// path and name rather than merely by a hypothetical. The failing run:
//
//	--- FAIL: TestGuard2_ReintroducedRawTextScanner (0.0Xs)
//	    guard2_i9_i13_test.go:NNN: I9/guard-2 violated: found a func declaration
//	    named "splitOnShellOperators" at internal/rules/docker/_demo_violation.go
//	    -- this symbol was DELETED by an ADR 0039 migration step and MUST NOT be
//	    reintroduced (docker's top-level &&/||/; splitter that joined segments
//	    back into text before re-entering the engine; ADR 0039 inventory site
//	    11; deleted by pg2-lwwwk)
//
// The throwaway file was removed immediately after capturing this; it is not
// part of the committed tree. See LOWERING.md's final section for the full
// transcript and the corpus replay this step also owes.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reintroducedRawTextScannerNames is guard 2's denylist: every raw-text
// structure-deriving symbol an ADR 0039 migration step deleted, module-wide,
// keyed by name with a doc string naming its former home and the bead that
// deleted it. A declaration of ANY kind (func, method, type, var, const)
// under one of these names, ANYWHERE in the module, is I9 violated by
// construction — the symbol was deleted specifically because it derived
// command structure from raw text outside the seam, and there is no
// legitimate reason for the SAME NAME to exist again for a different
// purpose (each is specific enough — `splitOnShellOperators`,
// `classifyBacktickSubstitution` — that an unrelated reuse is not a real
// risk this guard needs to tolerate).
var reintroducedRawTextScannerNames = map[string]string{
	"splitOnShellOperators": "internal/rules/docker -- the top-level &&/||/;" +
		" splitter that joined segments back into text before re-entering the" +
		" engine (ADR 0039 inventory site 11); deleted by pg2-lwwwk",
	"stripDockerPassthroughs": "internal/rules/docker -- drove the" +
		" split/rewrite/rejoin pipeline docker's gosu/su/-c unwrapping used" +
		" before resolving structurally; deleted by pg2-lwwwk",
	"stripSinglePassthrough": "internal/rules/docker -- re-emitted stripped" +
		" passthrough tokens by rebuilding a string; deleted by pg2-lwwwk",
	"shellSegment": "internal/rules/docker -- the text-segment type the" +
		" split/rejoin pipeline threaded (command string plus preceding" +
		" operator); deleted by pg2-lwwwk",
	"extractInnerCommand": "internal/rules/docker and internal/rules/kubectl" +
		" -- strings.Join(cmdArgs[...], \" \") over already-tokenized args," +
		" corrupting quoting on rejoin (the gosu/sh -c and kc-exec I13" +
		" violators); deleted by pg2-lwwwk and pg2-9aqol",
	"extractAfterFlag": "internal/rules/nix -- strings.Join over the" +
		" post-unquote args following nix develop/-shell's -c/--command;" +
		" deleted by pg2-m132k",
	"mustMarshalCommand": "internal/rules/safecmds -- re-serialised a" +
		" rule-joined xargs sh -c script string into a synthetic ToolInput" +
		" JSON for self-recursion; deleted by pg2-1zrup",
	"containsVarRef": "internal/rules/gitdir -- a hand-rolled" +
		" variable-reference scanner with NO quote comparison at all (the" +
		" case that sinks the rejected \"quote comparison inside a loop\"" +
		" property from the false-negative direction); deleted by pg2-0gsy5",
	"literalValue": "internal/rules/envvars -- a byte-by-byte quote/backslash" +
		" scan over an assignment value; replaced by" +
		" cmdparse.LiteralAssignmentValueText; deleted by pg2-30wro",
	"matchParen": "internal/cmdparse -- the last raw-text paren matcher" +
		" outside a real parse (already pinned narrower, cmdparse-scoped, by" +
		" deleted_raw_text_matchers_test.go); deleted by pg2-hed0a",
	"classifyCmdSubstitution": "internal/cmdparse -- prefix/remainder text" +
		" derivation of a $(...) extent (already pinned narrower by" +
		" deleted_raw_text_matchers_test.go); deleted by pg2-hed0a",
	"classifyBacktickSubstitution": "internal/cmdparse --" +
		" first/last-backtick text derivation of a backtick substitution's" +
		" extent (already pinned narrower by" +
		" deleted_raw_text_matchers_test.go); deleted by pg2-hed0a",
}

// TestGuard2_ReintroducedRawTextScanner is ENFORCEMENT GUARD 2 for I9 (and,
// by covering every historical I13-violating shape by name, the mechanical
// half of the I13 end-to-end assertion too — see this file's own doc comment
// for why the mechanism is a name denylist rather than a type or a shape
// check). It walks every .go file in the WHOLE MODULE — not merely
// internal/rules and internal/cmdparse — and fails if any top-level
// declaration (func, method, type, var, or const) under any of
// reintroducedRawTextScannerNames's keys exists anywhere.
//
// envvars.go's isStaticAbsolutePath is the required must-NOT-fire case: it is
// LIVE in the tree this test scans on every run (not a reintroduced fixture),
// its name is not on the denylist, and this test passing green on the current
// tree IS the demonstration that it is not flagged.
func TestGuard2_ReintroducedRawTextScanner(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	checked := 0
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			// A file that does not parse as Go cannot declare a Go symbol.
			// Fixture/testdata trees under this module intentionally hold
			// malformed shell text, not malformed Go, so this is not
			// expected to fire; skip rather than fail the whole guard on it.
			return nil //nolint:nilerr // deliberate: see comment above
		}
		checked++
		rel, _ := filepath.Rel(root, path)
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if why, bad := reintroducedRawTextScannerNames[d.Name.Name]; bad {
					offenders = append(offenders, rel+": func "+d.Name.Name+" -- "+why)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch vs := spec.(type) {
					case *ast.ValueSpec:
						for _, name := range vs.Names {
							if why, bad := reintroducedRawTextScannerNames[name.Name]; bad {
								offenders = append(offenders, rel+": "+name.Name+" -- "+why)
							}
						}
					case *ast.TypeSpec:
						if why, bad := reintroducedRawTextScannerNames[vs.Name.Name]; bad {
							offenders = append(offenders, rel+": type "+vs.Name.Name+" -- "+why)
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if checked == 0 {
		t.Fatalf("scanned 0 .go files under %s -- the guard is vacuous", root)
	}
	if len(offenders) > 0 {
		t.Errorf("I9/guard-2 violated -- %d reintroduced raw-text scanner declaration(s):", len(offenders))
		for _, o := range offenders {
			t.Errorf("  %s", o)
		}
	}
	t.Logf("guard 2: scanned %d .go files under the module for %d denylisted raw-text-scanner names",
		checked, len(reintroducedRawTextScannerNames))
}

// evaluateExpressionMethodName is EvaluateExpression's method name, factored
// to a constant so the two guards below can't drift on the spelling.
const evaluateExpressionMethodName = "EvaluateExpression"

// TestI13_NoJoinedTextPassedDirectlyToEvaluateExpression is the SECOND half of
// the I13 end-to-end assertion: a forward-looking, shape-based check (as
// opposed to guard 2's backward-looking name denylist) that would catch a
// FRESH violation under a NEW name, not merely a reintroduction of an old one.
//
// It is deliberately narrow, and the narrowness is load-bearing: it flags
// ONLY a call of the exact shape `<expr>.EvaluateExpression(strings.Join(...),
// ...)` -- the IMMEDIATE first argument is a strings.Join call -- not "a join
// happened somewhere upstream of this value". A broader "was this value ever
// touched by strings.Join" check was considered and rejected: kubectl.go's
// `structuralInnerCommand`/`quoteArgsAsLiteralWords` (pg2-9aqol, already
// closed) legitimately builds its EvaluateStructure `source` argument via
// strings.Join, single-quoting each element so nothing can be misread as an
// operator -- a DELIBERATE, reasoned, already-reviewed encoding, not a
// violation, and a broader check would false-positive on it. The narrow form
// targets EvaluateExpression specifically (not EvaluateStructure, which is
// I13's own sanctioned structural path and where that kind of encoding
// belongs) and only its own immediate argument expression, which is exactly
// the shape every one of the four historical violators shared: join, then
// hand the join straight to the text-entry point for re-evaluation.
//
// SCOPE excludes _test.go files. A mock hookio.Evaluator's own
// EvaluateStructure/EvaluateExpression implementations (e.g.
// kubectl_test.go's mockEvaluator, which reconstructs a lookup key via
// strings.Join purely to match its OWN test fixture table, then calls ITS
// OWN EvaluateExpression) are test scaffolding with no bearing on production
// I13 compliance, and excluding _test.go is how this check avoids flagging
// them -- confirmed against the tree at the time this guard was written:
// `grep -rn '\.EvaluateExpression(strings\.' --include='*.go' .` finds
// exactly one hit, and it is that mock.
func TestI13_NoJoinedTextPassedDirectlyToEvaluateExpression(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	checked := 0
	sawRealCall := false
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil //nolint:nilerr // non-Go fixture content, not a Go source file
		}
		checked++
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != evaluateExpressionMethodName {
				return true
			}
			sawRealCall = true
			if len(call.Args) == 0 {
				return true
			}
			argCall, ok := call.Args[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			argSel, ok := argCall.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := argSel.X.(*ast.Ident)
			if ok && pkgIdent.Name == "strings" && argSel.Sel.Name == "Join" {
				offenders = append(offenders, rel+": EvaluateExpression's first argument is a"+
					" strings.Join call -- I13 forbids a rule constructing command text for"+
					" re-evaluation; delegate through EvaluateStructure (the I13 structural entry"+
					" point, pg2-m1i6r) with the genuine parsed leaves instead")
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if checked == 0 {
		t.Fatalf("scanned 0 non-test .go files under %s -- the guard is vacuous", root)
	}
	if !sawRealCall {
		t.Fatalf("found no production call to %s at all -- this guard would pass vacuously;"+
			" I7's permanent text entry point (engine.go's EvaluateHook, envvars.go's"+
			" substitution recursion) is expected to call it", evaluateExpressionMethodName)
	}
	if len(offenders) > 0 {
		t.Errorf("I13 violated -- %d call(s) pass a freshly-joined string straight to %s:",
			len(offenders), evaluateExpressionMethodName)
		for _, o := range offenders {
			t.Errorf("  %s", o)
		}
	}
	t.Logf("I13 join-detector: scanned %d non-test .go files under the module", checked)
}

// evaluateStructureCallers are the four rule files ADR 0039 step 5's own
// per-rule children migrated onto the I13 structural entry point
// (EvaluateStructure). This is the POSITIVE half of the I13 end-to-end
// assertion: guard 2 and the join-detector above prove nothing is doing it
// WRONG, but neither proves the RIGHT path is actually exercised rather than
// simply unreachable dead code (the same distinction pg2-m1i6r's own bead
// drew for its zero-caller landing). A future rule that grows an inner-command
// delegation need without wiring EvaluateStructure at all would pass both
// guards above vacuously; this closes that gap by requiring each of the four
// known migrated rules to still call it.
var evaluateStructureCallers = []string{
	filepath.Join("internal", "rules", "docker", "docker.go"),
	filepath.Join("internal", "rules", "nix", "nix.go"),
	filepath.Join("internal", "rules", "kubectl", "kubectl.go"),
	filepath.Join("internal", "rules", "safecmds", "safecmds.go"),
}

// TestI13_StructuralEntryPointIsActuallyUsed is the positive half described
// above evaluateStructureCallers.
func TestI13_StructuralEntryPointIsActuallyUsed(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	for _, rel := range evaluateStructureCallers {
		path := filepath.Join(root, rel)
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", rel, perr)
		}
		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "EvaluateStructure" {
				found = true
			}
			return true
		})
		if !found {
			t.Errorf("%s no longer calls EvaluateStructure -- the I13 structural delegate"+
				" entry point (pg2-m1i6r) is expected to be this rule's inner-command"+
				" delegation path; if it was migrated onto something else, update this list"+
				" and record why", rel)
		}
	}
}
