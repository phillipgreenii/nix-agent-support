package pathsafety

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// This file implements ADR 0049's narrow execution-path carve-out: a plugin's own
// `hooks/hooks.json` manifest, and the scripts it names via `${CLAUDE_PLUGIN_ROOT}`,
// abstain on write — the REST of `.claude/plugins/**` (cache/, installed_plugins.json,
// known_marketplaces.json, blocklist.json, marketplaces/** content that is not a hook
// script) stays approved, per ADR 0041's collateral argument against a subtree rule.
//
// pluginsDirName is the one directory name, sitting directly inside `.claude`, that
// this file's predicates key off of. Everything below it belongs to plugin checkouts
// (ADR 0041's Decision already leaves the whole subtree approved by default); this
// file only carves execution-bearing paths back out of that approval.
const pluginsDirName = "plugins"

// pluginHooksManifestName is the fixed basename Claude Code plugins use for their
// hook manifest. Matched case-insensitively for the same APFS-bypass reason as
// agentConfigBasenames (pg2-2ng80): the directory-component matches in this file
// already fold case, and leaving the basename exact would reopen that hole one
// component over.
const pluginHooksManifestName = "hooks.json"

// pluginRootRefPrefix is the one substitution mechanism this carve-out understands
// for "the scripts a hooks.json names": Claude Code's own `${CLAUDE_PLUGIN_ROOT}`
// variable, which every real hooks.json on this machine uses to reference its own
// scripts (verified across pg-pr, claude-extended-tool-approver, claude-plugins-official's
// security-guidance, beads, superpowers). A bare command name with no such reference
// (e.g. "claude-extended-tool-approver", "bd codex-hook SessionStart") names a
// PATH-resolved binary, not a file inside the plugin tree, and is deliberately not
// treated as naming anything — there is no path to resolve. Guessing at bare relative
// paths or file extensions instead was explicitly rejected (pg2-m0uza's acceptance
// criteria: "an extension heuristic is not it") because it would either miss real
// references or manufacture false ones; ${CLAUDE_PLUGIN_ROOT} is unambiguous.
const pluginRootRefPrefix = "${CLAUDE_PLUGIN_ROOT}"

// pluginRootRefPattern captures the path fragment immediately following
// pluginRootRefPrefix, up to the next whitespace or quote character — the shape
// every observed hooks.json command string uses, quoted or not:
//
//	"${CLAUDE_PLUGIN_ROOT}/hooks/require-agent-pr-comment-marker.sh"
//	bash "${CLAUDE_PLUGIN_ROOT}/hooks/sg-python.sh" "${CLAUDE_PLUGIN_ROOT}/hooks/x.py"
//
// Built from pluginRootRefPrefix (via regexp.QuoteMeta) rather than a second
// hand-written literal, so the two cannot drift apart.
var pluginRootRefPattern = regexp.MustCompile(regexp.QuoteMeta(pluginRootRefPrefix) + `([^\s"'` + "`" + `]*)`)

// errPluginManifestAbsent is a sentinel distinguishing "no hooks.json exists here"
// (ordinary plugin content — nothing is named, so approve as usual) from every OTHER
// resolution failure (fail-safe — see isPluginHooksExecutionPath).
var errPluginManifestAbsent = errors.New("plugin hooks manifest does not exist")

// pluginHooksDir reports the plugin's own `hooks/` directory that an already-cleaned
// path sits AT OR BENEATH, if any. It requires `.claude` to be immediately followed
// by `plugins` (ADR 0041's scope for the subtree this carve-out narrows), with at
// least one plugin-root path component between `plugins` and the `hooks` directory
// itself — every layout observed on this machine nests the plugin's own directory in
// between (`plugins/marketplaces/<mkt>/plugins/<name>/hooks/`,
// `plugins/cache/<mkt>/<name>/<version>/hooks/`), so `.claude/plugins/hooks/hooks.json`
// — no plugin-root component at all — does not match.
//
// Matching is case-folded on every component for the same APFS reason as
// isAgentHooksPath. The scan takes the FIRST `.claude`/`plugins` adjacency and the
// FIRST `hooks` component found at least two positions after it, which is the
// plugin's own hooks directory in every real layout; content nested further inside
// that `hooks/` directory (a script's own subdirectory, say) is still "at or beneath"
// it and does not need a second match.
func pluginHooksDir(path string) (dir string, ok bool) {
	parts := splitPath(path)
	for i := 0; i+1 < len(parts); i++ {
		if !strings.EqualFold(parts[i], agentConfigDir) || !strings.EqualFold(parts[i+1], pluginsDirName) {
			continue
		}
		for j := i + 3; j < len(parts); j++ {
			if strings.EqualFold(parts[j], agentHooksDirName) {
				return joinAbs(parts[:j+1]), true
			}
		}
	}
	return "", false
}

// joinAbs reconstructs an absolute path from splitPath's components. splitPath
// splits filepath.Clean's output on the separator, so for an absolute path parts[0]
// is always "" — filepath.Join alone would drop that leading empty element and
// silently produce a relative path, so the leading separator is restored explicitly.
func joinAbs(parts []string) string {
	return string(filepath.Separator) + filepath.Join(parts...)
}

// isPluginHooksExecutionPath reports whether an already-normalized absolute path is
// either a plugin's own hooks.json manifest, or a script that manifest names via
// ${CLAUDE_PLUGIN_ROOT}. It is the predicate isPluginHooksExecutionWrite normalizes
// two ways, mirroring isAgentConfigWrite / isAgentHooksWrite.
//
// FAIL-SAFE, per ADR 0049's acceptance criteria: a hooks.json that exists but cannot
// be read or parsed must not silently fall through to approve — this function
// returns true (abstain) for every path under that plugin's hooks/ directory in that
// case, because an unresolvable manifest cannot rule out that the path is named. Only
// a manifest that is genuinely ABSENT (ordinary plugin content that never shipped
// hooks) reports false and lets ordinary evaluation proceed.
func isPluginHooksExecutionPath(path string) bool {
	hooksDir, ok := pluginHooksDir(path)
	if !ok {
		return false
	}
	manifestPath := filepath.Join(hooksDir, pluginHooksManifestName)
	if pathsEqualFold(path, manifestPath) {
		return true
	}
	scripts, err := resolvePluginHookScripts(manifestPath, filepath.Dir(hooksDir))
	if err != nil {
		return !errors.Is(err, errPluginManifestAbsent)
	}
	for _, s := range scripts {
		if pathsEqualFold(path, s) {
			return true
		}
	}
	return false
}

// isPluginHooksExecutionWrite applies isPluginHooksExecutionPath the same
// double-normalized way isAgentConfigWrite applies isAgentConfigPath: the path as
// NAMED and the symlink-RESOLVED path, so a symlink elsewhere pointing INTO a
// plugin's hooks/ directory cannot be used to slip the write past this check.
func (r *Rule) isPluginHooksExecutionWrite(path string) bool {
	if isPluginHooksExecutionPath(r.eval.CleanPath(path)) {
		return true
	}
	return isPluginHooksExecutionPath(r.eval.ResolvePath(path))
}

// pathsEqualFold compares two cleaned paths for equality, case-INSENSITIVELY. This
// machine's home volume folds case (APFS), so a script named by hooks.json in one
// spelling and written in another still name the same real file — the identical
// reasoning isAgentConfigPath documents for pg2-2ng80, applied here to whole-path
// comparison instead of a single component.
func pathsEqualFold(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// resolvePluginHookScripts reads and parses the hooks.json at manifestPath and
// returns the absolute paths of every script it names via ${CLAUDE_PLUGIN_ROOT},
// resolved against pluginRoot (the plugin's own root directory, one level above its
// hooks/ directory).
//
// Returns errPluginManifestAbsent specifically when the manifest does not exist —
// the one case callers must NOT treat as fail-safe, since it means the plugin never
// shipped a manifest and nothing is named. Any other error (permission denied,
// malformed JSON) is returned as-is, and callers MUST treat that as fail-safe
// (abstain), per this file's package-level doc comment.
func resolvePluginHookScripts(manifestPath, pluginRoot string) ([]string, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errPluginManifestAbsent
		}
		return nil, err
	}
	var manifest any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	var commands []string
	collectCommandStrings(manifest, &commands)
	var scripts []string
	for _, cmd := range commands {
		for _, m := range pluginRootRefPattern.FindAllStringSubmatch(cmd, -1) {
			scripts = append(scripts, filepath.Join(pluginRoot, m[1]))
		}
	}
	return scripts, nil
}

// collectCommandStrings recursively walks a generically-decoded JSON value
// (map[string]any / []any, per encoding/json's default unmarshal-into-any shapes)
// and appends every string found under a key literally named "command" to out.
//
// The walk is schema-agnostic ON PURPOSE: hooks.json event names (PreToolUse,
// SessionStart, ...) and per-hook fields (matcher, timeout, if, asyncRewake, ...)
// vary across the real manifests on this machine, and a typed struct would need to
// enumerate all of them and still break on the next Claude Code hook field. Scanning
// for the "command" key wherever it appears is robust to that variation and to
// nesting depth, at the cost of also matching a "command" key that is not a real
// hook entry — an over-match here only WIDENS the set of paths this carve-out
// abstains on, which is the fail-safe direction (see this file's package doc
// comment on the asymmetry).
func collectCommandStrings(node any, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			if k == "command" {
				if s, ok := val.(string); ok {
					*out = append(*out, s)
				}
			}
			collectCommandStrings(val, out)
		}
	case []any:
		for _, item := range v {
			collectCommandStrings(item, out)
		}
	}
}
