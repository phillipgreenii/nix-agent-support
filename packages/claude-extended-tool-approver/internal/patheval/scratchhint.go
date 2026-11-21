package patheval

import (
	"os"
	"path/filepath"
)

// ClaudeCodeTmpdirEnv names the environment variable that points at the agent
// scratch root (the nix module claude-code-tmp exports it).
const ClaudeCodeTmpdirEnv = "CLAUDE_CODE_TMPDIR"

// ScratchHint returns a sentence fragment to append to a refusal reason when path
// lies at or under the user cache directory ($HOME/.cache, or $XDG_CACHE_HOME when
// set); it returns "" for every other path, so a reason for any other path is
// unchanged.
//
// WHY: creating a NEW top-level directory under ~/.cache is not auto-approved (the
// cache directory is classified neither temp nor read-write, so the write abstains),
// but an unattended session turns that abstain into a deny with only our reason text
// to go on. A reason that merely says "non-writable path" makes an agent retry the
// same write in a loop; naming CLAUDE_CODE_TMPDIR and its CURRENT VALUE (read from
// this process's own environment, never hardcoded) tells it where scratch belongs.
// The root itself is already read-write through the machine's extraReadWriteRoots, so
// a retry there is approved. The hint applies only to the paths a command or tool
// names: a tool that creates ~/.cache/<x> by itself names no path and is not seen.
func (pe *PathEvaluator) ScratchHint(path string) string {
	cleaned := pe.cleanPath(path)
	if cleaned == "" {
		return ""
	}
	var caches []string
	if pe.home != "" {
		caches = append(caches, filepath.Join(pe.home, ".cache"))
	}
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" && filepath.IsAbs(x) {
		caches = append(caches, filepath.Clean(x))
	}
	resolved := evalSymlinksWithFallback(cleaned)
	for _, c := range caches {
		rc := evalSymlinksWithFallback(c)
		if pathContains(c, cleaned) || (resolved != "" && rc != "" && pathContains(rc, resolved)) {
			return scratchHintText(os.Getenv(ClaudeCodeTmpdirEnv))
		}
	}
	return ""
}

func scratchHintText(value string) string {
	if value == "" {
		return "; do not create scratch under the user cache directory, use the directory named by " +
			ClaudeCodeTmpdirEnv + " (it is not set in this process, so use the session temp directory)"
	}
	return "; do not create scratch under the user cache directory, use " + ClaudeCodeTmpdirEnv + "=" + value
}
