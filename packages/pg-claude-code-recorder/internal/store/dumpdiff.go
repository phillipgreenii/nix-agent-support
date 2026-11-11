package store

import (
	"fmt"
	"strings"
)

// maxDumpDiffLines bounds how many differing lines DumpDiff names, and
// maxDumpDiffLine bounds the length of each rendered line.
const (
	maxDumpDiffLines = 6
	maxDumpDiffLine  = 200
)

// DumpDiff names what differs between two Dump strings: for each table (a
// "[name]" header line) the first differing lines of the replay (a) and the
// index (b), keyed by the line's leading column. It returns "" when the dumps
// are equal. The result is bounded in size.
func DumpDiff(a, b string) string {
	if a == b {
		return ""
	}
	ta, tb := splitDump(a), splitDump(b)
	var out []string
	add := func(s string) bool {
		if len(out) >= maxDumpDiffLines {
			return false
		}
		out = append(out, s)
		return true
	}
	names := make([]string, 0, len(ta.order))
	seen := map[string]bool{}
	for _, n := range append(append([]string{}, ta.order...), tb.order...) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	more := false
	for _, n := range names {
		la, lb := ta.lines[n], tb.lines[n]
		ma, mb := lineSet(la), lineSet(lb)
		for _, l := range la {
			if !mb[l] {
				if !add(fmt.Sprintf("%s: replay has %s, index lacks it", n, clip(l))) {
					more = true
				}
			}
		}
		for _, l := range lb {
			if !ma[l] {
				if !add(fmt.Sprintf("%s: index has %s, replay lacks it", n, clip(l))) {
					more = true
				}
			}
		}
	}
	if len(out) == 0 {
		return "dumps differ in layout only"
	}
	s := strings.Join(out, "; ")
	if more {
		s += "; ..."
	}
	return s
}

type splitDumpResult struct {
	order []string
	lines map[string][]string
}

func splitDump(d string) splitDumpResult {
	r := splitDumpResult{lines: map[string][]string{}}
	cur := ""
	for _, l := range strings.Split(d, "\n") {
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			cur = l[1 : len(l)-1]
			r.order = append(r.order, cur)
			continue
		}
		if l != "" && cur != "" {
			r.lines[cur] = append(r.lines[cur], l)
		}
	}
	return r
}

func lineSet(ls []string) map[string]bool {
	m := make(map[string]bool, len(ls))
	for _, l := range ls {
		m[l] = true
	}
	return m
}

func clip(l string) string {
	if len(l) > maxDumpDiffLine {
		return l[:maxDumpDiffLine] + "..."
	}
	return l
}
