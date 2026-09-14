package pathspec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WORKSPACE VERB-DISCOVERY FACET (tc-8og1 item 3, the build-tool family
// design; first of five planned sub-slices — treefmt schema,
// request/rules.json wiring, cmddesc child-expression descriptor, and nix
// run installable vetting come after, in later slices).
//
// Operator ruling (Phillip, 2026-09-08, verbatim on tc-vn5z, recorded there
// and quoted on tc-8og1 item 3):
//
//	Q1 verb discovery: live static/lexical parsing for
//	justfile/package.json/devbox.json; rules.json data is reserved only for
//	flake.nix apps.
//	Q3 WORKSPACE vouching: WORKSPACE vouches only for verbs literally
//	defined in-project; operator data (rules.json) governs everything
//	reached by reference.
//
// This file adds the DISCOVERY primitive those two rulings call for: a new
// Kind.Verbs facet (workspace.go), alongside the existing
// Markers/Rules/Classify/Roots/Commands/Secrecy facets, that answers "which
// verb (recipe/script/task) names does the project's OWN file at this root
// literally define?" — a project's justfile, package.json, or devbox.json.
// It does NOT decide anything about a command yet (no new EffectKind, no
// evalcontract.Request field, no effectpolicy policy wiring) — those are
// later sub-slices (tc-8og1 item 3's slice order); this slice is the
// building block they will consume: "is verb V among the verbs kind K
// discovers at root R" answers Q3's "literally defined in-project" test.
//
// # Why these three formats, and not others (Q1)
//
// justfile and package.json/devbox.json's `scripts`/`shell.scripts` are
// SAFE to discover with a plain STATIC/LEXICAL read — package.json and
// devbox.json are plain JSON (parsed here with encoding/json, a real
// parser, not a text scan), and a justfile's recipe HEADERS (the
// `name params:` line a recipe starts with) are simple enough to recognize
// without evaluating the file's own expression language (variable
// interpolation, `if`/`else`, imports) — this file's scanner only ever
// reads header LINES, never a recipe's body or a variable's value.
// flake.nix's `apps.<system>.<name>` is deliberately NOT covered here: Nix
// is a full, Turing-complete language, so the app set may be COMPUTED
// (attribute-set comprehensions, imports, `flake-utils`/`flake-parts`
// generators) — discovering it would require evaluating the flake, not
// scanning it. Per the ruling, that stays operator-declared rules.json data
// (or a future eval-backed step), not this facet's job.
//
// # Fail-safe, never fail-open
//
// A false NEGATIVE here (a real recipe/script this scanner misses) is
// merely under-discovery: a later policy built on this facet would abstain
// on that verb rather than treating it as project-tied, the same outcome as
// if the file didn't define the verb at all. A false POSITIVE (a verb
// reported that the file does not actually define) would let a later
// policy VOUCH for something that isn't really there — exactly what Q3's
// ruling exists to prevent. Every parser in this file is therefore written
// to prefer silence over a guess: a malformed file, an unreadable file, or
// a line whose shape it cannot confidently classify contributes NOTHING,
// never a best-effort guess.

// justKind: a `just` command-runner project — a justfile at the candidate
// root, under any of the four names `just` itself searches for
// (justfile/Justfile/.justfile/.Justfile). Silent on path classification
// (just has no build/cache convention of its own to declare, unlike
// gradleKind's build/); its only opinion is Verbs — see justfileVerbs.
var justKind = Kind{
	Name:    "just",
	Markers: []string{"justfile", "Justfile", ".justfile", ".Justfile"},
	Verbs:   justfileVerbs,
}

// npmKind: an npm-ecosystem project — package.json at the candidate root,
// regardless of which package manager (npm/yarn/pnpm/bun) actually runs the
// scripts; the discoverable verb set is the same either way. Silent on path
// classification. Verbs are the "scripts" object's keys — see
// packageJSONVerbs.
var npmKind = Kind{
	Name:    "npm",
	Markers: []string{"package.json"},
	Verbs:   packageJSONVerbs,
}

// devboxKind: a devbox project — devbox.json at the candidate root. Silent
// on path classification. Verbs are the "shell.scripts" object's keys — see
// devboxVerbs.
var devboxKind = Kind{
	Name:    "devbox",
	Markers: []string{"devbox.json"},
	Verbs:   devboxVerbs,
}

// justfileVerbs discovers a justfile's top-level recipe names by a
// best-effort STATIC LINE SCAN (no evaluation of just's own expression
// language). It reads whichever of the kind's marker names exists first
// (just itself does not support more than one being present, so "first
// found" mirrors what `just` itself would actually run).
//
// Recognized recipe HEADER line shape, after trimming: an optional leading
// `@` (the quiet-recipe modifier), then an identifier-shaped first token
// (letters/digits/`_`/`-`, matching just's own recipe-name grammar,
// including a leading `_` for a private recipe — still literally defined
// in-project, so still discovered), then anything up to a `:` that is NOT
// immediately followed by `=` (which would make the line a `name := value`
// variable/alias assignment, not a recipe header). Only TOP-LEVEL lines
// (no leading whitespace) are considered — a recipe's own body is always
// indented, and continuation lines inside a body would otherwise be
// mistaken for new headers. Comments (`#`) and attribute lines (`[...]`,
// e.g. `[private]` on the line above a recipe) are skipped; they never
// carry a recipe name of their own regardless of this scan's identifier
// check.
//
// Known limitations (documented, not fixed — this is a best-effort
// discovery facet, not a just parser): a recipe header split across
// multiple lines with a trailing `\` continuation is not recognized: it
// yields a false negative (see this file's fail-safe design note), not a
// misattributed verb.
func justfileVerbs(root string) []string {
	path, ok := firstExistingFile(root, []string{"justfile", "Justfile", ".justfile", ".Justfile"})
	if !ok {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var verbs []string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			// Blank, or indented (a recipe body / continuation line, never
			// a top-level header) — top-level headers only.
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
			continue // comment, or an attribute line like [private]
		}
		trimmed = strings.TrimPrefix(trimmed, "@") // quiet-recipe modifier
		before, after, ok := strings.Cut(trimmed, ":")
		if !ok || strings.HasPrefix(after, "=") {
			continue // not a `name ...:` header, or a `name := value` assignment
		}
		name := firstToken(before)
		if isJustRecipeName(name) {
			verbs = append(verbs, name)
		}
	}
	return verbs
}

// isJustRecipeName reports whether s is shaped like a valid just recipe
// name: non-empty, every rune a letter, digit, `_`, or `-`.
func isJustRecipeName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isJustRecipeRune(r) {
			return false
		}
	}
	return true
}

// isJustRecipeRune reports whether r is a letter, digit, `_`, or `-` — the
// allowed character set for a just recipe name.
func isJustRecipeRune(r rune) bool {
	return r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// firstToken returns s's first whitespace-separated field, or "" if s has
// none.
func firstToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// firstExistingFile returns the absolute path of the first of names that
// exists as a regular file directly under root, or ok=false if none do.
func firstExistingFile(root string, names []string) (path string, ok bool) {
	for _, n := range names {
		p := filepath.Join(root, n)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, true
		}
	}
	return "", false
}

// packageJSONVerbs discovers a package.json's `scripts` object's keys.
// package.json is plain JSON, so this parses it with encoding/json (a real
// parser) rather than scanning text; a malformed file, or one with no
// `scripts` object, yields no verbs.
func packageJSONVerbs(root string) []string {
	var doc struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if !readJSONFile(filepath.Join(root, "package.json"), &doc) {
		return nil
	}
	return sortedKeys(doc.Scripts)
}

// devboxVerbs discovers a devbox.json's `shell.scripts` object's keys —
// same rationale and mechanism as packageJSONVerbs; devbox.json is also
// plain JSON. A script's value may be a single command string or an array
// of command strings (devbox's own schema); this facet only needs the
// NAMES, so it decodes the value as json.RawMessage without caring which
// shape it is.
func devboxVerbs(root string) []string {
	var doc struct {
		Shell struct {
			Scripts map[string]json.RawMessage `json:"scripts"`
		} `json:"shell"`
	}
	if !readJSONFile(filepath.Join(root, "devbox.json"), &doc) {
		return nil
	}
	return sortedKeys(doc.Shell.Scripts)
}

// readJSONFile reads path and decodes it into out, reporting ok=false
// (silently — see this file's fail-safe design note) on any read or parse
// error.
func readJSONFile(path string, out any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, out) == nil
}

// sortedKeys returns m's keys in sorted order, or nil for an empty/nil map
// — a deterministic Verbs result independent of Go's randomized map
// iteration order.
func sortedKeys(m map[string]json.RawMessage) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// VerbSet is one workspace kind's discovered project-tied verbs at one
// root — DiscoveredVerbs' per-(kind,root) result.
type VerbSet struct {
	// Kind is the discovering Kind's Name ("just", "npm", "devbox", ...).
	Kind string
	// Root is the absolute root the verbs were discovered at.
	Root string
	// Verbs are the discovered verb (recipe/script/task) names.
	Verbs []string
}

// DiscoveredVerbs walks abs's ancestors for every kind with a non-nil Verbs
// facet whose Markers are present — the SAME marker-presence walk
// InsideMarkerWorkspace uses (a LOCATION question, independent of what
// Classify/Rules would say about a path, since discovery is about the
// PROJECT ROOT, not about categorizing abs itself) — and returns one
// VerbSet per (kind, root) actually found with at least one discovered
// verb, deepest root first (the same innermost-first order
// Resolve/NonSecretWith use for their own walks), registry order as the
// tie-break between equal depths. A kind whose Verbs facet returns nothing
// (no marker file after all despite the location match — Markers only
// proves presence, not readability — an empty or malformed file, or a
// project that simply defines no verbs) contributes no VerbSet: only
// FOUND, non-empty verb sets are reported, matching this package's
// established "Silent means absent from the result, not a zero-value
// entry" convention (Resolve's CatSilent, NonSecretWith's skip-on-no-
// opinion).
func DiscoveredVerbs(kinds []Kind, abs string) []VerbSet {
	abs = filepath.Clean(abs)
	type found struct {
		root  string
		order int
		kind  Kind
	}
	var candidates []found
	for i, k := range kinds {
		if k.Verbs == nil || len(k.Markers) == 0 {
			continue
		}
		for dir := abs; ; dir = filepath.Dir(dir) {
			if hasMarker(dir, k.Markers) {
				candidates = append(candidates, found{dir, i, k})
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if len(candidates[i].root) != len(candidates[j].root) {
			return len(candidates[i].root) > len(candidates[j].root)
		}
		return candidates[i].order < candidates[j].order
	})
	var out []VerbSet
	for _, c := range candidates {
		verbs := c.kind.Verbs(c.root)
		if len(verbs) == 0 {
			continue
		}
		out = append(out, VerbSet{Kind: c.kind.Name, Root: c.root, Verbs: verbs})
	}
	return out
}
