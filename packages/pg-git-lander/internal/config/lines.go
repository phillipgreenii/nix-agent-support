package config

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	tableHeaderRe = regexp.MustCompile(`^\s*(\[\[?)\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)
	keyLineRe     = regexp.MustCompile(`^\s*([A-Za-z0-9_\-."']+)\s*=`)
)

func splitDotted(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ".") {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// scanLines maps a key path to its 1-based line in src. Paths use the form
// "repo[1].lock.wait" (array-of-tables entries carry their 0-based index).
// A table header is recorded under the table's own path. Keys nested inside
// a multi-line value or an inline table have no entry; lookups fall back to
// the enclosing key (see lineFor).
func scanLines(src []byte) map[string]int {
	lines := map[string]int{}
	counts := map[string]int{}
	cur := ""
	inMulti := false
	for i, raw := range strings.Split(string(src), "\n") {
		n := i + 1
		if inMulti {
			if strings.Contains(raw, `"""`) || strings.Contains(raw, `'''`) {
				inMulti = false
			}
			continue
		}
		if m := tableHeaderRe.FindStringSubmatch(raw); m != nil && !keyLineRe.MatchString(raw) {
			isArr := m[1] == "[["
			path := ""
			segs := splitDotted(m[2])
			for j, s := range segs {
				if path != "" {
					path += "."
				}
				path += s
				last := j == len(segs)-1
				if last && isArr {
					idx := counts[path]
					counts[path] = idx + 1
					path = fmt.Sprintf("%s[%d]", path, idx)
				} else if c, ok := counts[path]; ok {
					path = fmt.Sprintf("%s[%d]", path, c-1)
				}
			}
			cur = path
			lines[cur] = n
			continue
		}
		if m := keyLineRe.FindStringSubmatch(raw); m != nil {
			p := strings.Join(splitDotted(m[1]), ".")
			if cur != "" {
				p = cur + "." + p
			}
			if _, ok := lines[p]; !ok {
				lines[p] = n
			}
			if strings.Count(raw, `"""`) == 1 || strings.Count(raw, `'''`) == 1 {
				inMulti = true
			}
		}
	}
	return lines
}

// lineFor returns the line of path, or of its nearest enclosing key.
func lineFor(lines map[string]int, path string) int {
	for path != "" {
		if l, ok := lines[path]; ok {
			return l
		}
		i := strings.LastIndexAny(path, ".[")
		if i < 0 {
			return 0
		}
		path = path[:i]
	}
	return 0
}
