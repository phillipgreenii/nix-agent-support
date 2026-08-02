package primarycommit

import "strings"

// GIT-ALIAS RESOLUTION (tc-2phi8)
//
// A git alias can HIDE a guarded subcommand from the static parser: with
// `git -c alias.p='push origin HEAD:main' p`, GitInvocation returns subcmd "p",
// so both primary-push and primary-commit — and the generic git rule — Abstain,
// and an auto-approving session silently accepts the push. Aliases come from two
// places the file-based resolver can see WITHOUT shelling out to git: `-c
// alias.<name>=<body>` injected on the command line (InjectedAliases, below) and
// the `[alias]` section of local/global config (FileResolver.Aliases). Each rule
// merges the two (injected overriding, matching git's `-c` precedence) and calls
// ResolveGitAlias once to expand the subcommand before gating on it.
//
// These helpers are EXPORTED from primary-commit so primary-push can reuse them
// (it already imports this package for PrimaryResolver), keeping one alias
// implementation rather than a drifting copy per rule.

// InjectedAliases extracts `-c alias.<name>=<body>` definitions from the raw git
// args (the slice AFTER the `git` executable). Keys are git-config variable names,
// which are case-insensitive, so alias names are lowered — matching ResolveGitAlias'
// lowered lookup. Returns nil when no alias is injected.
func InjectedAliases(args []string) map[string]string {
	var out map[string]string
	for i := 0; i < len(args); i++ {
		if args[i] != "-c" || i+1 >= len(args) {
			continue
		}
		kv := args[i+1]
		i++ // consume the config token so a following `-c` is not misread
		eq := strings.Index(kv, "=")
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[:eq]))
		const pfx = "alias."
		if !strings.HasPrefix(key, pfx) || len(key) == len(pfx) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		// The command parser does NOT strip quotes from a partially-quoted token, so a
		// value written `alias.p='push origin HEAD:main'` arrives WITH its wrapping
		// quotes; a real shell would have removed them before git saw the config value.
		// Shell-unquote here so the stored body matches what git receives (and so the
		// body's inner spaces split normally in splitAliasBody).
		out[key[len(pfx):]] = shellUnquote(kv[eq+1:])
	}
	return out
}

// shellUnquote removes shell single/double quote delimiters from s, concatenating the
// pieces (bash-style): `'push origin HEAD:main'` -> `push origin HEAD:main`, and an
// unquoted string is returned unchanged. It intentionally does not interpret backslash
// escapes or `$`-expansions — an alias body is a fixed config value, not live shell.
func shellUnquote(s string) string {
	if !strings.ContainsAny(s, "'\"") {
		return s
	}
	var b strings.Builder
	var quote byte // 0 (none), '\'' or '"'
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				b.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ResolveGitAlias expands subcmd ONCE if it names an alias. It returns the effective
// subcommand and effective rest args (the alias body tokens, then the caller's
// post-alias args appended — the order git uses), and a non-empty shellBody when the
// alias is a SHELL alias (`!...`): a shell body cannot be parsed as a git subcommand,
// so it is returned verbatim (leading `!` stripped) for the caller to handle
// conservatively, and effSubcmd/effRest are meaningless in that case.
//
// Expansion is SINGLE-PASS: git does not re-expand an alias's first word into another
// alias, and not looping also bounds the work. Lookup is case-insensitive on the alias
// name (git config keys are).
func ResolveGitAlias(subcmd string, rest []string, aliases map[string]string) (effSubcmd string, effRest []string, shellBody string) {
	body, ok := aliases[strings.ToLower(subcmd)]
	if !ok || body == "" {
		return subcmd, rest, ""
	}
	if strings.HasPrefix(body, "!") {
		return "", nil, strings.TrimPrefix(body, "!")
	}
	tokens := splitAliasBody(body)
	if len(tokens) == 0 {
		return subcmd, rest, ""
	}
	effRest = append(append([]string{}, tokens[1:]...), rest...)
	return tokens[0], effRest, ""
}

// splitAliasBody tokenizes a git alias body on whitespace, honoring single and double
// quotes (git splits alias bodies with a shell-like word split). It is deliberately
// small: an alias body is a stored config value, already free of the shell operators
// (`&&`, `|`, `$(...)`) the full command parser must handle, so a quote-aware space
// split is sufficient and keeps this subprocess-free like the rest of FileResolver.
func splitAliasBody(body string) []string {
	var tokens []string
	var buf strings.Builder
	var quote byte // 0 (none), '\'' or '"'
	flush := func() {
		if buf.Len() > 0 {
			tokens = append(tokens, buf.String())
			buf.Reset()
		}
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				buf.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ' ' || c == '\t':
			flush()
		default:
			buf.WriteByte(c)
		}
	}
	flush()
	return tokens
}

// MergeAliases merges config-defined aliases with command-line-injected ones, injected
// overriding config (git: `-c` beats config). Returns nil when both are empty.
func MergeAliases(config, injected map[string]string) map[string]string {
	if len(config) == 0 {
		return injected
	}
	if len(injected) == 0 {
		return config
	}
	out := make(map[string]string, len(config)+len(injected))
	for k, v := range config {
		out[k] = v
	}
	for k, v := range injected {
		out[k] = v
	}
	return out
}
