package deletable

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// WORKSPACE DECLARATIONS (tc-z806.3) — a declaration kind separate from
// command schemas. Operator direction (Phillip, 2026-09-07, verbatim on
// tc-z806): "different languages and toolsets may define common 'cache' or
// 'build' paths which could be removed ... gradle will use a relative build
// directory. that path could be removed via gradle commands, but could also
// be approved for removal via rm because it was categorized through gradle
// as being a deletable path. the only catch on this is a tool like gradle
// would need a way to declare how to identify a 'gradle project'. ... i
// don't think this is a command declaration, but a new kind of declaration.
// something like a 'workspace' declaration. it can specify the relative
// structure for classification purposes. it would also include references
// to commands, if that is necessary. examples ... could be for git, pn
// workspace, gradle, go, home directory ... some kind of declaration of how
// to identify the path and then some relative path information for
// categorization."
//
// # Design
//
// A Kind is DATA: (a) how to IDENTIFY a root of this kind on the filesystem
// — marker files or directories relative to a candidate root (settings.gradle
// for gradle, go.mod for go, pn-workspace.toml for a pn workspace, .git for
// git), or an environment-derived root ($HOME for home, temproot.Roots for
// temp); (b) how to CLASSIFY a root-relative path into a Category — a list
// of gitignore-syntax Rules for the common case, or a Classify func when the
// kind needs to consult the filesystem (git reads the repository's own
// ignore rules); (c) optional ABSOLUTE roots the kind declares wherever they
// live (go's build cache under $HOME, not under the go.mod root); and (d)
// Commands: the names of command schemas whose effects target those paths
// (gradle clean removes build/), recorded so the tool's own command and a
// plain `rm -rf build` are seen to share ONE classification — informational
// today, since no gradle schema exists yet.
//
// Resolution of one absolute path (Resolve):
//
//  1. Collect every (root, kind) the path lies under: each ancestor directory
//     holding a kind's marker, $HOME for home, each temproot.Roots entry for
//     temp, and each kind's declared absolute roots.
//  2. Ask each candidate for its Category of the path.
//  3. PROTECTED FROM ANY CANDIDATE WINS (the parent design: protections
//     always win over deletable). Otherwise the INNERMOST candidate with an
//     opinion decides — ordered by root depth, deepest first, so a gradle
//     project inside a git tree inside $TMPDIR is judged by gradle for
//     build/, by git for everything gradle is silent on, and never by temp,
//     because git is never silent inside its tree (Keep is an opinion: the
//     tree's content is not disposable just because of where the tree
//     sits). Outer kinds (home, temp) apply only where every inner kind is
//     silent.
//  4. No opinion anywhere: Silent, and Classify falls back to the zone
//     model's writability alone.
//
// Categories today are Deletable / Keep / Protected plus Silent
// (extensible; the bead's open question recommended two and a third — Keep,
// "the workspace's own content, needs consent" — turned out necessary so an
// inner kind can HOLD a path against an outer deletable source).
//
// Declaration shape for consumers: built-in Go values here (the same pattern
// as command schemas); a rules.json extension point is deliberately not
// built in this bead.

// Category is a workspace kind's opinion about one of its paths.
type Category int

const (
	// Silent: the kind has no opinion; an outer kind, or the zone model,
	// decides.
	CatSilent Category = iota
	// Deletable: disposable (cache, build output, ignored, temporary).
	// Deletable IMPLIES writable: a declaration that a path is disposable
	// vouches for removing it even where the zone model is merely unknown.
	CatDeletable
	// Keep: the workspace's own content — writable does not mean disposable;
	// a delete needs consent. An inner kind uses it to hold a path against
	// an outer Deletable source.
	CatKeep
	// Protected: must not be removed by a plain delete (repository
	// metadata, worktree administration, credential stores). Wins over every
	// other category from any candidate.
	CatProtected
)

// String returns the deterministic category name.
func (c Category) String() string {
	switch c {
	case CatSilent:
		return "silent"
	case CatDeletable:
		return "deletable"
	case CatKeep:
		return "keep"
	case CatProtected:
		return "protected"
	default:
		return "category-invalid"
	}
}

// Rule pairs a gitignore-syntax pattern (relative to the kind's root; the
// subset gitignore.go documents) with the Category it declares.
type Rule struct {
	Pattern  string
	Category Category
}

// RootDecl is an absolute root a kind declares independent of any marker,
// with the category of everything under it.
type RootDecl struct {
	Root     string
	Category Category
}

// Kind is one workspace declaration. Exactly one of Markers / Home / Temp
// identifies roots by structure; Roots adds absolute roots on top.
type Kind struct {
	// Name is the kind's registry key and appears in reasons.
	Name string
	// Markers: any of these entries (file or directory) present directly
	// under a candidate directory makes it a root of this kind.
	Markers []string
	// Home: the root is $HOME itself.
	Home bool
	// Temp: the roots are temproot.Roots.
	Temp bool
	// Rules classify a root-relative path; the LAST matching rule wins,
	// like gitignore. Ignored when Classify is set.
	Rules []Rule
	// Classify, when set, replaces Rules: it receives the root, the
	// root-relative slash path, the absolute path, and whether abs is a
	// directory.
	Classify func(root, rel, abs string, isDir bool) Category
	// Roots declares absolute roots (environment-derived) with a category.
	Roots func() []RootDecl
	// Commands names command schemas whose effects act on this kind's
	// classified paths (informational; see the package design note).
	Commands []string
	// Secrecy is this kind's opinion on whether a root-relative path is
	// NON-SECRET (tc-lc8f item 3z; see deletable.go's "# NON-SECRET
	// declarations" doc comment for the two operator rulings and why this
	// facet exists here, in the project specification, rather than in a
	// secret-detection policy). nil (the default for every kind but git in
	// this slice) means the kind expresses no opinion on secrecy at all.
	//
	// A non-nil Secrecy is asked the SAME (root, rel, abs, isDir) shape
	// Classify is, and there is only ONE opinion it may express: ok=true
	// means "declared non-secret", with reason naming why. ok=false means
	// NO OPINION for this specific path — e.g. git's own declaration when
	// the path is untracked — never "this path IS secret"; a kind has no
	// way to assert secrecy through this facet, only to vouch against it.
	// NonSecretWith's walk therefore has no Protected-style override to
	// check for first (unlike Resolve/Category): it simply returns the
	// FIRST ok=true opinion found, innermost candidate first, and treats
	// every ok=false or nil-Secrecy candidate as silent — continuing
	// outward exactly as Resolve treats CatSilent.
	Secrecy func(root, rel, abs string, isDir bool) (ok bool, reason string)
	// Verbs is this kind's WORKSPACE VERB-DISCOVERY facet (tc-8og1 item 3,
	// the build-tool family design; see verbs.go's package-level doc
	// comment for the full ruling and rationale). nil (the default) means
	// the kind discovers no verbs at all. See verbs.go's Kind.Verbs doc
	// comment for the calling contract.
	Verbs func(root string) []string
}

// DefaultKinds returns the built-in declarations in registry order: temp,
// home, git, go, gradle, pn, just, npm, devbox. Order matters only as the
// tie-break between two candidates with the SAME root depth.
func DefaultKinds() []Kind {
	return []Kind{tempKind, homeKind, gitKind, goKind, gradleKind, pnKind, justKind, npmKind, devboxKind}
}

// tempKind: everything under a temp root (temproot.Roots: $TMPDIR,
// /private/var/folders, /private/tmp, /tmp, CETA_EXTRA_TEMP_ROOTS) is
// disposable. Outermost by nature; any inner kind holds paths against it.
var tempKind = Kind{
	Name: "temp",
	Temp: true,
	Rules: []Rule{
		{"**", CatDeletable},
	},
}

// homeKind: $HOME. ~/.cache (and $XDG_CACHE_HOME when it points elsewhere)
// is disposable by the XDG contract; ~/.ssh and ~/.gnupg are credential
// stores, protected here declaratively as well as by secretpath (which the
// DeleteAccess policy checks first — this entry is the declaration, not the
// enforcement).
var homeKind = Kind{
	Name: "home",
	Home: true,
	Rules: []Rule{
		{".cache/", CatDeletable},
		{".ssh/", CatProtected},
		{".gnupg/", CatProtected},
	},
	Roots: func() []RootDecl {
		if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
			if r := patheval.ResolveRealPath(x); r != "" {
				return []RootDecl{{r, CatDeletable}}
			}
		}
		return nil
	},
}

// gitKind: a working tree (a `.git` directory OR file — a worktree checkout
// has a file, and counts). `.git` itself, and this workspace's `.worktrees`
// convention (git worktrees whose removal by rm orphans git's admin
// entries; integrate-branch removes them properly), are declared Protected
// here. Every other path is judged by the repository's ignore rules:
// ignored -> Deletable, else Keep — never Silent, so the tree's content is
// held against an outer temp root.
//
// SUPERSEDED for one specific path shape (tc-lc8f item 4a; operator ruling,
// Phillip, 2026-09-07, verbatim on tc-vn5z: "removing a worktree is fine,
// assuming it osnt dirty..."): a worktree ROOT ITSELF — a direct child of
// `.worktrees` (this Classify's `.worktrees/`-prefix match still fires for
// it as written below) — is intercepted BEFORE effectpolicy's DeleteAccess
// policy ever calls Classify, and judged by deletable.AtWorktreeRoot /
// ProbeWorktreeState instead (internal/deletable/worktree.go). The
// Protected declaration below therefore still governs `.worktrees` itself
// and any path NESTED inside a worktree (not its root) — only the root
// entry's OWN Protected opinion is bypassed, at the policy layer, not here.
var gitKind = Kind{
	Name:    "git",
	Markers: []string{".git"},
	Classify: func(root, rel, abs string, isDir bool) Category {
		if rel == ".git" || strings.HasPrefix(rel, ".git/") || rel == ".worktrees" || strings.HasPrefix(rel, ".worktrees/") {
			return CatProtected
		}
		if Ignored(root, abs) {
			return CatDeletable
		}
		return CatKeep
	},
	Commands: []string{"git clean"},
	// Secrecy (tc-lc8f item 3z; deletable.go's "# NON-SECRET declarations"
	// doc comment carries the two operator rulings behind this): "tracked in
	// the index and not gitignored" is non-secret — secrets are never
	// committed, so an ignored file (`.env`, say) keeps ordinary secret
	// matching regardless of what this declares. Ignored(root, abs) is the
	// SAME lexical gitignore matcher gitKind's own Classify above already
	// uses; it costs nothing extra to consult here and it is what makes
	// this declaration honor "not gitignored" literally even for the rare
	// force-added (`git add -f`) tracked-but-ignored file, rather than
	// relying on git ls-files to have already excluded it (plain `git
	// ls-files`, with no `-i`/`-o`, does NOT filter by .gitignore at all —
	// it lists whatever is in the index, ignored or not).
	//
	// rel == "." (the repository root itself) is deliberately given no
	// opinion: "the repo root is non-secret" is not a meaningful
	// declaration and this facet only ever narrows a `secrets` PATH
	// COMPONENT match, which the root itself can never be.
	Secrecy: func(root, rel, abs string, isDir bool) (bool, string) {
		if rel == "." || Ignored(root, abs) {
			return false, ""
		}
		tracked, err := gitTrackedProbe(root, rel, isDir)
		if err != nil || !tracked {
			return false, ""
		}
		return true, "tracked by git and not ignored: " + rel + " (repository at " + root + ")"
	},
}

// goKind: a Go module (go.mod). The module tree itself declares nothing —
// Go writes build products to caches under $HOME, not under the module —
// so the interesting declarations are the ABSOLUTE cache roots, derived
// exactly as `go env` derives them, without exec: GOCACHE, else
// $XDG_CACHE_HOME/go-build, else $HOME/.cache/go-build ($HOME/Library/
// Caches/go-build on darwin); GOMODCACHE, else $GOPATH/pkg/mod, else
// $HOME/go/pkg/mod. Documented gap: GOMODCACHE's default sits inside
// patheval's read-only `~/go/pkg` zone, which the DeleteAccess policy
// checks BEFORE this declaration, so `rm -rf ~/go/pkg/mod` is Forbidden by
// zone regardless — the declaration here is correct data (it is a cache)
// and TestGoKindModCacheZoneConflict pins the interaction rather than hiding
// it; changing that zone is a separate decision.
var goKind = Kind{
	Name:    "go",
	Markers: []string{"go.mod"},
	Roots: func() []RootDecl {
		var decls []RootDecl
		add := func(p string) {
			if p == "" {
				return
			}
			if r := patheval.ResolveRealPath(p); r != "" {
				decls = append(decls, RootDecl{r, CatDeletable})
			}
		}
		home, _ := os.UserHomeDir()
		if c := os.Getenv("GOCACHE"); c != "" {
			add(c)
		} else if x := os.Getenv("XDG_CACHE_HOME"); x != "" && runtime.GOOS != "darwin" {
			add(filepath.Join(x, "go-build"))
		} else if home != "" {
			if runtime.GOOS == "darwin" {
				add(filepath.Join(home, "Library", "Caches", "go-build"))
			} else {
				add(filepath.Join(home, ".cache", "go-build"))
			}
		}
		if m := os.Getenv("GOMODCACHE"); m != "" {
			add(m)
		} else if gp := os.Getenv("GOPATH"); gp != "" {
			add(filepath.Join(strings.Split(gp, string(os.PathListSeparator))[0], "pkg", "mod"))
		} else if home != "" {
			add(filepath.Join(home, "go", "pkg", "mod"))
		}
		return decls
	},
	Commands: []string{"go clean"},
}

// gradleKind: a Gradle project (settings.gradle[.kts] or build.gradle[.kts]
// directly in the root). build/ and .gradle/ are the build output and the
// project-local cache, both regenerated by any build — `gradle clean`
// removes build/, and a plain `rm -rf build` is the same classification.
// Silent on everything else (the enclosing git tree decides).
var gradleKind = Kind{
	Name:    "gradle",
	Markers: []string{"settings.gradle", "settings.gradle.kts", "build.gradle", "build.gradle.kts"},
	Rules: []Rule{
		{"build/", CatDeletable},
		{".gradle/", CatDeletable},
	},
	Commands: []string{"gradle clean", "gradlew clean"},
}

// pnKind: a pn workspace (pn-workspace.toml). Its coordinated workforest
// sets live under workforests_dir (default `.workforests`, the
// `[workspace]` key of the same name) and are git worktrees of every repo:
// removing one with rm orphans each repo's worktree admin entry, so the
// directory is Protected — `pn workspace workforest remove` is the tool
// (pn-workspace-rules' "How a set is laid out"). Silent on everything else.
//
// SUPERSEDED for a direct child of workforests_dir (a coordinated set's own
// root, e.g. `.workforests/<set-name>`) by the same tc-lc8f item 4a ruling
// gitKind's doc comment quotes: effectpolicy's DeleteAccess policy
// intercepts that exact path (deletable.IsDeclaredWorktreeSlot) before ever
// calling Classify, and judges it by ProbeWorktreeState instead. A path
// NESTED inside a set (a per-repo worktree admin entry two or more levels
// down) is unaffected and stays governed by the Protected declaration below.
var pnKind = Kind{
	Name:    "pn",
	Markers: []string{"pn-workspace.toml"},
	Classify: func(root, rel, abs string, isDir bool) Category {
		dir := pnWorkforestsDir(root)
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return CatProtected
		}
		return CatSilent
	},
	Commands: []string{"pn workspace workforest remove"},
}

// pnWorkforestsDir reads `workforests_dir = '...'` from the root's
// pn-workspace.toml (a single line scan — the value is a bare relative
// directory name; no TOML parser is needed for one scalar), defaulting to
// `.workforests`.
func pnWorkforestsDir(root string) string {
	f, err := os.Open(filepath.Join(root, "pn-workspace.toml"))
	if err != nil {
		return ".workforests"
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "workforests_dir") {
			continue
		}
		if _, v, ok := strings.Cut(line, "="); ok {
			v = strings.TrimSpace(v)
			v = strings.Trim(v, `'"`)
			v = strings.Trim(filepath.ToSlash(filepath.Clean(v)), "/")
			if v != "" && v != "." {
				return v
			}
		}
	}
	return ".workforests"
}

// candidate is one (root, kind) pair a path lies under.
type candidate struct {
	root string
	kind Kind
	// order is the kind's registry position, the tie-break between equal
	// root depths.
	order int
}

// Resolution is Resolve's answer: the deciding category and, when it is not
// Silent, the kind and root that decided.
type Resolution struct {
	Category Category
	Kind     string
	Root     string
}

// Resolve classifies abs — an absolute, symlink-resolved path — against
// kinds per the algorithm in this file's header.
func Resolve(kinds []Kind, abs string) Resolution {
	abs = filepath.Clean(abs)
	cands, isDir := sortedCandidates(kinds, abs)
	if len(cands) == 0 {
		return Resolution{Category: CatSilent}
	}
	var first *Resolution
	for _, c := range cands {
		rel := relTo(abs, c.root)
		cat := c.kind.categorize(c.root, rel, abs, isDir)
		if cat == CatProtected {
			return Resolution{Category: CatProtected, Kind: c.kind.Name, Root: c.root}
		}
		if cat != CatSilent && first == nil {
			first = &Resolution{Category: cat, Kind: c.kind.Name, Root: c.root}
		}
	}
	if first != nil {
		return *first
	}
	return Resolution{Category: CatSilent}
}

// NonSecretWith walks abs's candidates in the SAME innermost-first order as
// Resolve (see sortedCandidates), asking each candidate's Secrecy func
// (nil-safe) for an opinion, and returns the FIRST non-secret opinion
// found. See Kind.Secrecy's doc comment for why there is no Protected-style
// override to check for first: NON-SECRET is the only opinion any kind
// expresses through this facet, so a candidate with no opinion (Secrecy
// nil, or Secrecy answering ok=false for this path) is silently skipped and
// the walk continues outward exactly as Resolve treats CatSilent.
//
// deletable.go's NonSecret/NonSecretWithKinds are the entry points most
// callers use (they also resolve a cwd-relative/`~`-expanded path string
// via a *patheval.PathEvaluator first); this is the kinds-only core, kept
// in this file beside Resolve because it operates purely on Kind
// declarations over an already-resolved abs path, exactly like Resolve
// does.
func NonSecretWith(kinds []Kind, abs string) (bool, string) {
	abs = filepath.Clean(abs)
	cands, isDir := sortedCandidates(kinds, abs)
	for _, c := range cands {
		if c.kind.Secrecy == nil {
			continue
		}
		rel := relTo(abs, c.root)
		if ok, reason := c.kind.Secrecy(c.root, rel, abs, isDir); ok {
			return true, reason
		}
	}
	return false, ""
}

// sortedCandidates collects abs's (root, kind) candidates and sorts them
// deepest-root-first (registry order as the tie-break between equal
// depths) — the shared ordering Resolve and NonSecretWith both walk — and
// reports whether abs itself is a directory, computed once for both
// callers' categorize/Secrecy calls.
func sortedCandidates(kinds []Kind, abs string) (cands []candidate, isDir bool) {
	cands = candidates(kinds, abs)
	sort.SliceStable(cands, func(i, j int) bool {
		if len(cands[i].root) != len(cands[j].root) {
			return len(cands[i].root) > len(cands[j].root)
		}
		return cands[i].order < cands[j].order
	})
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		isDir = true
	}
	return cands, isDir
}

// relTo returns abs's slash-separated path relative to root ("." when abs
// equals root), the same computation Resolve and NonSecretWith each need
// for every candidate they visit.
func relTo(abs, root string) string {
	if abs == root {
		return "."
	}
	return filepath.ToSlash(strings.TrimPrefix(abs, root+"/"))
}

// candidates lists every (root, kind) abs lies under.
func candidates(kinds []Kind, abs string) []candidate {
	var out []candidate
	home, _ := os.UserHomeDir()
	home = patheval.ResolveRealPath(home)
	for i, k := range kinds {
		switch {
		case k.Temp:
			for _, r := range temproot.Roots() {
				if patheval.PathContains(r, abs) {
					out = append(out, candidate{r, k, i})
				}
			}
		case k.Home:
			if home != "" && patheval.PathContains(home, abs) {
				out = append(out, candidate{home, k, i})
			}
		case len(k.Markers) > 0:
			for dir := abs; ; dir = filepath.Dir(dir) {
				if hasMarker(dir, k.Markers) {
					out = append(out, candidate{dir, k, i})
				}
				if filepath.Dir(dir) == dir {
					break
				}
			}
		}
		if k.Roots != nil {
			for _, d := range k.Roots() {
				if d.Root != "" && patheval.PathContains(d.Root, abs) {
					out = append(out, candidate{d.Root, rootKind(k, d.Category), i})
				}
			}
		}
	}
	return out
}

// rootKind derives a kind that classifies a declared absolute root AND
// everything under it as its category (unlike a Rules-driven kind, whose
// root itself is Silent — `rm -rf /tmp` is not what a temp declaration
// vouches for, but `rm -rf "$GOCACHE"` is exactly what a cache declaration
// vouches for), keeping the declaring kind's name.
func rootKind(k Kind, cat Category) Kind {
	return Kind{Name: k.Name, Classify: func(string, string, string, bool) Category { return cat }}
}

// InsideMarkerWorkspace reports whether abs lies at or under an ancestor
// directory holding one of the NAMED kinds' Markers — a MARKER-PRESENCE
// question, deliberately independent of what that kind's Classify/Rules
// would say about the path (Resolve/Classify answer a DIFFERENT question,
// deletability, and goKind in particular is Silent over its own module tree:
// its Rules are empty and its Classify is unset, so categorize's rel=="."
// fast path and its empty Rules loop both return CatSilent for every path
// under a go.mod root — Resolve would never surface "go" as the deciding
// kind for an ordinary source file). This is the effectpolicy package's
// TrustedCheckoutExec policy's "am I inside a recognised git/go checkout"
// check (slice 3x, tc-lc8f item 4e) — a plain existence test over the same
// Markers data every Kind already declares, reusing hasMarker rather than
// re-walking ancestors with a second implementation.
func InsideMarkerWorkspace(kinds []Kind, names []string, abs string) bool {
	abs = filepath.Clean(abs)
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	for _, k := range kinds {
		if len(k.Markers) == 0 || !want[k.Name] {
			continue
		}
		for dir := abs; ; dir = filepath.Dir(dir) {
			if hasMarker(dir, k.Markers) {
				return true
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return false
}

// hasMarker reports whether any marker exists directly under dir.
func hasMarker(dir string, markers []string) bool {
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// categorize applies the kind's Classify func or its Rules (last match
// wins, gitignore semantics via ignoreRule) to one root-relative path.
func (k Kind) categorize(root, rel, abs string, isDir bool) Category {
	if k.Classify != nil {
		return k.Classify(root, rel, abs, isDir)
	}
	if rel == "." {
		return CatSilent
	}
	cat := CatSilent
	for _, r := range k.Rules {
		rule, ok := parseIgnoreLine(r.Pattern, "")
		if !ok {
			continue
		}
		if rule.matches(rel, isDir) || ancestorMatches(rule, rel) {
			cat = r.Category
		}
	}
	return cat
}

// ancestorMatches reports whether rule matches any proper ancestor
// directory of rel — a `build/` rule covers build/out too, exactly as a
// gitignore directory pattern covers everything inside.
func ancestorMatches(rule ignoreRule, rel string) bool {
	comps := strings.Split(rel, "/")
	for i := 1; i < len(comps); i++ {
		if rule.matches(strings.Join(comps[:i], "/"), true) {
			return true
		}
	}
	return false
}
