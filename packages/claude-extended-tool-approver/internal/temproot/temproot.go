// Package temproot implements pg2-yoqsr's temp-root carve-out primitives:
// the set of directory roots under which a git repository is a disposable
// TEMPORARY fixture rather than a real one, and the operand-resolution
// helpers the gitdir and git rules both need to check a git invocation's
// repo-locating operands against that set.
//
// # WHY THIS EXISTS (pg2-yoqsr)
//
// CETA's git/pathsafety guards correctly refuse (1) a direct write to git
// metadata under a `.git/` directory and (2) assigning a git repo-locating
// env var (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, GIT_COMMON_DIR,
// GIT_OBJECT_DIRECTORY) on a git invocation. Both are right for a REAL
// repository, but neither used to distinguish one from a THROWAWAY fixture
// that exists only under a temporary path — so there was no sanctioned way
// to build a disposable git fixture and drive the exact failure mode those
// guards exist to prevent (see docs/adr/0056-ceta-temp-repo-carve-out.md in
// phillipgreenii-nix-agent-support for the full rationale and the incident
// that motivated it).
//
// # THE NAIVE READING IS WRONG (operator ruling, 2026-08-27)
//
// "Allow it if a temp path appears in the command" would open the exact hole
// the guard closes:
//
//	GIT_DIR=<REAL canonical>/.git   git -C <tmpdir> config user.email t@example.com
//
// A temp path IS present (`-C <tmpdir>`), yet the write lands on the REAL
// repository, because GIT_DIR outranks `-C`. So the carve-out keys on the
// EFFECTIVE git directory a command would act on, and every operand that
// participates in resolving it — GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE/
// GIT_COMMON_DIR/GIT_OBJECT_DIRECTORY, `--git-dir`/`--work-tree`/`-C`
// operands, and cwd when nothing more specific overrides it — MUST
// INDEPENDENTLY resolve under a temporary root. If ANY of them is outside
// one, the command is refused exactly as before the carve-out: mixed
// real+temp is the attack this whole package exists to keep refusing, not a
// use case to accommodate.
package temproot

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// CanonicalRepoLocatingEnvVars are the env var NAMES whose assignment
// redirects WHICH files a git invocation reads or writes, keyed on NAME
// rather than on the SHAPE of the value assigned to them.
//
// KEYING ON NAME, NOT ON A '.git' PATH SEGMENT, IS THE SIDE-FINDING FIX
// (pg2-yoqsr comment, 2026-08-27). A subagent testing a sibling bead was
// refused `GIT_DIR=<path>/.git` but ALLOWED `GIT_DIR=<bare-repo-path>` — a
// bare repository's GIT_DIR IS its own top-level directory, so there is no
// `.git` path COMPONENT anywhere in the value for a pattern match to anchor
// on, even though it redirects git's metadata exactly as surely as the
// non-bare spelling does. A bare repo is a real repo; the false negative was
// in the GUARD, not in the input. Keying on the variable NAME closes this
// for every shape the value can take — bare or non-bare, resolvable or not
// yet existing (R6) — because the check that follows (Under) resolves and
// compares the EFFECTIVE directory, never the literal text.
//
// GIT_PREFIX IS DELIBERATELY ABSENT even though pg2-yoqsr's own Problem
// section lists it alongside these five when describing the PRE-EXISTING
// refusal. It is a relative in-tree offset git sets for its OWN subprocesses
// (hooks, aliases) to recover the cwd they were invoked from — it does not
// redirect WHERE git resolves its own metadata, so assigning it does not
// create the hazard this package guards against. pg2-yoqsr's acceptance
// criteria (R2) and Verification section both omit it from the operand list
// that must independently resolve under a temp root; that enumeration is
// authoritative and this map matches it exactly.
var CanonicalRepoLocatingEnvVars = map[string]bool{
	"GIT_DIR":              true,
	"GIT_WORK_TREE":        true,
	"GIT_INDEX_FILE":       true,
	"GIT_COMMON_DIR":       true,
	"GIT_OBJECT_DIRECTORY": true,
}

// extraRootsEnvVar names the escape hatch for a temp-fixture root this
// package's built-in list does not already cover (a differently-configured
// session scratchpad, a CI sandbox's own temp mount, ...), mirroring the
// established CETA_EXTRA_READWRITE_ROOTS/CETA_EXTRA_READONLY_ROOTS pattern
// (internal/patheval) rather than inventing a new configuration shape.
const extraRootsEnvVar = "CETA_EXTRA_TEMP_ROOTS"

// Roots returns the realpath-resolved directories under which a filesystem
// path is treated as TEMPORARY/disposable rather than a durable, real
// location (pg2-yoqsr R3/R4).
//
// R4 names four "at minimum" landing zones — the realpath of $TMPDIR,
// /private/var/folders, /private/tmp, and the Claude Code session
// scratchpad root — because those are where `mktemp -d`, Go's
// `t.TempDir()`, and bats' $BATS_TEST_TMPDIR actually land on this machine.
// /tmp is included too (its own realpath on macOS IS /private/tmp, but the
// two are listed as independent literals rather than one assumed alias so
// the set stays correct on a platform where they differ). The Claude Code
// session scratchpad root is NOT listed as a fifth literal: this package has
// no reliable, non-guessed way to derive it (no env var carries it to a hook
// subprocess as of 2026-08-28, and constructing it from a UID/project-hash
// pattern would be exactly the "hardcoded guesswork" R4 forbids) — and on
// this deployment it is always a descendant of /private/tmp (observed
// pattern `/private/tmp/claude-<uid>/<project>/<session>/scratchpad`), so it
// is already covered by that root without a separately-derived one. If a
// deployment ever puts it somewhere /private/tmp does not reach,
// CETA_EXTRA_TEMP_ROOTS is the sanctioned way to add it explicitly rather
// than teaching this package to guess.
func Roots() []string {
	var roots []string
	seen := make(map[string]bool)
	add := func(p string) {
		if p == "" {
			return
		}
		r := patheval.ResolveRealPath(p)
		if r == "" || seen[r] {
			return
		}
		seen[r] = true
		roots = append(roots, r)
	}
	add(os.Getenv("TMPDIR"))
	add("/private/var/folders")
	add("/private/tmp")
	add("/tmp")
	if extra := os.Getenv(extraRootsEnvVar); extra != "" {
		for _, p := range strings.Split(extra, ":") {
			add(p)
		}
	}
	return roots
}

// Under reports whether path — absolute, and possibly not yet existing on
// disk (R6: the first step of building a fixture is `git init` into an empty
// directory) — resolves under one of Roots(), compared after symlink
// resolution and matched as a DIRECTORY-BOUNDARY PREFIX, never a substring
// (R3): `/tmp` is a symlink to `/private/tmp` on this machine, and a
// substring test would wrongly admit `/Users/phillipg/my-tmp-repo` or
// `/Users/phillipg/phillipg_mbp/tmp/...`. An empty or unresolvable path is
// fail-safe: NOT under a temp root.
func Under(path string) bool {
	if path == "" {
		return false
	}
	resolved := patheval.ResolveRealPath(path)
	if resolved == "" {
		return false
	}
	for _, root := range Roots() {
		if patheval.PathContains(root, resolved) {
			return true
		}
	}
	return false
}

// ResolveOperand resolves value — a `-C`/`--git-dir`/`--work-tree` operand or
// a GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE/GIT_COMMON_DIR/GIT_OBJECT_DIRECTORY
// env value, possibly relative or `~`-prefixed, possibly not yet existing on
// disk — to an absolute, realpath-resolved form comparable against
// Roots()/Under, exactly as git itself would locate it relative to cwd. A
// leading `~`/`~/...` is expanded against the process's own HOME first
// (mirroring internal/rules/git's expandTilde), then a still-relative value
// is joined onto cwd, then symlinks are resolved with the same
// not-yet-existing-directory fallback Under uses.
//
// A RELATIVE value with a cwd that is itself not absolute (empty, in
// practice — a hand-built test HookInput that leaves CWD unset) resolves to
// "" rather than silently falling back to filepath.Join's own behaviour for
// an empty base, which is this PROCESS's OS working directory. That fallback
// would be actively dangerous here: it makes the carve-out's verdict depend
// on where the CETA BINARY happens to be running (a repo checkout normally,
// but a `nix build` sandbox or CI runner may checkout under /tmp) rather
// than on the command's own cwd, and Under("") is already false, so this
// keeps an ambiguous relative operand on the fail-safe (refused) side
// exactly like an unresolvable one.
//
// value is UNQUOTED before anything else — one wrapping pair of `"..."` or
// `'...'`, mirroring internal/rules/gitdir's own unquoteOperand. This is NOT
// optional: cmdparse already unquotes ARG tokens (a `-C`/`--git-dir`/
// `--work-tree` operand arrives clean), but it deliberately keeps an
// EnvAssignment's Value verbatim — `GIT_DIR="/real/canonical/.git" git ...`
// arrives with the quotes still attached — so a caller passing an env
// value straight through, unquoted, sees a string that does not start with
// `/` at all. Measured regression this fix closes: WITHOUT it,
// `filepath.IsAbs` reports false for `"/real/canonical/.git"` (it starts
// with `"`, not `/`), so the value gets JOINED onto cwd instead of used
// as-is; realpath's not-yet-existing fallback then walks up to the nearest
// REAL ancestor of that garbled join — which, when cwd is itself a temp
// root, IS a temp root — and the R2 regression case (a REAL, non-temp
// GIT_DIR mixed with a temp `-C`) was wrongly RELAXED as a result. Stripping
// the quotes first makes the value's own absoluteness the one that governs,
// exactly as it does for an already-clean arg operand.
func ResolveOperand(cwd, value string) string {
	value = unquoteOperand(value)
	value = ExpandTilde(value)
	if !filepath.IsAbs(value) {
		if !filepath.IsAbs(cwd) {
			return ""
		}
		value = filepath.Join(cwd, value)
	}
	return patheval.ResolveRealPath(value)
}

// unquoteOperand strips ONE pair of wrapping quotes, mirroring
// internal/rules/gitdir's own unquoteOperand exactly (duplicated rather than
// exported from there to avoid a rule package depending on ANOTHER rule
// package for a one-line primitive both need for unrelated reasons).
func unquoteOperand(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// ExpandTilde expands a leading `~` or `~/` to the user's home directory.
// Exported so both this package's own ResolveOperand/EffectiveDir and a rule
// package's pre-existing, independent copy (internal/rules/git's
// expandTilde) can eventually share one implementation; non-tilde paths are
// returned unchanged.
func ExpandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// EffectiveDir folds a `-C` chdir chain onto cwd the way git does: an
// absolute chdir resets the running directory, a relative one is joined onto
// it. This MIRRORS internal/rules/git's own effectiveDir and
// internal/rules/primarycommit's same-named helper — both already
// independently duplicate this exact fold from each other (see
// internal/rules/git's doc comment on its own copy) — as a THIRD copy,
// because pg2-yoqsr's temp-root participant scan needs it from the gitdir
// rule package too, which cannot import the git rule package's unexported
// helper without an import cycle. Consolidating all three is a larger,
// separate refactor this bead does not attempt.
func EffectiveDir(cwd string, chdirs []string) string {
	dir := cwd
	for _, c := range chdirs {
		c = ExpandTilde(c)
		if filepath.IsAbs(c) {
			dir = c
		} else {
			dir = filepath.Join(dir, c)
		}
	}
	return dir
}
