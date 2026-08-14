package patheval

import (
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
)

var unexpandedVarPattern = regexp.MustCompile(`\$[A-Za-z_{]`)

type PathAccess int

const (
	PathReject    PathAccess = iota // explicitly blocked (e.g., ~/.ssh, ~/.gnupg)
	PathUnknown                     // not in any configured zone
	PathReadOnly                    // content reading allowed
	PathReadWrite                   // reading and writing allowed
)

func (pa PathAccess) String() string {
	switch pa {
	case PathReject:
		return "reject"
	case PathUnknown:
		return "unknown"
	case PathReadOnly:
		return "read-only"
	case PathReadWrite:
		return "read-write"
	default:
		return "invalid"
	}
}

// CanRead returns true if the path is in a zone that allows content reading.
func (pa PathAccess) CanRead() bool {
	return pa == PathReadOnly || pa == PathReadWrite
}

// CanWrite returns true if the path is in a zone that allows writing.
func (pa PathAccess) CanWrite() bool {
	return pa == PathReadWrite
}

type PathEvaluator struct {
	projectRoot    string // symlink-resolved
	rawProjectRoot string // cleaned but not symlink-resolved (for escape detection)
	// projectRootGrantsZone is false when projectRoot is too BROAD to be treated as
	// a project — a FABRICATED root (DetectProjectRoot found no `.git` and handed
	// back its argument) that is $HOME or an ancestor of it. See rootGrantsZone.
	projectRootGrantsZone bool
	cwd                   string
	home                  string
	xdgDataHome           string
	workspaceRoot         string
	gradleHome            string
	tmpRoot               string
	sandboxConfig         *SandboxFilesystemConfig
	mounts                []Mount // non-nil with inContainer=true enables container mode
	inContainer           bool
	// extraReadWrite / extraReadOnly are symlink-resolved roots configured via
	// the CETA_EXTRA_READWRITE_ROOTS / CETA_EXTRA_READONLY_ROOTS env vars
	// (":"-separated absolute paths). They are checked LAST, after every
	// built-in zone (see Evaluate).
	extraReadWrite []string
	extraReadOnly  []string
}

// resolveExtraRoots reads a ":"-separated list of absolute paths from the named
// env var, drops empties, and returns each symlink-resolved (dropping any that
// resolve to "").
func resolveExtraRoots(envVar string) []string {
	raw := os.Getenv(envVar)
	if raw == "" {
		return nil
	}
	var roots []string
	for _, p := range strings.Split(raw, ":") {
		if p == "" {
			continue
		}
		if r := resolveRefPath(p); r != "" {
			roots = append(roots, r)
		}
	}
	return roots
}

// evalSymlinksWithFallback resolves symlinks in path. If path doesn't exist,
// it walks up the directory tree to find the nearest existing ancestor, resolves
// that, and reattaches the remaining suffix. Returns "" only for broken symlinks
// or if the filesystem root is reached without finding any resolvable ancestor.
func evalSymlinksWithFallback(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Check if the path itself exists as a symlink (broken symlink case)
		if info, lstatErr := os.Lstat(path); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return ""
		}
		// Path doesn't exist — walk up to find an existing ancestor, then reconstruct
		suffix := ""
		dir := path
		for {
			parent := filepath.Dir(dir)
			if parent == dir {
				// Reached filesystem root without finding a resolvable ancestor
				return ""
			}
			base := filepath.Base(dir)
			if suffix == "" {
				suffix = base
			} else {
				suffix = filepath.Join(base, suffix)
			}
			dir = parent
			resolvedDir, dirErr := filepath.EvalSymlinks(dir)
			if dirErr == nil {
				return filepath.Clean(filepath.Join(resolvedDir, suffix))
			}
		}
	}
	return filepath.Clean(resolved)
}

// resolveRefPath cleans and resolves symlinks for a reference path (projectRoot,
// cwd, home, etc.). Unlike evalSymlinksWithFallback, an empty input stays empty.
func resolveRefPath(path string) string {
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	return evalSymlinksWithFallback(path)
}

func New(projectRoot string) *PathEvaluator {
	rawProjectRoot := filepath.Clean(projectRoot)
	projectRoot = resolveRefPath(projectRoot)
	home, _ := os.UserHomeDir()
	home = resolveRefPath(home)
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" && home != "" {
		xdgData = filepath.Join(home, ".local", "share")
	} else {
		xdgData = resolveRefPath(xdgData)
	}
	workspaceRoot := os.Getenv("WORKSPACE_ROOT")
	if workspaceRoot != "" {
		workspaceRoot = resolveRefPath(workspaceRoot)
	}
	gradleHome := os.Getenv("GRADLE_USER_HOME")
	if gradleHome == "" && home != "" {
		gradleHome = filepath.Join(home, ".gradle")
	} else {
		gradleHome = resolveRefPath(gradleHome)
	}
	tmpRoot := evalSymlinksWithFallback("/tmp")
	if tmpRoot == "" {
		tmpRoot = "/tmp"
	}
	return &PathEvaluator{
		projectRoot:           projectRoot,
		projectRootGrantsZone: rootGrantsZone(projectRoot, home),
		rawProjectRoot:        rawProjectRoot,
		cwd:                   projectRoot,
		home:                  home,
		xdgDataHome:           xdgData,
		workspaceRoot:         workspaceRoot,
		gradleHome:            gradleHome,
		tmpRoot:               tmpRoot,
		extraReadWrite:        resolveExtraRoots("CETA_EXTRA_READWRITE_ROOTS"),
		extraReadOnly:         resolveExtraRoots("CETA_EXTRA_READONLY_ROOTS"),
	}
}

func NewWithCWD(projectRoot, cwd string) *PathEvaluator {
	rawProjectRoot := filepath.Clean(projectRoot)
	projectRoot = resolveRefPath(projectRoot)
	cwd = resolveRefPath(cwd)
	home, _ := os.UserHomeDir()
	home = resolveRefPath(home)
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" && home != "" {
		xdgData = filepath.Join(home, ".local", "share")
	} else {
		xdgData = resolveRefPath(xdgData)
	}
	workspaceRoot := os.Getenv("WORKSPACE_ROOT")
	if workspaceRoot != "" {
		workspaceRoot = resolveRefPath(workspaceRoot)
	}
	gradleHome := os.Getenv("GRADLE_USER_HOME")
	if gradleHome == "" && home != "" {
		gradleHome = filepath.Join(home, ".gradle")
	} else {
		gradleHome = resolveRefPath(gradleHome)
	}
	tmpRoot := evalSymlinksWithFallback("/tmp")
	if tmpRoot == "" {
		tmpRoot = "/tmp"
	}
	return &PathEvaluator{
		projectRoot:           projectRoot,
		projectRootGrantsZone: rootGrantsZone(projectRoot, home),
		rawProjectRoot:        rawProjectRoot,
		cwd:                   cwd,
		home:                  home,
		xdgDataHome:           xdgData,
		workspaceRoot:         workspaceRoot,
		gradleHome:            gradleHome,
		tmpRoot:               tmpRoot,
		extraReadWrite:        resolveExtraRoots("CETA_EXTRA_READWRITE_ROOTS"),
		extraReadOnly:         resolveExtraRoots("CETA_EXTRA_READONLY_ROOTS"),
	}
}

func (pe *PathEvaluator) ProjectRoot() string {
	return pe.projectRoot
}

// SetSandboxConfig sets the sandbox filesystem path config, resolving symlinks
// in all config paths.
func (pe *PathEvaluator) SetSandboxConfig(cfg *SandboxFilesystemConfig) {
	if cfg == nil {
		pe.sandboxConfig = nil
		return
	}
	pe.sandboxConfig = &SandboxFilesystemConfig{
		DenyRead:   resolveConfigPaths(cfg.DenyRead),
		DenyWrite:  resolveConfigPaths(cfg.DenyWrite),
		AllowRead:  resolveConfigPaths(cfg.AllowRead),
		AllowWrite: resolveConfigPaths(cfg.AllowWrite),
	}
}

// WithCWD returns a new PathEvaluator with a different CWD but the same config
// and resolved reference paths. Use this instead of NewWithCWD when the caller
// already has a configured evaluator (e.g., safecmds per-command CWD).
func (pe *PathEvaluator) WithCWD(cwd string) *PathEvaluator {
	cwd = resolveRefPath(cwd)
	return &PathEvaluator{
		projectRoot:           pe.projectRoot,
		projectRootGrantsZone: pe.projectRootGrantsZone,
		rawProjectRoot:        pe.rawProjectRoot,
		cwd:                   cwd,
		home:                  pe.home,
		xdgDataHome:           pe.xdgDataHome,
		workspaceRoot:         pe.workspaceRoot,
		gradleHome:            pe.gradleHome,
		tmpRoot:               pe.tmpRoot,
		sandboxConfig:         pe.sandboxConfig,
		mounts:                pe.mounts,
		inContainer:           pe.inContainer,
		extraReadWrite:        pe.extraReadWrite,
		extraReadOnly:         pe.extraReadOnly,
	}
}

func (pe *PathEvaluator) Evaluate(path string) PathAccess {
	cleaned := pe.cleanPath(path)
	if cleaned == "" {
		return PathUnknown
	}
	if pe.inContainer {
		return pe.evaluateContainer(cleaned)
	}
	path = evalSymlinksWithFallback(cleaned)
	if path == "" {
		return PathUnknown
	}
	// Detect symlink escape: path appears to be in the project but resolves outside it.
	// Allow escapes to zones that are less permissive (read-only, reject) — the concern
	// is only when a symlink could escalate access (e.g., write to an unexpected location).
	if pe.rawProjectRoot != "" && pathContains(pe.rawProjectRoot, cleaned) && !pathContains(pe.projectRoot, path) {
		// Check if the resolved path lands in a known read-only or reject zone.
		// If so, allow it (the symlink target is less permissive than read-write project).
		// If the target is read-write (e.g., /tmp) or unknown, block the escape.
		resolvedZone := pe.classifyWithoutEscapeCheck(path)
		if resolvedZone == PathReadWrite || resolvedZone == PathUnknown {
			return PathUnknown
		}
		// Target is read-only or reject — safe to use that classification
		return resolvedZone
	}
	// <projectRoot>/** — skipped when the root is too BROAD to be a project
	// (pg2-byh62, see rootGrantsZone). THIS SITE AND classifyWithoutEscapeCheck'S MUST
	// BE KEPT IN STEP: the two functions carry independent copies of the same zone
	// ladder, and guarding only the other one left this hole fully open (measured — the
	// escape-check copy is reached only for a symlink that appears to be in the project
	// and resolves outside it, so it is not on the path an ordinary read takes).
	if pe.projectRootGrantsZone && strings.HasPrefix(path+"/", pe.projectRoot+"/") {
		return PathReadWrite
	}
	// WORKSPACE_ROOT/** (broader than project root, for multi-repo workspaces)
	if pe.workspaceRoot != "" {
		if strings.HasPrefix(path+"/", pe.workspaceRoot+"/") || path == pe.workspaceRoot {
			return PathReadWrite
		}
	}
	// /tmp/** (use resolved tmpRoot to handle symlinks like macOS /tmp -> /private/tmp)
	if strings.HasPrefix(path+"/", pe.tmpRoot+"/") || path == pe.tmpRoot {
		return PathReadWrite
	}
	// sandbox.filesystem.allowWrite paths
	if pe.sandboxConfig != nil {
		for _, rwp := range pe.sandboxConfig.AllowWrite {
			if pathContains(rwp, path) {
				return PathReadWrite
			}
		}
	}
	// /nix/**
	if strings.HasPrefix(path, "/nix/") || path == "/nix" {
		return PathReadOnly
	}
	// ~/.claude/ — plans and projects are readwrite (Claude writes plans and memory),
	// everything else is readonly (settings, credentials, etc.)
	if pe.home != "" {
		claudeDir := filepath.Join(pe.home, ".claude")
		if pathContains(claudeDir, path) {
			claudePlans := filepath.Join(claudeDir, "plans")
			claudeProjects := filepath.Join(claudeDir, "projects")
			if pathContains(claudePlans, path) || pathContains(claudeProjects, path) {
				return PathReadWrite
			}
			return PathReadOnly
		}
		// ~/.claude.json
		claudeJSON := filepath.Join(pe.home, ".claude.json")
		if path == claudeJSON {
			return PathReadOnly
		}
		// ~/go/pkg/**
		goPkg := filepath.Join(pe.home, "go", "pkg")
		if strings.HasPrefix(path+"/", goPkg+"/") || path == goPkg {
			return PathReadOnly
		}
	}
	// Gradle cache (GRADLE_USER_HOME or ~/.gradle)
	if pe.gradleHome != "" {
		if strings.HasPrefix(path+"/", pe.gradleHome+"/") || path == pe.gradleHome {
			return PathReadOnly
		}
	}
	// <xdgDataHome>/nix-support-local-plugins/**
	if pe.xdgDataHome != "" {
		nixPlugins := filepath.Join(pe.xdgDataHome, "nix-support-local-plugins")
		if strings.HasPrefix(path+"/", nixPlugins+"/") || path == nixPlugins {
			return PathReadOnly
		}
		// <xdgDataHome>/contained-claude/**
		containedClaude := filepath.Join(pe.xdgDataHome, "contained-claude")
		if pathContains(containedClaude, path) {
			return PathReadOnly
		}
		// <xdgDataHome>/claude-extended-tool-approver/**
		// ReadWrite: the tool's own database (asks.db) is a legitimate write target.
		extToolApprover := filepath.Join(pe.xdgDataHome, "claude-extended-tool-approver")
		if pathContains(extToolApprover, path) {
			return PathReadWrite
		}
		// <xdgDataHome>/claude-pretool-hook/** (old name)
		pretoolHook := filepath.Join(pe.xdgDataHome, "claude-pretool-hook")
		if pathContains(pretoolHook, path) {
			return PathReadOnly
		}
	}
	// Extra roots configured via env are checked LAST — built-in zones win.
	return pe.extraRootAccess(path)
}

// extraRootAccess classifies path against the env-configured extra roots,
// checking read-write before read-only. Returns PathUnknown when path is under
// no extra root. Checked only after every built-in zone (see Evaluate).
func (pe *PathEvaluator) extraRootAccess(path string) PathAccess {
	for _, root := range pe.extraReadWrite {
		if pathContains(root, path) {
			return PathReadWrite
		}
	}
	for _, root := range pe.extraReadOnly {
		if pathContains(root, path) {
			return PathReadOnly
		}
	}
	return PathUnknown
}

// classifyWithoutEscapeCheck runs zone classification on an already-resolved path,
// skipping the symlink escape check. Used by the escape check itself to determine
// what zone the symlink target lands in.
func (pe *PathEvaluator) classifyWithoutEscapeCheck(path string) PathAccess {
	// The project-root zone is skipped when the root is too BROAD to be a project —
	// a fabricated root at or above $HOME (pg2-byh62, see rootGrantsZone). Skipping it
	// does not by itself deny path: every zone below still applies, so a path that is
	// genuinely in the workspace, /tmp or an allowWrite root is unaffected.
	if pe.projectRootGrantsZone && strings.HasPrefix(path+"/", pe.projectRoot+"/") {
		return PathReadWrite
	}
	if pe.workspaceRoot != "" {
		if strings.HasPrefix(path+"/", pe.workspaceRoot+"/") || path == pe.workspaceRoot {
			return PathReadWrite
		}
	}
	if strings.HasPrefix(path+"/", pe.tmpRoot+"/") || path == pe.tmpRoot {
		return PathReadWrite
	}
	if pe.sandboxConfig != nil {
		for _, rwp := range pe.sandboxConfig.AllowWrite {
			if pathContains(rwp, path) {
				return PathReadWrite
			}
		}
	}
	if strings.HasPrefix(path, "/nix/") || path == "/nix" {
		return PathReadOnly
	}
	if pe.home != "" {
		claudeDir := filepath.Join(pe.home, ".claude")
		if pathContains(claudeDir, path) {
			claudePlans := filepath.Join(claudeDir, "plans")
			claudeProjects := filepath.Join(claudeDir, "projects")
			if pathContains(claudePlans, path) || pathContains(claudeProjects, path) {
				return PathReadWrite
			}
			return PathReadOnly
		}
		claudeJSON := filepath.Join(pe.home, ".claude.json")
		if path == claudeJSON {
			return PathReadOnly
		}
		goPkg := filepath.Join(pe.home, "go", "pkg")
		if strings.HasPrefix(path+"/", goPkg+"/") || path == goPkg {
			return PathReadOnly
		}
	}
	if pe.gradleHome != "" {
		if strings.HasPrefix(path+"/", pe.gradleHome+"/") || path == pe.gradleHome {
			return PathReadOnly
		}
	}
	if pe.xdgDataHome != "" {
		nixPlugins := filepath.Join(pe.xdgDataHome, "nix-support-local-plugins")
		if strings.HasPrefix(path+"/", nixPlugins+"/") || path == nixPlugins {
			return PathReadOnly
		}
		containedClaude := filepath.Join(pe.xdgDataHome, "contained-claude")
		if pathContains(containedClaude, path) {
			return PathReadOnly
		}
		extToolApprover := filepath.Join(pe.xdgDataHome, "claude-extended-tool-approver")
		if pathContains(extToolApprover, path) {
			return PathReadOnly
		}
		pretoolHook := filepath.Join(pe.xdgDataHome, "claude-pretool-hook")
		if pathContains(pretoolHook, path) {
			return PathReadOnly
		}
	}
	// Extra roots configured via env are checked LAST — built-in zones win.
	return pe.extraRootAccess(path)
}

// resolveConfigPaths resolves symlinks in a list of paths, dropping any that
// can't be resolved at all (e.g., completely broken symlinks).
func resolveConfigPaths(paths []string) []string {
	resolved := make([]string, 0, len(paths))
	for _, p := range paths {
		r := resolveRefPath(p)
		if r != "" {
			resolved = append(resolved, r)
		}
	}
	return resolved
}

// pathContains returns true if path is equal to or under dir.
func pathContains(dir, path string) bool {
	return strings.HasPrefix(path+"/", dir+"/") || path == dir
}

// cleanPath expands variables, ~, resolves relative paths, and cleans.
// Does NOT resolve symlinks.
func (pe *PathEvaluator) cleanPath(path string) string {
	path = os.ExpandEnv(path)
	if unexpandedVarPattern.MatchString(path) {
		return ""
	}
	if strings.HasPrefix(path, "~") {
		switch {
		case path == "~" || strings.HasPrefix(path, "~/"):
			// Bare "~" and "~/..." expand via the current user's HOME (tc-sfpto).
			if pe.home != "" {
				path = pe.home + path[1:]
			}
		default:
			// "~user" / "~user/rest" — tilde + a username with no leading slash.
			// Resolve the NAMED user's home via os/user, then append the
			// remainder. tc-fielf: this branch previously fell into the case
			// above and produced pe.home + "user" (e.g. /Users/testuseruser), a
			// garbage path that could land in a writable zone and auto-approve
			// `rm -rf ~someuser`.
			name := path[1:]
			rest := ""
			if i := strings.IndexByte(name, '/'); i >= 0 {
				rest = name[i:]
				name = name[:i]
			}
			u, err := user.Lookup(name)
			if err != nil {
				// Unknown user: do NOT silently expand to a garbage path. Return
				// "" so Evaluate classifies it PathUnknown (neither readable nor
				// writable), which keeps the referencing command from being
				// auto-approved (matches the unexpanded-variable fallback above).
				return ""
			}
			path = u.HomeDir + rest
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(pe.cwd, path)
	}
	return filepath.Clean(path)
}

func (pe *PathEvaluator) resolvePath(path string) string {
	path = pe.cleanPath(path)
	if path == "" {
		return ""
	}
	return evalSymlinksWithFallback(path)
}

// CleanPath exposes cleanPath: the env-expanded, ~-expanded, cwd-relative-resolved,
// lexically cleaned form of path, WITHOUT symlink resolution. Use it when a rule has
// to reason about the file a request NAMES rather than the zone the file lands in
// (Evaluate answers the latter). Returns "" when the path holds an unexpanded
// variable.
func (pe *PathEvaluator) CleanPath(path string) string {
	return pe.cleanPath(path)
}

// ResolvePath exposes resolvePath: CleanPath plus symlink resolution. Returns ""
// when the path cannot be resolved at all (broken symlink, unexpanded variable).
func (pe *PathEvaluator) ResolvePath(path string) string {
	return pe.resolvePath(path)
}

// IsDenyRead returns true if path is blocked for reading by sandbox.filesystem.denyRead,
// accounting for allowRead overrides (allowRead takes precedence over denyRead).
func (pe *PathEvaluator) IsDenyRead(path string) bool {
	if pe.sandboxConfig == nil {
		return false
	}
	resolved := pe.resolvePath(path)
	if resolved == "" {
		return false
	}
	for _, p := range pe.sandboxConfig.AllowRead {
		if pathContains(p, resolved) {
			return false // allowRead takes precedence over denyRead
		}
	}
	for _, p := range pe.sandboxConfig.DenyRead {
		if pathContains(p, resolved) {
			return true
		}
	}
	return false
}

// IsDenyWrite returns true if path is blocked for writing by sandbox.filesystem.denyWrite.
// denyWrite has highest priority — it blocks even CWD and allowWrite paths.
func (pe *PathEvaluator) IsDenyWrite(path string) bool {
	if pe.sandboxConfig == nil {
		return false
	}
	resolved := pe.resolvePath(path)
	if resolved == "" {
		return false
	}
	for _, p := range pe.sandboxConfig.DenyWrite {
		if pathContains(p, resolved) {
			return true
		}
	}
	return false
}

// DetectProjectRoot returns the project root to attribute cwd to: MONOREPO_ROOT when
// cwd lies under it, else the nearest ancestor holding a `.git`, else CWD ITSELF.
//
// ITS RETURN VALUE IS AMBIGUOUS AND CALLERS MUST NOT READ IT AS "THIS IS A REPO ROOT"
// (pg2-byh62). The final fallback hands back the argument, so "cwd IS a project root"
// and "nothing was found, here is your cwd back" are the same string and cannot be told
// apart afterwards. Use `InGitRepo(path)` for the yes/no question — that is this
// function's `.git` walk exposed as a predicate, and it exists precisely because a
// security control MUST NOT be relaxed on an answer that might be a fallback
// (pg2-pmk9q).
//
// THE AMBIGUITY BEING UNDOCUMENTED IS WHAT MADE pg2-byh62 INVISIBLE. A fabricated root
// is still granted a PathReadWrite zone over its whole subtree, so from a non-repo cwd
// that whole subtree became readable — and with cwd=$HOME that is every dotfile the
// user owns. Measured on main @71a6abba with the deny-list applied,
// `cwd=$HOME cat ~/.npmrc` returned `approve` while the same command from any repo cwd
// returned `abstain`.
//
// THE ZONE IS STILL GRANTED FOR MOST FABRICATED ROOTS, deliberately — see
// rootGrantsZone for which are refused and why the answer is not "all of them".
func DetectProjectRoot(cwd string) string {
	cwd = filepath.Clean(cwd)

	// Check env vars, but only use them if cwd is under the specified root
	if root := os.Getenv("MONOREPO_ROOT"); root != "" {
		root = filepath.Clean(root)
		if strings.HasPrefix(cwd+"/", root+"/") || cwd == root {
			return root
		}
	}
	// Walk up looking for .git
	if root, ok := gitRoot(cwd); ok {
		return root
	}
	return cwd
}

// rootGrantsZone reports whether root may grant a PathReadWrite zone over its whole
// subtree. THIS IS THE RECORDED DECISION FOR pg2-byh62, and the decision is NOT
// "a fabricated root grants nothing".
//
// THE HAZARD IS BREADTH, NOT FABRICATION. Two facts settled it, both measured:
//
//   - THE FABRICATED ROOT IS THE NORMAL CASE HERE, not an edge case. This machine's
//     pn-workspace root `/Users/phillipg/phillipg_mbp` holds no `.git` of its own — it
//     is a directory OF sibling repos — and it is the single most common cwd in the
//     asklog corpus: 36,408 of 139,842 replayable rows, with 47,829 (34%) from
//     non-repo cwds in total. Refusing every fabricated root would strip the zone from
//     the directory agents work in most, which is the prompt flood this bead's own
//     text warned to measure before choosing.
//   - THE DANGEROUS FABRICATED ROOTS ARE THE ONES THAT SWALLOW $HOME. `$HOME` as a
//     root covers `.npmrc`, `.config/gcloud/`, `.terraform.d/` and every other
//     credential file not named in `sandbox.filesystem.denyRead`; `/` is worse. The
//     workspace root covers repos and scratch notes. Those are different kinds of
//     answer, and only the second is a project.
//
// So a fabricated root is refused iff it IS $HOME or an ANCESTOR of $HOME. Everything
// else keeps today's behaviour.
//
// A REAL `.git` AT $HOME IS DELIBERATELY STILL HONOURED. Some people version-control
// their home directory, and that is an explicit declaration by the user that it is a
// project — unlike a fabricated root, which is this package inventing one. Keying on
// InGitRepo rather than on the path alone is what preserves that distinction.
//
// THE COMPENSATING CONTROL FOR EVERY FABRICATED ROOT THAT IS STILL HONOURED is the
// secrets rule: `secretpath.IsSecret` plus `sandbox.filesystem.denyRead`, both of which
// run BEFORE the zone model and are cwd-independent (measured: `cat ~/.kube/config`
// returns `reject` from the workspace root, from $HOME and from a non-repo temp dir
// alike). That control is a NAMED LIST, so it protects the paths someone remembered —
// which is exactly why the breadth guard above is worth having on top of it.
func rootGrantsZone(root, home string) bool {
	if root == "" {
		return false
	}
	if InGitRepo(root) {
		return true // a real repo root, or a path inside one: the user declared it
	}
	if home == "" {
		return true // breadth is undecidable; denying would strip every zone at once
	}
	// A fabricated root at or above $HOME covers every dotfile the user owns.
	return !coversPath(root, home)
}

// coversPath reports whether root is target or an ancestor of it.
//
// IT DOES NOT USE pathContains, DELIBERATELY, and this is not a style choice:
// pathContains compares `target+"/"` against `dir+"/"`, which for dir=="/" builds the
// prefix `"//"` and therefore reports FALSE for every path on the filesystem. That blind
// spot is harmless in pathContains' own callers (an allow/deny entry of `/` is not a real
// config) but fatal here — `/` is the BROADEST fabricated root there is, reachable from
// cwd=`/` via DetectProjectRoot's fallback, and treating it as covering nothing would
// have granted a read-write zone over the entire filesystem. Caught by
// TestRootGrantsZoneTable's `/` row.
func coversPath(root, target string) bool {
	if root == "" || target == "" {
		return false
	}
	if root == target {
		return true
	}
	if root == "/" {
		return strings.HasPrefix(target, "/")
	}
	return strings.HasPrefix(target, root+"/")
}

// InGitRepo reports whether path lies inside a git working tree — i.e. whether
// path itself or any ancestor of it holds a `.git` entry.
//
// It is DetectProjectRoot's `.git` walk exposed as a PREDICATE, and the two are
// not interchangeable: DetectProjectRoot FALLS BACK to its argument when no
// `.git` is found, so its return value cannot distinguish "this is a repo root"
// from "nothing was found, here is your cwd back". A caller that needs the
// yes/no answer — the `secrets` rule's in-repo relaxation (pg2-pmk9q) — must have
// it as a predicate or it will read the fallback as a repo.
//
// It deliberately does NOT honour MONOREPO_ROOT. That env var overrides which
// root a project is attributed to; it says nothing about whether a path is
// version-controlled, and honouring it would report `true` for a path outside
// any repo whenever the var happened to be set.
//
// `.git` is matched by os.Stat, so a WORKTREE — where `.git` is a FILE holding a
// `gitdir:` pointer rather than a directory — counts, which it must: agents in
// this workspace work almost exclusively in `.worktrees/<name>` checkouts, and a
// dir-only test would report every one of them as unversioned.
//
// The walk is unbounded upward and stops at the filesystem root, matching
// DetectProjectRoot. path SHOULD already be absolute and lexically cleaned (see
// PathEvaluator.CleanPath); a relative path is cleaned but is walked relative to
// the PROCESS cwd, which is rarely what a rule means.
func InGitRepo(path string) bool {
	if path == "" {
		return false
	}
	_, ok := gitRoot(filepath.Clean(path))
	return ok
}

// gitRoot walks up from dir — dir itself included — and returns the first
// ancestor holding a `.git` entry. The second result is false when the walk
// reaches the filesystem root without finding one; callers MUST branch on it
// rather than on the returned string, which is "" in that case.
func gitRoot(dir string) (string, bool) {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
