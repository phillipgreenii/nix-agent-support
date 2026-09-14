package pathspec

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// ADR 0068 P2 (tc-mkpaz.2, docs/adr/0068-ceta-unified-path-access-
// resolution.md, "Spec sources" — OS spec bullet) ports
// internal/patheval/evaluator.go's classify() zone ladder into this package
// as a SECOND, pathspec-local encoding of the same ambient,
// project-independent zone facts — a deliberate, accepted duplication (see
// the ADR's opening scope note): patheval.classify()/Evaluate() are shared,
// production-facing primitives, consulted directly by eleven
// internal/rules/* packages, internal/engine, and internal/setup.RuleChain,
// and are NOT modified by this file — only read, as the source of truth for
// every zone below.
//
// # Ten zones, not nine
//
// The design text's own "Spec sources" bullet names contained-claude/ in
// its prose list of xdgDataHome subpaths, but then miscounts the running
// total as "nine zones in classify() total". The prose list it just gave
// (the ten items below) is actually TEN. This file follows the PROSE LIST
// — which does name contained-claude/ — not the miscounted total:
//
//  1. /tmp (osTmpKind) — patheval.classify() treats all of /tmp as full
//     ReadWrite. workspace.go's tempKind already states /tmp's Delete
//     facet (Delete: Permitted); this file adds the Read/Write opinion
//     alongside it, on a SEPARATE Kind, so a caller wanting the full
//     picture passes both tempKind and osTmpKind.
//  2. /nix (osNixKind) — Read: Permitted, Write: Forbidden, Delete:
//     Forbidden: "immutable path owned by nix-daemon, so only read is
//     allowed" [design: "Spec sources" — OS spec bullet, verbatim].
//  3. ~/.claude (osHomeKind) — ReadOnly, except its existing
//     plans/projects read-write carve-out (mirrors classify()'s own
//     claudePlans/claudeProjects special-case).
//  4. ~/.claude.json (osHomeKind) — ReadOnly.
//  5. The Gradle user cache, GRADLE_USER_HOME or ~/.gradle
//     (osGradleCacheKind) — ReadOnly.
//  6. ~/go/pkg (osHomeKind) — ReadOnly, with its Delete facet left
//     deliberately Unknown; see "The ~/go/pkg exception" below.
//  7-10. The four <xdgDataHome> subpaths (osXDGDataKind):
//     nix-support-local-plugins/ (ReadOnly), contained-claude/ (ReadOnly —
//     omitted from an earlier revision of this packet; confirmed present
//     in patheval/evaluator.go's classify(), lines ~414-416),
//     claude-extended-tool-approver/ itself (ReadWrite — the tool's own
//     asks.db), and the old-name claude-pretool-hook/ (ReadOnly) [design:
//     "Spec sources" — OS spec bullet, verbatim list].
//
// # The ~/go/pkg exception (binding decision)
//
// ~/go/pkg's Delete facet MUST stay Unknown, NEVER Forbidden — the one
// deliberate, NAMED exception to "complete every Kind's opinion":
// "if ~/go/pkg's Delete facet were ever 'completed' to Forbidden ..., that
// OUTER Forbidden would win outright over GOMODCACHE's INNER Delete:
// Permitted, and the exact bug this ADR exists to fix would be silently
// reintroduced" [design: "The fold: one algorithm, run once per facet" —
// the full ~/go/pkg exception paragraph, cited verbatim]. workspace.go's
// updateFacet makes a Forbidden opinion win outright regardless of depth,
// so setting Delete: Forbidden here would override goKind's deeper
// GOMODCACHE RootDecl (Delete: Permitted) even though GOMODCACHE sits
// INSIDE ~/go/pkg. Leaving Delete Unknown here means goKind's deeper,
// non-Unknown opinion is free to win instead, exactly as intended.
//
// # Fixed-root mechanism: Kind.Roots, not a dedicated field
//
// The ADR left the FixedRoot-vs-Roots() choice to the implementer ("whether
// a dedicated field is structurally required or merely a readability
// preference is worth confirming at implementation time rather than
// assumed here"). This file reuses Kind.Roots — already used by
// workspace.go's homeKind/goKind for a COMPUTED absolute root — for /nix
// and /tmp too: it is perfectly capable of returning a literal constant
// with no computation at all, and adding a second, single-purpose field
// would duplicate machinery Roots already provides.
//
// # No changes to internal/patheval or workspace.go
//
// This file adds new pathspec.Kind values only. internal/patheval is
// read-only reference; workspace.go (and its DefaultKinds()) is untouched —
// wiring these Kinds into DefaultKinds() and the policy layer is "P5:
// policy-layer collapse + fabricated-root wiring", not this packet.
// Exercise these Kinds through the explicit-kind-set entry points
// (ResolveAccess/ClassifyWith), passing []Kind{...} directly — no
// DefaultKinds() wiring exists yet at this packet's boundary.

// osTmpKind: /tmp is fully read-write per patheval.classify() (the tmpRoot
// check, evaluated before any narrower zone). See "Ten zones" item 1 above
// for why this is a Read/Write-only Kind, deliberately separate from
// workspace.go's tempKind (which already states /tmp's Delete facet).
var osTmpKind = Kind{
	Name: "os-tmp",
	Roots: func() []RootDecl {
		root := patheval.ResolveRealPath("/tmp")
		if root == "" {
			root = "/tmp"
		}
		return []RootDecl{{Root: root, Read: Permitted, Write: Permitted}}
	},
}

// osNixKind: /nix is the immutable Nix store, owned by nix-daemon. See
// "Ten zones" item 2 above.
var osNixKind = Kind{
	Name: "os-nix",
	Roots: func() []RootDecl {
		root := patheval.ResolveRealPath("/nix")
		if root == "" {
			root = "/nix"
		}
		return []RootDecl{{Root: root, Read: Permitted, Write: Forbidden, Delete: Forbidden}}
	},
}

// osHomeKind covers the three $HOME-relative zones that need PATH-VARYING
// (rel-based) logic rather than a single opinion for the whole root — the
// plans/projects carve-out under ~/.claude, the separate ~/.claude.json
// file, and the ~/go/pkg blanket convention (see "Ten zones" items 3, 4,
// and 6, and "The ~/go/pkg exception" above). Home: true anchors the
// candidate at $HOME (the same resolved root workspace.go's homeKind
// shares via candidates()' shared home computation); Classify then
// branches on the root-relative path, mirroring classify()'s own
// sequential checks.
var osHomeKind = Kind{
	Name: "os-home",
	Home: true,
	Classify: func(root, rel, abs string, isDir bool) (read, write, del AccessResult) {
		switch {
		case rel == ".claude" || strings.HasPrefix(rel, ".claude/"):
			if rel == ".claude/plans" || strings.HasPrefix(rel, ".claude/plans/") ||
				rel == ".claude/projects" || strings.HasPrefix(rel, ".claude/projects/") {
				return Permitted, Permitted, Unknown
			}
			return Permitted, Forbidden, Unknown
		case rel == ".claude.json":
			return Permitted, Forbidden, Unknown
		case rel == "go/pkg" || strings.HasPrefix(rel, "go/pkg/"):
			// Delete deliberately left Unknown — see "The ~/go/pkg
			// exception" above. Do NOT add Delete: Forbidden here.
			return Permitted, Forbidden, Unknown
		default:
			return Unknown, Unknown, Unknown
		}
	},
}

// osGradleCacheKind: the Gradle user cache, GRADLE_USER_HOME or ~/.gradle
// (patheval's own gradleHome derivation, mirrored here without exec). See
// "Ten zones" item 5 above. Distinct from workspace.go's gradleKind (a
// per-PROJECT settings.gradle/build.gradle marker Kind) — this is the
// global, ambient cache directory, not a project's build/.gradle output.
var osGradleCacheKind = Kind{
	Name: "os-gradle-cache",
	Roots: func() []RootDecl {
		var base string
		if g := os.Getenv("GRADLE_USER_HOME"); g != "" {
			base = g
		} else if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".gradle")
		}
		if base == "" {
			return nil
		}
		root := patheval.ResolveRealPath(base)
		if root == "" {
			return nil
		}
		return []RootDecl{{Root: root, Read: Permitted, Write: Forbidden}}
	},
}

// osXDGDataKind covers the four <xdgDataHome> subpaths patheval.classify()
// declares (XDG_DATA_HOME, or ~/.local/share when unset — the same
// derivation patheval.New/NewWithCWD use, mirrored here without exec). See
// "Ten zones" items 7-10 above.
var osXDGDataKind = Kind{
	Name: "os-xdg-data",
	Roots: func() []RootDecl {
		var base string
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			base = x
		} else if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".local", "share")
		}
		if base == "" {
			return nil
		}
		xdg := patheval.ResolveRealPath(base)
		if xdg == "" {
			return nil
		}
		add := func(sub string, read, write AccessResult) RootDecl {
			root := patheval.ResolveRealPath(filepath.Join(xdg, sub))
			if root == "" {
				root = filepath.Join(xdg, sub)
			}
			return RootDecl{Root: root, Read: read, Write: write}
		}
		return []RootDecl{
			// nix-support-local-plugins/: ReadOnly.
			add("nix-support-local-plugins", Permitted, Forbidden),
			// contained-claude/: ReadOnly (the correction this file's
			// header documents — confirmed present in classify(), lines
			// ~414-416).
			add("contained-claude", Permitted, Forbidden),
			// claude-extended-tool-approver/: ReadWrite — the tool's own
			// asks.db.
			add("claude-extended-tool-approver", Permitted, Permitted),
			// claude-pretool-hook/ (old name): ReadOnly.
			add("claude-pretool-hook", Permitted, Forbidden),
		}
	},
}

// OSKinds returns the OS-spec Kinds declared in this file, in no
// significant order (each occupies a disjoint root; ordering only matters
// as the tie-break between two candidates at the SAME depth, which these
// Kinds never share with each other). Pass it (optionally alongside
// workspace.go's DefaultKinds()/tempKind, e.g. for /tmp's full
// Read/Write/Delete picture) to ResolveAccess/ClassifyWith's explicit
// []Kind parameter — wiring it into DefaultKinds() itself is P5's job, not
// this packet's (see this file's package-level doc comment).
func OSKinds() []Kind {
	return []Kind{osTmpKind, osNixKind, osHomeKind, osGradleCacheKind, osXDGDataKind}
}
