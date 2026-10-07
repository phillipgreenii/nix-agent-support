package github

import (
	"strings"
	"testing"
)

// patch joins diff lines the way the host returns a file's patch: from the
// first "@@" header on, newline separated, no trailing newline.
func patch(lines ...string) string { return strings.Join(lines, "\n") }

func wantPos(t *testing.T, d *DiffPositions, side string, line, want int) {
	t.Helper()
	got, ok := d.Position(side, line)
	if want == 0 {
		if ok {
			t.Errorf("%s %d resolved to position %d, want no position", side, line, got)
		}
		return
	}
	if !ok || got != want {
		t.Errorf("%s %d = (%d, %v), want %d", side, line, got, ok, want)
	}
}

// TestParsePatchPositions_AddedFile: a new file is one hunk of added lines;
// the first line after the header is position 1 and there is no LEFT side.
func TestParsePatchPositions_AddedFile(t *testing.T) {
	d := ParsePatchPositions(patch("@@ -0,0 +1,3 @@", "+a", "+b", "+c"))
	wantPos(t, d, "RIGHT", 1, 1)
	wantPos(t, d, "RIGHT", 3, 3)
	wantPos(t, d, "RIGHT", 4, 0)
	wantPos(t, d, "LEFT", 1, 0)
	wantPos(t, d, "", 2, 2) // an empty side is RIGHT
}

// TestParsePatchPositions_TwoHunks: the position keeps counting across the
// second "@@" line, which itself consumes one position.
func TestParsePatchPositions_TwoHunks(t *testing.T) {
	d := ParsePatchPositions(patch(
		"@@ -1,4 +1,5 @@",   // not counted
		" package main",     // 1  old 1 / new 1
		" ",                 // 2  old 2 / new 2
		"-func old() {}",    // 3  old 3
		"+func new() {}",    // 4  new 3
		"+func extra() {}",  // 5 new 4
		" var x = 1",        // 6  old 4 / new 5
		"@@ -20,3 +21,3 @@", // 7 (a later header consumes a position)
		" a",                // 8  old 20 / new 21
		"-b",                // 9  old 21
		"+c",                // 10 new 22
		" d",                // 11 old 22 / new 23
	))
	wantPos(t, d, "RIGHT", 1, 1)
	wantPos(t, d, "RIGHT", 5, 6)
	wantPos(t, d, "LEFT", 4, 6)
	// The line right after the hunk boundary: counts the second header.
	wantPos(t, d, "RIGHT", 21, 8)
	wantPos(t, d, "LEFT", 20, 8)
	wantPos(t, d, "RIGHT", 22, 10)
	wantPos(t, d, "RIGHT", 23, 11)
	// A line between the hunks is not in the diff.
	wantPos(t, d, "RIGHT", 10, 0)
	wantPos(t, d, "LEFT", 10, 0)
}

// TestParsePatchPositions_Sides: RIGHT never resolves a removed line and LEFT
// never resolves an added one; both resolve a context line to the same position.
func TestParsePatchPositions_Sides(t *testing.T) {
	d := ParsePatchPositions(patch("@@ -5,3 +5,3 @@", " keep", "-gone", "+came", " tail"))
	wantPos(t, d, "LEFT", 6, 2)  // the deleted line
	wantPos(t, d, "RIGHT", 6, 3) // its replacement
	wantPos(t, d, "left", 6, 2)  // case-insensitive
	wantPos(t, d, "RIGHT", 5, 1)
	wantPos(t, d, "LEFT", 5, 1)
	wantPos(t, d, "RIGHT", 7, 4)
	wantPos(t, d, "LEFT", 7, 4)
	wantPos(t, d, "LEFT", 8, 0)
}

// TestParsePatchPositions_NoNewlineMarker: "\ No newline at end of file"
// consumes a position, so the lines after it are shifted by one.
func TestParsePatchPositions_NoNewlineMarker(t *testing.T) {
	d := ParsePatchPositions(patch(
		"@@ -1,2 +1,2 @@",
		" a",                           // 1
		"-b",                           // 2  old 2
		"\\ No newline at end of file", // 3
		"+c",                           // 4  new 2
		"\\ No newline at end of file", // 5
		"@@ -9,1 +9,1 @@",              // 6
		" z",                           // 7  old 9 / new 9
	))
	wantPos(t, d, "LEFT", 2, 2)
	wantPos(t, d, "RIGHT", 2, 4)
	wantPos(t, d, "RIGHT", 9, 7)
}

// TestParsePatchPositions_HeaderForms: counts are optional in a hunk header
// and a trailing section heading is ignored.
func TestParsePatchPositions_HeaderForms(t *testing.T) {
	d := ParsePatchPositions(patch("@@ -3 +3 @@ func f() {", "-x", "+y"))
	wantPos(t, d, "LEFT", 3, 1)
	wantPos(t, d, "RIGHT", 3, 2)
}

// TestParsePatchPositions_TrailingNewlineAndFileHeaders: a trailing newline
// adds no line, and file headers before the first hunk are not diff lines.
func TestParsePatchPositions_TrailingNewlineAndFileHeaders(t *testing.T) {
	d := ParsePatchPositions("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,1 +1,2 @@\n a\n+b\n")
	wantPos(t, d, "RIGHT", 1, 1)
	wantPos(t, d, "RIGHT", 2, 2)
	wantPos(t, d, "RIGHT", 3, 0)
}

// TestParsePatchPositions_Unreadable: a patch with no hunk header, or a
// header that does not parse, resolves nothing (fail closed).
func TestParsePatchPositions_Unreadable(t *testing.T) {
	for name, p := range map[string]string{
		"empty":         "",
		"no hunk":       "Binary files differ",
		"broken header": "@@ nonsense @@\n a\n",
	} {
		d := ParsePatchPositions(p)
		wantPos(t, d, "RIGHT", 1, 0)
		wantPos(t, d, "LEFT", 1, 0)
		_ = name
	}
	var nilPositions *DiffPositions
	if _, ok := nilPositions.Position("RIGHT", 1); ok {
		t.Error("a nil DiffPositions must resolve nothing")
	}
}

func TestResolveAnchor(t *testing.T) {
	files := []ComparedFile{
		{Path: "main.go", Patch: patch("@@ -1,2 +1,3 @@", " a", "+b", " c")},
		{Path: "big.go"}, // patch omitted by the host
		{Path: "new_name.go", PreviousPath: "old_name.go", Patch: patch("@@ -1,2 +1,2 @@", "-x", "+y", " z")},
		{Path: "pure_rename.go", PreviousPath: "was.go"}, // rename without content change
	}
	cases := []struct {
		name       string
		path, side string
		line       int
		wantPath   string
		wantPos    int
		wantOK     bool
	}{
		{"added line", "main.go", "RIGHT", 2, "main.go", 2, true},
		{"line not in the diff", "main.go", "RIGHT", 50, "", 0, false},
		{"file not in the difference", "other.go", "RIGHT", 1, "", 0, false},
		{"omitted patch", "big.go", "RIGHT", 1, "", 0, false},
		{"binary or rename-only has no patch", "pure_rename.go", "RIGHT", 1, "", 0, false},
		{"RIGHT under the new path", "new_name.go", "RIGHT", 1, "new_name.go", 2, true},
		{"LEFT under the old path of a rename", "old_name.go", "LEFT", 1, "new_name.go", 1, true},
		{"LEFT under the new path of a rename", "new_name.go", "LEFT", 1, "new_name.go", 1, true},
		{"RIGHT under the old path is not the file", "old_name.go", "RIGHT", 1, "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, pos, ok := ResolveAnchor(files, c.path, c.side, c.line)
			if path != c.wantPath || pos != c.wantPos || ok != c.wantOK {
				t.Errorf("ResolveAnchor = (%q, %d, %v), want (%q, %d, %v)", path, pos, ok, c.wantPath, c.wantPos, c.wantOK)
			}
		})
	}
}
