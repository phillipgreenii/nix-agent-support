package setup

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestEarlyBandDecisionSitesCarryRationaleComments enforces the rationale-comment
// convention adopted in pg2-3rz87 (deciding pg2-4yy4r's open question): every
// non-obvious decision site in the early band carries a comment stating (a) why
// THIS verdict level and not the adjacent one, (b) the evidence or bead it derives
// from, and (c) what observation would justify changing it. The exemplar is
// `internal/rules/gh/gh.go`'s `pr merge --auto` branch — read it for the expected
// shape before adding a new decision site anywhere this test covers.
//
// # Why the scope is Approve and terminal-NoOpinion too, not just Ask/Reject
//
// pg2-4yy4r originally specified enforcement over Ask/Reject sites only. That would
// have caught NONE of the four P0 findings the 2026-07-30/31 session made while
// deciding this bead — every one of them lived at an Approve site or a matching
// predicate wired the wrong direction: `gh api` blanket-approved as "read-only"
// (pg2-cl0v2), `git reset --har` approved with the reason "git reset (soft) is
// safe" (pg2-os1kq, an abbreviated-flag miss), `git branch --delete --force`
// approved with NO comment at all (pg2-os1kq), and a credential readable through
// one variable hop (`F=~/.ssh/id_rsa; cat $F`) approved because the dynamic-value
// guard was wired to writes only (pg2-2ke04). So this test enumerates Approve and
// terminal-NoOpinion sites IN ADDITION to Ask/Reject — there was no enforcement of
// any of the four in the tree before this bead; it lands the union in one place
// rather than extending a narrower test that never existed as code.
//
// # Scope: the early band
//
// gitdir, dangerouscmds, secrets, envvars — the rules that run ahead of the
// generic approvers (path-safety, safe-commands) in setup.RuleChain, where a
// decisive verdict of any kind short-circuits everything after it (first-match-
// wins; see RuleChain's own ordering comments in factory.go). pathtraversal was
// ALSO named in pg2-4yy4r's acceptance criteria "if it still exists" — it does
// not: it was deleted by pg2-bn7sx per the pg2-4yy4r operator ruling (item 6),
// confirmed by `ls internal/rules/` on this tree 2026-08-18 finding no such
// directory. It is omitted here rather than silently globbed, so a future
// reintroduction is a deliberate edit to the pkgs list below, not an accident.
//
// # Distinguishing a decision from a non-decision
//
// Read against TODAY's vocabulary (ADR 0043's NoOpinion + ErrNotApplicable, ADR
// 0044's ErrRefused/Refuse), confirmed by grepping internal/hookio for both names
// before writing this enumeration:
//
//   - ErrNotApplicable ("this rule does not govern this input", hookio.NotApplicable)
//     is a bare sentinel error with a ZERO-VALUE RuleResult — no Decision, no Reason,
//     nothing to attach a rationale to. It is structurally invisible to this scan by
//     construction, which is exactly right: a non-participation is not a decision and
//     pg2-3rz87's acceptance criteria excludes it explicitly.
//   - A FOLD SEED / IDENTITY value (envvars.Evaluate's `hookio.RuleResult{Decision:
//     hookio.NoOpinion, Module: r.Name()}`, used as the MostRestrictive fold's
//     starting point before any sub-verdict is examined) sets Decision but carries
//     no Reason — there is nothing yet to justify. This scan treats "a Reason field
//     is present in the literal" as the proxy for "this is an actual verdict, not a
//     placeholder", which is true of every genuine decision literal audited across
//     the four packages (confirmed by inspection: every Reject/Ask/Approve literal
//     sets Reason; both NoOpinion seeds do not).
//   - A NoOpinion literal passed directly to hookio.Refuse (ADR 0044) is a FLOORED
//     REFUSAL, not a terminal decision: the chain continues and a later rule's
//     stronger verdict still wins. Only a NoOpinion returned with a nil error —
//     genuinely terminal, per ADR 0043's "NoOpinion ... is terminal: the engine
//     stops the chain on it" — is in scope. This scan excludes the direct
//     `hookio.Refuse(hookio.RuleResult{Decision: hookio.NoOpinion, ...})` shape;
//     none of the four packages currently uses it, so this is forward guarding
//     rather than a live exclusion today.
//   - Approve, Ask and Reject are ALWAYS decisive regardless of how they are
//     threaded to the caller (a direct return, an intermediate variable, a
//     MostRestrictive fold argument), so this scan does not attempt to prove
//     terminality for them the way it does for NoOpinion — constructing one of
//     these three verdicts at all is the decision point worth documenting.
//
// # What "has a rationale comment" means here, and its granularity
//
// This test checks per ENCLOSING FUNCTION, not per branch/case: a function that
// constructs at least one qualifying decision literal must carry at least one
// comment somewhere in its own doc comment or body (checked via go/ast's
// CommentMap, bounded to the function's own [Pos,End) span so a same-file
// PACKAGE doc or a sibling function's doc can never satisfy it by proximity
// alone). That is the granularity the failure message itself is written at
// (criterion: name the site precisely enough to fix without hunting — module
// plus enclosing function), and it matches this codebase's actual commenting
// style: a shared rationale for multiple sibling branches often sits once on the
// enclosing function (e.g. secrets.decide's doc explains both its Reject and Ask
// branches in one place) rather than being repeated per case.
//
// # THE ACCEPTED LIMITATION — read this before trusting a green run
//
// A test can only check that a rationale comment EXISTS, never that it is TRUE.
// Two comments in this tree's own history were both present AND both false: the
// `gh api` branch's "read-only gh api" reason (pg2-cl0v2 — `gh api --method PUT
// .../merge` bypassed the `gh pr merge` Reject) and the `git reset` branch's
// "git reset (soft) is safe" reason (pg2-os1kq — an abbreviated `--har` spelling
// still performed a hard reset). This test raises the floor from "some sites have
// no comment at all" to "every in-scope decision site has SOME comment"; it is
// NOT a substitute for adversarial review of what that comment claims, and a
// green run here MUST NOT be cited as evidence that any specific rationale is
// correct.
func TestEarlyBandDecisionSitesCarryRationaleComments(t *testing.T) {
	root := rationaleModuleRoot(t)
	// pathtraversal intentionally absent — see the doc comment above.
	pkgs := []string{"gitdir", "dangerouscmds", "secrets", "envvars"}
	allow := rationaleAllowlistIndex(t)

	var failures []string
	for _, pkg := range pkgs {
		dir := filepath.Join(root, "internal", "rules", pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		fset := token.NewFileSet()
		for _, de := range entries {
			name := de.Name()
			if de.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			cmap := ast.NewCommentMap(fset, file, file.Comments)
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				decisions := decisionSitesIn(fd)
				if len(decisions) == 0 {
					continue
				}
				if hasNearbyComment(cmap, fd) {
					continue
				}
				sig := funcSignature(fd)
				if reason, ok := allow[pkg+"."+sig]; ok {
					t.Logf("rationale-convention allowlisted: %s.%s (%s) — %s", pkg, sig, strings.Join(decisions, ","), reason)
					continue
				}
				sort.Strings(decisions)
				failures = append(failures, fmt.Sprintf(
					"%s: func %s returns %s with no nearby rationale comment (want: why this level and not the adjacent one, the evidence/bead it derives from, what would justify changing it — see internal/rules/gh/gh.go's `pr merge --auto` branch for the shape)",
					pkg, sig, strings.Join(decisions, ", "),
				))
			}
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		t.Fatalf("%d early-band decision site(s) missing a rationale comment:\n  %s",
			len(failures), strings.Join(failures, "\n  "))
	}
}

// rationaleAllowEntry is one EXPLICIT, DATED, INDIVIDUALLY-JUSTIFIED exemption from
// TestEarlyBandDecisionSitesCarryRationaleComments. A blanket/wildcard entry is
// FORBIDDEN — rationaleAllowlistIndex rejects one mechanically — because a
// catch-all would defeat the entire point of the enforcement test.
type rationaleAllowEntry struct {
	pkg    string // early-band package name, e.g. "dangerouscmds"
	fn     string // funcSignature output, e.g. "Rule.Evaluate" or "(*Rule).verdict"
	date   string // when this exemption was recorded, YYYY-MM-DD
	reason string // why a real rationale could not be written yet, one line
}

// rationaleAllowlist is currently empty: every decision site this test's first run
// surfaced (pg2-3rz87) was fixed with a real rationale comment rather than
// exempted. Kept as a named, checked mechanism rather than removed outright so a
// future genuinely-hard case has a place to go WITHOUT inventing a new bypass —
// see the doc comment above and rationaleAllowlistIndex for the no-wildcard rule.
var rationaleAllowlist = []rationaleAllowEntry{}

// rationaleAllowlistIndex validates and indexes rationaleAllowlist. Every field is
// required and neither pkg nor fn may be a wildcard — an incomplete or wildcard
// entry fails the test immediately rather than silently exempting more than one
// site.
func rationaleAllowlistIndex(t *testing.T) map[string]string {
	t.Helper()
	idx := make(map[string]string, len(rationaleAllowlist))
	for _, e := range rationaleAllowlist {
		if e.pkg == "" || e.fn == "" || e.date == "" || e.reason == "" {
			t.Fatalf("rationaleAllowlist entry incomplete (pkg, fn, date and reason are all required): %+v", e)
		}
		if e.pkg == "*" || e.fn == "*" || strings.Contains(e.pkg, "*") || strings.Contains(e.fn, "*") {
			t.Fatalf("rationaleAllowlist entry %s.%s is a wildcard — blanket allowlist entries are FORBIDDEN, every entry must individually justify one function", e.pkg, e.fn)
		}
		key := e.pkg + "." + e.fn
		if _, dup := idx[key]; dup {
			t.Fatalf("rationaleAllowlist has a duplicate entry for %s", key)
		}
		idx[key] = fmt.Sprintf("allowlisted %s: %s", e.date, e.reason)
	}
	return idx
}

// decisionSitesIn returns the Decision names (Approve/NoOpinion/Ask/Reject) of
// every qualifying hookio.RuleResult composite literal in fd's body — one entry
// per literal, duplicates included, since the caller only needs to know whether
// the SET is non-empty and what it contains for the failure message.
//
// "Qualifying" is deliberately narrow — see the package-level doc comment on
// TestEarlyBandDecisionSitesCarryRationaleComments for the full reasoning:
//   - the literal's type must be hookio.RuleResult;
//   - it must set a Decision field to one of hookio.{Approve,NoOpinion,Ask,Reject};
//   - it must ALSO set a Reason field (any value) — the proxy that distinguishes a
//     genuine verdict from a fold seed/identity value; and
//   - a NoOpinion literal passed directly as the sole argument of hookio.Refuse is
//     excluded — ADR 0044's floored refusal, not a terminal decision.
func decisionSitesIn(fd *ast.FuncDecl) []string {
	var found []string
	var stack []ast.Node
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if lit, ok := n.(*ast.CompositeLit); ok {
			if decision, hasReason := ruleResultFields(lit); decision != "" && hasReason {
				if decision != "NoOpinion" || !directRefuseArgument(stack) {
					found = append(found, decision)
				}
			}
		}
		stack = append(stack, n)
		return true
	})
	return found
}

// ruleResultFields inspects one composite literal and reports the Decision it
// names (hookio.Approve/NoOpinion/Ask/Reject, or "" if the literal is not of type
// hookio.RuleResult or sets no recognized Decision) and whether it also sets a
// Reason field.
func ruleResultFields(lit *ast.CompositeLit) (decision string, hasReason bool) {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok || pkgIdent.Name != "hookio" || sel.Sel.Name != "RuleResult" {
		return "", false
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Decision":
			if vsel, ok := kv.Value.(*ast.SelectorExpr); ok {
				if vpkg, ok := vsel.X.(*ast.Ident); ok && vpkg.Name == "hookio" {
					switch vsel.Sel.Name {
					case "Approve", "NoOpinion", "Ask", "Reject":
						decision = vsel.Sel.Name
					}
				}
			}
		case "Reason":
			hasReason = true
		}
	}
	return decision, hasReason
}

// directRefuseArgument reports whether the node at the top of stack (the
// immediate syntactic parent of the literal currently being inspected) is a call
// to hookio.Refuse — i.e. the literal is that call's argument written inline,
// `hookio.Refuse(hookio.RuleResult{...})`, rather than threaded through a
// variable. See decisionSitesIn's doc for why this shape is excluded.
func directRefuseArgument(stack []ast.Node) bool {
	if len(stack) == 0 {
		return false
	}
	call, ok := stack[len(stack)-1].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	return ok && pkgIdent.Name == "hookio" && sel.Sel.Name == "Refuse"
}

// hasNearbyComment reports whether fd carries any comment at all: its own doc
// comment, or any comment associated (via cmap, go/ast's own attachment
// heuristic) with a node that lies entirely within fd's [Pos,End) span. Bounding
// to fd's span is what keeps a package doc comment (whose span covers the WHOLE
// file, including every sibling function) from satisfying every function in the
// file by proximity alone — see the "granularity" section of this test's doc
// comment.
func hasNearbyComment(cmap ast.CommentMap, fd *ast.FuncDecl) bool {
	if fd.Doc != nil {
		return true
	}
	for node, cgs := range cmap {
		if len(cgs) == 0 {
			continue
		}
		if node.Pos() >= fd.Pos() && node.End() <= fd.End() {
			return true
		}
	}
	return false
}

// funcSignature renders fd as "<ReceiverType>.<Name>" (e.g. "Rule.Evaluate") or
// bare "<Name>" for a free function — the "enclosing function" half of this
// test's required failure-message granularity (module name plus enclosing
// function).
func funcSignature(fd *ast.FuncDecl) string {
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		if t := recvTypeName(fd.Recv.List[0].Type); t != "" {
			return t + "." + fd.Name.Name
		}
	}
	return fd.Name.Name
}

func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

// rationaleModuleRoot walks up from the test's working directory to the nearest
// go.mod, the same way internal/hookio/adr0043_test.go's moduleRoot does (that
// helper is unexported to its own package, so it is re-derived here rather than
// imported).
func rationaleModuleRoot(t *testing.T) string {
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
			t.Fatal("go.mod not found walking up from the test's working directory")
		}
		dir = parent
	}
}
