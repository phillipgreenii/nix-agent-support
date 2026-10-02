package report

import "strings"

// QuoteArgv renders argv for display and for eval: elements made only of safe
// characters stay bare; every other element (including the empty string) is
// wrapped in POSIX single quotes, with each embedded ' written as '\”. The
// result round-trips through `eval set -- "$(...)"` to the exact argv.
func QuoteArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = quoteWord(a)
	}
	return strings.Join(parts, " ")
}

func quoteWord(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !isSafe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isSafe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("_@%+=:,./-", r)
}
