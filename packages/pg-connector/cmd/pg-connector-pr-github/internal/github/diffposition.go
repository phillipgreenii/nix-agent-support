package github

import (
	"regexp"
	"strconv"
	"strings"
)

// DiffPositions maps the lines of one file's patch to the diff POSITION the
// host's position-based review comment takes (INV-REVHEAD-2).
//
// The position is the 1-based index of a diff line within the file's FULL
// patch, counted from the line after the file's first "@@" header: that line
// is 1, every LATER "@@" header line and every "\ No newline at end of file"
// line also consumes one position, and the count never restarts between hunks.
type DiffPositions struct {
	// right maps a new-file line (context or added) to its position; left maps
	// an old-file line (context or removed).
	right, left map[int]int
	// bad is set when the patch could not be read (a hunk header that does not
	// parse); nothing resolves then, so a comment fails closed rather than
	// landing a line away.
	bad bool
}

var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// ParsePatchPositions reads one file's patch text (as the host's compare read
// returns it: starting at the first "@@" header, without file headers).
func ParsePatchPositions(patch string) *DiffPositions {
	d := &DiffPositions{right: map[int]int{}, left: map[int]int{}}
	lines := strings.Split(patch, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var oldLine, newLine, pos int
	seenHunk := false
	for _, l := range lines {
		if strings.HasPrefix(l, "@@") {
			m := hunkHeaderRE.FindStringSubmatch(l)
			if m == nil {
				d.bad = true
				return d
			}
			oldLine, _ = strconv.Atoi(m[1])
			newLine, _ = strconv.Atoi(m[2])
			if seenHunk {
				pos++
			}
			seenHunk = true
			continue
		}
		if !seenHunk {
			// File headers a caller left on the text; they are not diff lines.
			continue
		}
		pos++
		switch {
		case strings.HasPrefix(l, "\\"):
			// "\ No newline at end of file": consumes a position, maps no line.
		case strings.HasPrefix(l, "+"):
			d.right[newLine] = pos
			newLine++
		case strings.HasPrefix(l, "-"):
			d.left[oldLine] = pos
			oldLine++
		default:
			// A context line (" text", or an empty line whose space was stripped).
			d.right[newLine] = pos
			d.left[oldLine] = pos
			newLine++
			oldLine++
		}
	}
	return d
}

// Position resolves line on side ("LEFT" or "RIGHT", case-insensitive; "" is
// RIGHT) to a diff position. ok is false when the line is not part of the
// patch (outside every hunk, or a line the side does not have).
func (d *DiffPositions) Position(side string, line int) (int, bool) {
	if d == nil || d.bad {
		return 0, false
	}
	m := d.right
	if strings.EqualFold(strings.TrimSpace(side), "LEFT") {
		m = d.left
	}
	pos, ok := m[line]
	return pos, ok
}
