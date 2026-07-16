// Package secretpath recognizes well-known credential/secret file paths so the
// rule chain can prompt (Ask) before an agent reads them, rather than silently
// auto-approving (pg2-to8pe). Matching is purely lexical on the path string —
// it never touches the filesystem — so it works uniformly for Bash arguments
// and for the Read/Grep/Glob/Edit tool paths.
package secretpath

import "strings"

// secretDirs are path components whose presence anywhere in a path marks the
// whole path as secret (e.g. "secrets/prod.yaml", "~/.ssh/id_rsa").
var secretDirs = map[string]bool{
	"secrets": true,
	".ssh":    true,
}

// nonSecretDotEnv are .env.* variants that are conventionally committed and
// carry no real secrets, so they must NOT match.
var nonSecretDotEnv = map[string]bool{
	".env.example":  true,
	".env.sample":   true,
	".env.template": true,
	".env.dist":     true,
}

// IsSecret reports whether path points at a well-known credential/secret file.
// It recognizes, by default (no configuration required):
//   - the Claude credential basenames ".credentials", ".credentials.json"
//     (the Linux OAuth file) and "auth.json";
//   - any path under a "secrets/" or ".ssh/" directory component;
//   - ".env" and ".env.*" (excluding conventional non-secret variants such as
//     ".env.example");
//   - any "*token*.json" basename (case-insensitive).
//
// Directory-component matching (secrets/.ssh) fires only when path actually
// looks like a path (contains a "/"), so a bare word like the `secrets`
// argument of `kubectl get secrets` is not misread as a secret path.
func IsSecret(path string) bool {
	if path == "" {
		return false
	}
	hasSeparator := strings.Contains(path, "/")
	base := ""
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			continue
		}
		if hasSeparator && secretDirs[part] {
			return true
		}
		base = part
	}
	return isSecretBasename(base)
}

func isSecretBasename(base string) bool {
	switch base {
	case ".credentials", ".credentials.json", "auth.json":
		return true
	}
	if isSecretDotEnv(base) {
		return true
	}
	// *token*.json — API/OAuth token dumps (case-insensitive).
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, ".json") && strings.Contains(lower, "token") {
		return true
	}
	return false
}

func isSecretDotEnv(base string) bool {
	if base == ".env" {
		return true
	}
	if !strings.HasPrefix(base, ".env.") {
		return false
	}
	return !nonSecretDotEnv[base]
}
