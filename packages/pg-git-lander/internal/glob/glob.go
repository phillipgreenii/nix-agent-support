// Package glob is the repo-relative path glob matcher shared by the config
// check and the conflict resolver.
package glob

import (
	"path"
	"strings"
)

// Match reports whether the repo-relative path p matches glob: "*"
// matches within one path segment and "**" across segments (zero or more).
func Match(glob, p string) bool {
	return matchSegs(strings.Split(glob, "/"), strings.Split(p, "/"))
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegs(rest, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
