package pathspec

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/userconfig"
)

// P5's remaining REJECT targets + the R7/D12 protected-path REJECT/ABSTAIN
// split (Phase 2h(1), docket tc-o14i5.3, semantic-post-check round-1/2/3
// findings — see this packet's own text for the full provenance). Kept as
// their own Kinds (claudeConfigGitKind/claudeConfigHomeKind below), rather
// than folded into gitKind/homeKind's existing Classify/Rules
// (workspace.go), so THOSE Kinds' Ground-Truth line citations stay
// untouched — this file only ADDS Write-facet opinions at the same two
// roots (a git working tree's root, and $HOME) R7/D12 names.
//
// Five targets, all Write-facet only:
//
//  1. R7/D12 enumeration: REJECT set (.claude/settings.json,
//     .claude/settings.local.json, .claude/hooks/**, .mcp.json, and their
//     home equivalents ~/.claude/settings*.json, ~/.claude/hooks/**,
//     ~/.claude.json) and ABSTAIN set (CLAUDE.md, CLAUDE.local.md,
//     .claude/agents|commands|skills|rules/**, and their home
//     equivalents) — claudeProtectedWrite.
//  2. asks.db* — see osspec.go's osAsksDBKind instead: it needs an
//     env-derived xdgDataHome root (the same computation osXDGDataKind
//     already performs), which the Markers/Home-only Kind shape this file
//     uses cannot express as a path-varying Classify (Roots-declared
//     candidates are flat, see workspace.go's rootKind).
//  3. P18's agentWritableConfig allowlist over both P17 config layers
//     (a repo's `.ceta/`, and the two user-level directories
//     specfmt.DefaultUserDir()/userconfig.DefaultPath() resolve) —
//     cetaProtectedConfigWrite/isCetaConfigFile/agentWritableConfigAllows.
//  4. The hook-router config (router-config.json) — routerConfigBasenameWrite,
//     folded into claudeProtectedWrite.
//  5. ~/.claude/plugins/** — folded directly into claudeConfigHomeKind's
//     Classify (home-scoped only; there is no repo-level equivalent).
//
// ABSTAIN uses holdKeep (workspace.go's unexported "stop the outward search,
// export as Unknown" state — see gitKind's own Classify doc comment for the
// general mechanism), not a bare Unknown: an ABSTAIN-set path must not let
// an outer zone's ordinary CanWrite() default still approve it. REJECT uses
// Forbidden, which workspace.go's updateFacet always lets win outright,
// regardless of depth or Kind registry order.

// claudeConfigGitKind: R7/D12 + P18, evaluated at a git working tree's root
// (the SAME Markers a candidate is found under as gitKind itself — see
// candidates(), which creates one candidate per (root, kind), independent
// per Kind, so this Kind's own Write opinion is folded into the SAME
// resolveFacets walk gitKind's Delete opinion already participates in).
var claudeConfigGitKind = Kind{
	Name:    "claude-config",
	Markers: []string{".git"},
	Classify: func(root, rel, abs string, isDir bool) (read, write, del AccessResult) {
		if rel == ".ceta" || strings.HasPrefix(rel, ".ceta/") {
			return Unknown, cetaProtectedConfigWrite(abs), Unknown
		}
		return Unknown, claudeProtectedWrite(rel), Unknown
	},
}

// claudeConfigHomeKind: R7/D12's home-equivalent set, P18's two user-level
// config directories, and ~/.claude/plugins/**, evaluated at $HOME.
var claudeConfigHomeKind = Kind{
	Name: "claude-config-home",
	Home: true,
	Classify: func(root, rel, abs string, isDir bool) (read, write, del AccessResult) {
		if rel == ".claude/plugins" || strings.HasPrefix(rel, ".claude/plugins/") {
			return Unknown, Forbidden, Unknown
		}
		if isCetaConfigFile(abs) {
			return Unknown, cetaProtectedConfigWrite(abs), Unknown
		}
		return Unknown, claudeProtectedWrite(rel), Unknown
	},
}

// claudeProtectedWrite is the shared R7/D12 write classification (plus the
// hook-router config basename check, item 4 above), evaluated against a
// root-relative path under EITHER a git working tree's root or $HOME — the
// two roots R7/D12 names. See this file's package doc comment for the
// REJECT-vs-ABSTAIN mechanism (Forbidden vs holdKeep).
//
// Matching is component-wise and case-INSENSITIVE (strings.EqualFold per
// path component) — the same fold pg2-8st32's pathContainsFold/
// isAgentConfigPath apply, for the identical reason: a case-insensitive
// volume makes ".CLAUDE/settings.json" and ".claude/settings.json" name the
// SAME real file, and under-matching would cost the entire control.
func claudeProtectedWrite(rel string) AccessResult {
	if rel == "" || rel == "." {
		return Unknown
	}
	if routerConfigBasenameWrite(rel) {
		return Forbidden
	}
	parts := strings.Split(rel, "/")
	switch {
	case foldEqual(parts[0], "CLAUDE.md"), foldEqual(parts[0], "CLAUDE.local.md"):
		if len(parts) == 1 {
			return holdKeep
		}
		return Unknown
	case foldEqual(parts[0], ".claude.json"):
		if len(parts) == 1 {
			return Forbidden
		}
		return Unknown
	case foldEqual(parts[0], ".mcp.json"):
		if len(parts) == 1 {
			return Forbidden
		}
		return Unknown
	case !foldEqual(parts[0], ".claude") || len(parts) < 2:
		return Unknown
	}
	switch {
	case len(parts) == 2 && (foldEqual(parts[1], "settings.json") || foldEqual(parts[1], "settings.local.json")):
		return Forbidden
	case foldEqual(parts[1], "hooks"):
		return Forbidden
	case len(parts) == 2 && foldEqual(parts[1], "CLAUDE.md"):
		return holdKeep
	case foldEqual(parts[1], "agents"), foldEqual(parts[1], "commands"), foldEqual(parts[1], "skills"), foldEqual(parts[1], "rules"):
		return holdKeep
	default:
		return Unknown
	}
}

// routerConfigBasenameWrite reports whether rel's basename is
// "router-config.json" (case-insensitive) — claude-hook-router's own config
// file (packages/claude-hook-router/internal/router/config.go's LoadConfig),
// resolved at runtime from ${CLAUDE_PLUGIN_ROOT}, a location this package
// cannot reliably reconstruct (CLAUDE_PLUGIN_ROOT at THIS hook's own
// runtime names ceta's own plugin root, not claude-hook-router's — the two
// are different plugins). A basename-unconditional match is the safe
// direction here: over-matching costs one unnecessary Reject on an
// unrelated same-named file, which does not exist in either fixture root
// this package classifies against; under-matching would silently miss the
// real target whenever CLAUDE_PLUGIN_ROOT resolves outside
// ~/.claude/plugins/** (which is already covered unconditionally by
// claudeConfigHomeKind's own plugins/** check, item 5).
func routerConfigBasenameWrite(rel string) bool {
	return foldEqual(filepath.Base(rel), "router-config.json")
}

func foldEqual(a, b string) bool {
	return strings.EqualFold(a, b)
}

// isCetaConfigFile reports whether abs lies under either of the two
// USER-LEVEL P17 layers: specfmt.DefaultUserDir() (the new engine's
// embedded/user spec repository) or the directory holding
// userconfig.DefaultPath() (this packet's own agentWritableConfig field's
// file, per its self-referential rule — see userconfig.Config's own doc
// comment). The REPO-level P17 layer (a project's `.ceta/`) is checked
// separately by claudeConfigGitKind's Classify, which already has the
// candidate's own root in hand and does not need to re-resolve it here.
func isCetaConfigFile(abs string) bool {
	if userDir, err := specfmt.DefaultUserDir(); err == nil && userDir != "" && patheval.PathContains(userDir, abs) {
		return true
	}
	if cfgPath, err := userconfig.DefaultPath(); err == nil && cfgPath != "" {
		if patheval.PathContains(filepath.Dir(cfgPath), abs) {
			return true
		}
	}
	return false
}

// cetaProtectedConfigWrite is P18: a ceta config file in either P17 layer
// (a repo's `.ceta/`, or either of isCetaConfigFile's two user-level
// directories) is Reject-by-default for an agent-proposed write, UNLESS a
// human-set userconfig.Config.AgentWritableConfig pattern names abs — in
// which case the verdict is Unknown (defers to ordinary path policy, per
// P18's own text: "judged by normal path policy once that file/glob is
// named in a human-set agentWritableConfig setting"). The self-referential
// rule (a write to the agentWritableConfig setting FILE ITSELF needs its
// own allowlisting) falls out of this uniformly: userconfig.DefaultPath()'s
// own directory is itself covered by isCetaConfigFile, so its default
// verdict is Forbidden exactly like any other P17-layer file, unless the
// SAME list also names it.
func cetaProtectedConfigWrite(abs string) AccessResult {
	cfg := userconfig.LoadDefault()
	if allowed, _ := agentWritableConfigAllows(cfg.AgentWritableConfig, abs); allowed {
		return Unknown
	}
	return Forbidden
}

// agentWritableConfigAllows reports whether abs matches one of patterns
// (userconfig.Config.AgentWritableConfig): each pattern is either an
// absolute path/glob, or `~/`-prefixed for a $HOME-relative pattern,
// matched with the SAME gitignore-style glob syntax gitignore.go's
// globMatch already implements (`*`, `?`, `**`) — a relative, non-`~`
// pattern names no meaningful base and is skipped (never matches
// everything by accident).
func agentWritableConfigAllows(patterns []string, abs string) (bool, string) {
	for _, p := range patterns {
		pat := p
		if strings.HasPrefix(pat, "~/") {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				pat = filepath.Join(home, pat[2:])
			}
		}
		if !filepath.IsAbs(pat) {
			continue
		}
		if globMatch(filepath.ToSlash(filepath.Clean(pat)), filepath.ToSlash(abs)) {
			return true, "allowlisted by agentWritableConfig pattern " + p
		}
	}
	return false, ""
}
