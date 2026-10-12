package pathspec

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// P5 .git/ PROTECTION (docket tc-o14i5.3, packet tc-o14i5.3.9; operator
// design P5/R10/R11 and ADR 0059): ANY write into anything inside the
// RESOLVED git dir or common dir (in a linked worktree `.git` is a FILE
// whose `gitdir:` line names an admin directory inside the main clone, and
// the common dir lives there too), or into the core.hooksPath target, by a
// NON-git-verb effect (redirect, cp, mv, tee, sed -i, Write/Edit, ...) is
// REJECTED - EXCEPT a temp repo (its effective git dir under a temp root,
// ADR 0059), which has full read/write access including its `.git/` (R11).
//
// This corrects workspace.go's gitKind, whose `.git/**` opinion is
// Delete-only and leaves Write Unknown (so the write falls back to the
// ordinary project zone and is approved). It is a function rather than a
// Kind because the verdict depends on WHO writes: git's own verbs declare
// their `.git` writes as IMPLICIT effects and are judged by the git spec,
// so the policy layer (effectpolicy.judgeWrite) skips this check for them.
//
// Membership is tested against the RESOLVED path: callers MUST pass the
// result of ResolveComponentwise (symlink.go), so a symlink inside the
// working tree that points at `.git` - or a `link/../.git` hop - cannot
// bypass the check by presenting a literal path outside the git dir.

// GitDirWriteVerdict reports whether a non-git-verb write to abs (absolute
// and symlink-resolved) lands inside a protected git location. Forbidden:
// inside a git dir, common dir or hooksPath target of a non-temp repo.
// Permitted: inside such a location of a TEMP repo (ADR 0059; the git dir
// and common dir itself under a temp root). Unknown: abs is not inside any
// git-controlled location (the ordinary path policy decides). reason names
// what decided.
func GitDirWriteVerdict(abs string) (AccessResult, string) {
	abs = filepath.Clean(abs)
	if !filepath.IsAbs(abs) {
		return Unknown, ""
	}
	// Every ancestor (and abs itself) may be a working tree root (it has a
	// `.git` entry) or a bare/separate git dir. Check all: the innermost
	// match is not the only one, a path can sit in a nested repo's tree AND
	// inside an outer repo's git dir.
	result, reason := Unknown, ""
	for dir := abs; ; dir = filepath.Dir(dir) {
		for _, loc := range protectedLocations(dir) {
			if !patheval.PathContains(loc.path, abs) {
				continue
			}
			if isTempPath(loc.path) {
				if result == Unknown {
					result, reason = Permitted, "temp repo "+loc.what+" "+loc.path+" (ADR 0059)"
				}
				continue
			}
			return Forbidden, "write inside " + loc.what + " " + loc.path + " (P5): modify it through git commands only"
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	return result, reason
}

// gitLocation is one protected directory and what it is.
type gitLocation struct {
	path string
	what string
}

// protectedLocations lists the protected git locations controlled by dir:
// as a working tree root (its `.git` directory, or the admin dir and common
// dir a `.git` file names, plus a configured core.hooksPath target), and as
// a bare/separate git dir itself (HEAD + objects/ + refs/).
func protectedLocations(dir string) []gitLocation {
	var out []gitLocation
	if looksLikeGitDir(dir) {
		out = append(out, gitLocation{real(dir), "git dir"})
		if c := commonDirOf(dir); c != "" && c != real(dir) {
			out = append(out, gitLocation{c, "common git dir"})
		}
	}
	dotGit := filepath.Join(dir, ".git")
	fi, err := os.Lstat(dotGit)
	if err != nil {
		return out
	}
	var gitDir string
	switch {
	case fi.IsDir():
		gitDir = real(dotGit)
	case fi.Mode().IsRegular():
		// A linked worktree (or --separate-git-dir): `gitdir: <path>`.
		b, err := os.ReadFile(dotGit)
		if err != nil {
			return append(out, gitLocation{dotGit, ".git file"})
		}
		line := strings.TrimSpace(string(b))
		target, ok := strings.CutPrefix(line, "gitdir:")
		if !ok {
			return append(out, gitLocation{dotGit, ".git file"})
		}
		target = strings.TrimSpace(target)
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		gitDir = real(target)
		// The `.git` file itself is part of the protected surface (rewriting
		// it redirects git to an attacker-chosen directory).
		out = append(out, gitLocation{real(dotGit), ".git file"})
	default:
		// A symlink named .git: protect the link and what it resolves to.
		out = append(out, gitLocation{dotGit, ".git entry"})
		gitDir = real(dotGit)
	}
	if gitDir == "" {
		return out
	}
	out = append(out, gitLocation{gitDir, "git dir"})
	common := commonDirOf(gitDir)
	if common != "" && common != gitDir {
		out = append(out, gitLocation{common, "common git dir"})
	}
	if common == "" {
		common = gitDir
	}
	if hp := hooksPathOf(common, gitDir, dir); hp != "" {
		out = append(out, gitLocation{hp, "core.hooksPath target"})
	}
	return out
}

// real resolves symlinks, falling back to the cleaned input.
func real(p string) string {
	if r := patheval.ResolveRealPath(p); r != "" {
		return r
	}
	return filepath.Clean(p)
}

// looksLikeGitDir reports whether dir is itself a git directory (bare repo
// or a --separate-git-dir target): HEAD, objects/ and refs/ all present.
// Matches git's own is_git_directory heuristic (HEAD file, objects dir, refs
// dir; git source setup.c, is_git_directory).
func looksLikeGitDir(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	for _, sub := range []string{"objects", "refs"} {
		if fi, err := os.Stat(filepath.Join(dir, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// commonDirOf reads gitDir/commondir (git help gitrepository-layout:
// "commondir ... if this file exists, the git dir is a linked worktree's
// admin dir and the file names the common dir, absolute or relative to the
// admin dir"). "" when the file is absent.
func commonDirOf(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return ""
	}
	c := strings.TrimSpace(string(b))
	if c == "" {
		return ""
	}
	if !filepath.IsAbs(c) {
		c = filepath.Join(gitDir, c)
	}
	return real(c)
}

// hooksPathOf returns the core.hooksPath target configured in the repo's
// config (common dir) or per-worktree config, resolved like git does
// (git help config: core.hooksPath - "~" expands, a relative path is
// relative to the directory where the hooks are run, i.e. the working tree
// root). "" when unset or unresolvable.
func hooksPathOf(common, gitDir, workTree string) string {
	val := ""
	for _, cfg := range []string{filepath.Join(common, "config"), filepath.Join(gitDir, "config.worktree")} {
		if v, ok := readCoreHooksPath(cfg); ok {
			val = v
		}
	}
	if val == "" {
		return ""
	}
	switch {
	case val == "~" || strings.HasPrefix(val, "~/"):
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		val = filepath.Join(home, strings.TrimPrefix(val, "~"))
	case !filepath.IsAbs(val):
		val = filepath.Join(workTree, val)
	}
	return real(val)
}

// readCoreHooksPath scans a git config file for [core] hooksPath. It is a
// deliberately small scanner (section header + key = value, section and
// variable names case-insensitive, per git help config SYNTAX): anything it
// cannot read is "not set", the ordinary path policy still applies.
func readCoreHooksPath(file string) (string, bool) {
	f, err := os.Open(file)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	inCore := false
	val, found := "", false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			hdr := strings.ToLower(strings.TrimSpace(strings.Trim(line, "[]")))
			// `[core]` with an optional trailing key=value on the same line
			// is not handled; a subsectioned `[core "x"]` is not core.
			inCore = hdr == "core"
			continue
		}
		if !inCore {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.ToLower(strings.TrimSpace(k)) != "hookspath" {
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"`)
		val, found = v, true
	}
	return val, found
}

// isTempPath is temproot.Under, indirected so tests can classify a fixture
// repo as NON-temp (every directory a test can create lives under some temp
// root, so the Forbidden branch is otherwise unreachable from a test).
var isTempPath = temproot.Under

// SetTempClassifierForTest replaces the "is this path under a temp root"
// predicate GitDirWriteVerdict uses and returns a restore func. Every
// directory a test can create sits under some temp root, so without this
// seam the non-temp (Forbidden) branch could not be exercised end to end
// (including from effectpolicy's tests). It is not used by production code.
func SetTempClassifierForTest(f func(string) bool) (restore func()) {
	prev := isTempPath
	isTempPath = f
	return func() { isTempPath = prev }
}
