package deletable

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// This file is an IN-PROCESS gitignore matcher for the DOCUMENTED SUBSET
// below. It exists because the alternative — shelling out to
// `git check-ignore` for every delete effect a command emits — is an exec
// per path inside a permission hook, and because the matcher's answer must
// be available for a path that does not exist yet (git check-ignore answers
// for non-existent paths too, but only one at a time). TestGitignoreAgainstGit
// (gitignore_test.go) verifies every construct listed here against the real
// `git check-ignore` on a fixture repository, and skips when git is absent.
//
// Supported (verified against git 2.54):
//
//   - Blank lines and `#` comments are skipped; a trailing unescaped space is
//     trimmed; `\#`, `\!`, `\ ` and `\\` escape the following character.
//   - A leading `!` negates: the LAST matching pattern wins, in file order,
//     with the files consulted in git's precedence order (.git/info/exclude,
//     then the root .gitignore, then each nested .gitignore from the root
//     down to the directory containing the path — a deeper file's patterns
//     are consulted AFTER a shallower file's, so they win).
//   - A trailing `/` restricts the pattern to DIRECTORIES. The matcher
//     stats the path; a path that does not exist is treated as not a
//     directory (so `build/` does not match a missing `build`).
//   - A pattern with a `/` anywhere but the end is ANCHORED to the directory
//     holding the .gitignore (a leading `/` anchors too and is stripped);
//     otherwise it matches the basename at any depth below that directory.
//   - `*` and `?` never match `/`; `[...]` character classes match one
//     non-`/` character (filepath.Match semantics per component).
//   - `**/` at the start matches in all directories; `/**` at the end matches
//     everything inside; `/**/` in the middle matches zero or more
//     directories.
//   - A path is ignored if ANY ancestor directory (below the repository root)
//     is ignored; a negation cannot re-include a path whose parent directory
//     is excluded (git's documented rule).
//
// Not supported, deliberately (each falls to "not ignored", the SAFE
// direction for a deletability source — an unsupported pattern can only make
// fewer paths deletable, never more): core.excludesFile (the user-global
// ignore file — it is per-user configuration, not part of the repository),
// `git config` overrides, and the index (a TRACKED path that also matches an
// ignore pattern is reported ignored here, whereas git reports it not
// ignored because tracking wins; a delete of a tracked-but-ignored path is
// therefore judged deletable — acceptable for the spike, noted for the
// workspace-declarations bead which may consult `git ls-files`).

// ignoreRule is one parsed gitignore pattern with its provenance directory.
type ignoreRule struct {
	// pattern is the cleaned pattern text with any leading `!`, leading `/`
	// and trailing `/` removed.
	pattern string
	// negate is true for a `!pattern` line.
	negate bool
	// dirOnly is true for a trailing-`/` pattern.
	dirOnly bool
	// anchored is true when the pattern contains a `/` (other than a trailing
	// one), so it matches only relative to base.
	anchored bool
	// base is the repository-relative directory ("" for the root) whose
	// .gitignore the rule came from; the pattern is evaluated against paths
	// relative to it.
	base string
}

// parseIgnoreFile reads one gitignore-format file. A missing file yields no
// rules and no error; any other read error is swallowed too (fail-safe: a
// file that cannot be read ignores nothing).
func parseIgnoreFile(path, base string) []ignoreRule {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var rules []ignoreRule
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if r, ok := parseIgnoreLine(sc.Text(), base); ok {
			rules = append(rules, r)
		}
	}
	return rules
}

// parseIgnoreLine parses one line of gitignore syntax into a rule, reporting
// false for a blank or comment line.
func parseIgnoreLine(line, base string) (ignoreRule, bool) {
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}
	line = trimTrailingSpace(line)
	if line == "" {
		return ignoreRule{}, false
	}
	r := ignoreRule{base: base}
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	}
	if strings.HasPrefix(line, "\\") && len(line) > 1 && (line[1] == '#' || line[1] == '!') {
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if strings.HasPrefix(line, "/") {
		r.anchored = true
		line = strings.TrimPrefix(line, "/")
	}
	if strings.Contains(line, "/") {
		r.anchored = true
	}
	if line == "" {
		return ignoreRule{}, false
	}
	r.pattern = line
	return r, true
}

// trimTrailingSpace drops unescaped trailing spaces, per gitignore(5).
func trimTrailingSpace(s string) string {
	for strings.HasSuffix(s, " ") && !strings.HasSuffix(s, "\\ ") {
		s = strings.TrimSuffix(s, " ")
	}
	return strings.ReplaceAll(s, "\\ ", " ")
}

// matches reports whether rule matches rel — a repository-relative,
// forward-slash path — given whether rel names a directory.
func (r ignoreRule) matches(rel string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	// Scope to the rule's base directory.
	if r.base != "" {
		if !strings.HasPrefix(rel, r.base+"/") {
			return false
		}
		rel = strings.TrimPrefix(rel, r.base+"/")
	}
	if !r.anchored {
		// Basename match at any depth: `*.log` matches `a/b/x.log`.
		return globMatch(r.pattern, filepath.Base(rel))
	}
	return globMatch(r.pattern, rel)
}

// globMatch matches a gitignore glob against a slash-separated path. `**`
// components match zero or more directories; every other component is
// matched with filepath.Match, which never lets `*`/`?`/`[..]` cross a `/`.
func globMatch(pattern, path string) bool {
	return globComponents(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func globComponents(pat, comps []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Zero or more directories: try every split point.
			rest := pat[1:]
			if len(rest) == 0 {
				// A trailing `/**` matches everything INSIDE the directory,
				// not the directory itself (gitignore(5)).
				return len(comps) > 0
			}
			for i := 0; i <= len(comps); i++ {
				if globComponents(rest, comps[i:]) {
					return true
				}
			}
			return false
		}
		if len(comps) == 0 {
			return false
		}
		ok, err := filepath.Match(pat[0], comps[0])
		if err != nil || !ok {
			return false
		}
		pat, comps = pat[1:], comps[1:]
	}
	return len(comps) == 0
}

// ignoreFilesFor lists the gitignore-format files that apply to rel (a
// repository-relative path), in git's precedence order: .git/info/exclude,
// the root .gitignore, then each nested .gitignore from the root down to the
// directory holding rel. Later files win (their rules are appended last).
func ignoreFilesFor(root, rel string) []ignoreRule {
	var rules []ignoreRule
	rules = append(rules, parseIgnoreFile(filepath.Join(root, ".git", "info", "exclude"), "")...)
	rules = append(rules, parseIgnoreFile(filepath.Join(root, ".gitignore"), "")...)
	dir := filepath.Dir(rel)
	if dir == "." {
		return rules
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		base := strings.Join(parts[:i+1], "/")
		rules = append(rules, parseIgnoreFile(filepath.Join(root, base, ".gitignore"), base)...)
	}
	return rules
}

// Ignored reports whether abs — an absolute path — is ignored by the
// repository rooted at root, per the subset documented at the top of this
// file. A path outside root, or root itself, is never ignored. A path is
// ignored when the last matching rule for it OR for any ancestor directory
// below root is a non-negated one, evaluating each ancestor from the root
// down (an excluded parent cannot be re-included by a child negation).
func Ignored(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	rel = filepath.ToSlash(rel)
	comps := strings.Split(rel, "/")
	for i := range comps {
		prefix := strings.Join(comps[:i+1], "/")
		isDir := i < len(comps)-1 // an ancestor is a directory by construction
		if !isDir {
			if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(prefix))); err == nil && info.IsDir() {
				isDir = true
			}
		}
		if lastMatchIgnores(ignoreFilesFor(root, prefix), prefix, isDir) {
			return true
		}
	}
	return false
}

// lastMatchIgnores applies rules in order and reports whether the LAST match
// is a non-negated rule.
func lastMatchIgnores(rules []ignoreRule, rel string, isDir bool) bool {
	ignored := false
	for _, r := range rules {
		if r.matches(rel, isDir) {
			ignored = !r.negate
		}
	}
	return ignored
}
