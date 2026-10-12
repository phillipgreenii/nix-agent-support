package pathspec

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// R2 REPO-TRUST BOUNDARY (Phase 2h(1), docket tc-o14i5.3, design "R2
// boundary"): "project root, its git worktrees, and members of the same
// pn-workspace workforest set (pn-workspace.toml lookup, cached). Nested
// git repos that are neither worktrees nor submodules at the parent's
// recorded gitlink are OUTSIDE (checked under P9, cached per evaluation).
// Untracked non-git content under the root is INSIDE (accepted)." This file
// is the general-purpose classification machinery K5 (config-exec-sources),
// K6 (data-read-paths), K8 (secret-classification), and K13
// (read-glob-grep adapters) consume for their own "is this path inside the
// trust boundary" question (this packet's Contract "Produces").
//
// # Per-evaluation cache scope
//
// The pn-workspace.toml lookup (WorkforestMembers, discoverWorkforestMembers)
// is the one part of this boundary check that touches the filesystem beyond
// a handful of Lstat calls already cheap enough to repeat. R2Cache exists so
// a caller can construct ONE instance per hook evaluation (one Evaluate call
// over one hookInput) and thread it through every InsideRepoTrust call for
// that evaluation, memoizing the lookup per project root — never shared
// across separate hook invocations or held at package level, which would
// leak stale workspace-membership data across unrelated invocations (the
// same reasoning workspace.go's package doc comment gives for threading a
// cache through, rather than holding one globally).

// R2Cache memoizes the pn-workspace.toml workforest-membership lookup
// InsideRepoTrust needs, scoped to the lifetime of one hook evaluation.
// The zero value is not usable; construct one with NewR2Cache(). Safe for
// concurrent use.
type R2Cache struct {
	mu      sync.Mutex
	members map[string]map[string]bool
	// K8 (tc-o14i5.3.8) per-evaluation memo for secretspec.go: tracked-file
	// vouch results by abs path (so a second lookup never re-shells out to
	// git), the user-level declarations, and repo-level declarations by root.
	vouch    map[string]vouchEntry
	userDecl *SecretDeclarations
	repoDecl map[string]SecretDeclarations
}

// NewR2Cache returns a fresh, empty R2Cache — construct one per hook
// evaluation (see this file's package doc comment for why a package-level
// cache would be wrong).
func NewR2Cache() *R2Cache {
	return &R2Cache{members: make(map[string]map[string]bool), vouch: make(map[string]vouchEntry), repoDecl: make(map[string]SecretDeclarations)}
}

// WorkforestMembers returns the resolved absolute repo roots that are
// members of the SAME pn-workspace workforest SET as projectRoot (each
// direct subdirectory of the set container projectRoot itself sits in),
// or nil if projectRoot is not itself a workforest member repo at all. The
// pn-workspace.toml lookup this requires (IsWorkforestSetContainer,
// pnWorkforestsDir) is performed at most once per projectRoot for this
// cache's lifetime — a second call with the same projectRoot returns the
// memoized result without touching the filesystem again.
func (c *R2Cache) WorkforestMembers(projectRoot string) map[string]bool {
	if c == nil || projectRoot == "" {
		return discoverWorkforestMembers(projectRoot)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if members, ok := c.members[projectRoot]; ok {
		return members
	}
	members := discoverWorkforestMembers(projectRoot)
	c.members[projectRoot] = members
	return members
}

// discoverWorkforestMembers is R2Cache.WorkforestMembers' uncached core: it
// reads the pn-workspace.toml lookup (via the already-exported
// IsWorkforestSetContainer/pnWorkforestsDir machinery worktree.go/
// workspace.go declare for the SAME convention pnKind's own Protected
// declaration uses) and lists projectRoot's sibling member directories.
func discoverWorkforestMembers(projectRoot string) map[string]bool {
	if projectRoot == "" {
		return nil
	}
	setDir := filepath.Dir(projectRoot)
	if setDir == projectRoot || !IsWorkforestSetContainer(setDir) {
		return nil
	}
	entries, err := os.ReadDir(setDir)
	if err != nil {
		return nil
	}
	members := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if r := patheval.ResolveRealPath(filepath.Join(setDir, e.Name())); r != "" {
			members[r] = true
		}
	}
	if len(members) == 0 {
		return nil
	}
	return members
}

// InsideRepoTrust answers R2's repo-trust-boundary question for abs (an
// already resolved, absolute path) against projectRoot (the project's own
// already-resolved absolute root): project root itself, everything under it
// with no intervening untrusted-nested-repo boundary (which includes
// untracked non-git content, and the project's own git worktrees — a
// worktree's `.git` FILE marker means it is never itself flagged as a
// boundary; see nestedUntrustedRepo), and members of the same pn-workspace
// workforest SET are INSIDE. A nested git repo under projectRoot that is
// neither a git worktree of projectRoot nor a directory recorded as a
// submodule in projectRoot's own .gitmodules is OUTSIDE. cache may be nil
// (a fresh, uncached lookup is performed instead — the CORRECT but slower
// fallback, never a wrong answer).
func InsideRepoTrust(cache *R2Cache, projectRoot, abs string) (inside bool, reason string) {
	if projectRoot == "" || abs == "" {
		return false, "no project root or path to resolve"
	}
	projectRoot = filepath.Clean(projectRoot)
	abs = filepath.Clean(abs)
	if abs == projectRoot {
		return true, "project root itself"
	}
	if patheval.PathContains(projectRoot, abs) {
		if outside, blocker := nestedUntrustedRepo(projectRoot, abs); outside {
			return false, "nested git repo at " + blocker + " is neither a worktree nor a recorded submodule of " + projectRoot
		}
		return true, "under project root " + projectRoot
	}
	members := cache.WorkforestMembers(projectRoot)
	for member := range members {
		if member == abs || patheval.PathContains(member, abs) {
			return true, "member of the same pn-workspace workforest set (" + member + ")"
		}
	}
	return false, "outside project root " + projectRoot + " and not a pn-workspace workforest sibling"
}

// nestedUntrustedRepo walks from abs up to (but not including) projectRoot,
// collecting every directory that is itself a git root (a `.git` entry
// present directly under it — a worktree's FILE marker or a real
// repository's directory marker, either counts). The SHALLOWEST one found
// (closest to projectRoot) is the boundary that governs abs: a git worktree
// of projectRoot (IsWorktreeRoot) or a directory recorded as a submodule in
// projectRoot's own .gitmodules is not a boundary at all (INSIDE); any
// other nested git root is OUTSIDE (P9, R2's own binding text).
func nestedUntrustedRepo(projectRoot, abs string) (outside bool, blockerRoot string) {
	var nestedRoots []string
	for dir := abs; dir != projectRoot; dir = filepath.Dir(dir) {
		if hasGitEntry(dir) {
			nestedRoots = append(nestedRoots, dir)
		}
		if filepath.Dir(dir) == dir {
			break // reached the filesystem root without reaching projectRoot
		}
	}
	if len(nestedRoots) == 0 {
		return false, ""
	}
	root := nestedRoots[len(nestedRoots)-1] // shallowest: last appended, walking deepest-first
	if IsWorktreeRoot(root) || isRecordedSubmodule(projectRoot, root) {
		return false, ""
	}
	return true, root
}

// hasGitEntry reports whether dir directly contains a `.git` entry (file or
// directory — a worktree's marker is a file; a real repository's is a
// directory; this check does not need to tell them apart, only find them).
func hasGitEntry(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// isRecordedSubmodule reports whether root (an absolute path under
// projectRoot) is listed as a submodule `path` in projectRoot's own
// .gitmodules file, at whatever gitlink commit is currently checked out —
// a plain line scan (mirroring pnWorkforestsDir's own single-value read;
// neither needs a full INI parser).
func isRecordedSubmodule(projectRoot, root string) bool {
	rel, err := filepath.Rel(projectRoot, root)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	f, err := os.Open(filepath.Join(projectRoot, ".gitmodules"))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "path") {
			continue
		}
		_, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = filepath.ToSlash(filepath.Clean(strings.TrimSpace(v)))
		if v == rel {
			return true
		}
	}
	return false
}
