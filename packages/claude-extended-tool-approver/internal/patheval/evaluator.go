package patheval

import (
	"os"
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
	cwd            string
	home           string
	xdgDataHome    string
	workspaceRoot  string
	gradleHome     string
	tmpRoot        string
	sandboxConfig  *SandboxFilesystemConfig
	mounts         []Mount // non-nil with inContainer=true enables container mode
	inContainer    bool
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
		projectRoot:    projectRoot,
		rawProjectRoot: rawProjectRoot,
		cwd:            projectRoot,
		home:           home,
		xdgDataHome:   xdgData,
		workspaceRoot: workspaceRoot,
		gradleHome:    gradleHome,
		tmpRoot:       tmpRoot,
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
		projectRoot:    projectRoot,
		rawProjectRoot: rawProjectRoot,
		cwd:            cwd,
		home:           home,
		xdgDataHome:    xdgData,
		workspaceRoot:  workspaceRoot,
		gradleHome:     gradleHome,
		tmpRoot:        tmpRoot,
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
		projectRoot:    pe.projectRoot,
		rawProjectRoot: pe.rawProjectRoot,
		cwd:            cwd,
		home:           pe.home,
		xdgDataHome:    pe.xdgDataHome,
		workspaceRoot:  pe.workspaceRoot,
		gradleHome:     pe.gradleHome,
		tmpRoot:        pe.tmpRoot,
		sandboxConfig:  pe.sandboxConfig,
		mounts:         pe.mounts,
		inContainer:    pe.inContainer,
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
	// <projectRoot>/**
	if strings.HasPrefix(path+"/", pe.projectRoot+"/") {
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
	return PathUnknown
}

// classifyWithoutEscapeCheck runs zone classification on an already-resolved path,
// skipping the symlink escape check. Used by the escape check itself to determine
// what zone the symlink target lands in.
func (pe *PathEvaluator) classifyWithoutEscapeCheck(path string) PathAccess {
	if strings.HasPrefix(path+"/", pe.projectRoot+"/") {
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
	return PathUnknown
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
		if pe.home != "" {
			path = pe.home + path[1:]
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
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return cwd
}
