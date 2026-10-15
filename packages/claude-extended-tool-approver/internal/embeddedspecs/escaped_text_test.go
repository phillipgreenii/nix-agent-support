package embeddedspecs

import (
	"encoding/json"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// literalUnicodeEscape matches a backslash-u00XX sequence in a DECODED string:
// the shape of text that was JSON-escaped twice (a "<" that became the six
// characters backslash,u,0,0,3,c after one decode). Real citation text quotes
// a tool's --help output verbatim, and none of the built-in specs legitimately
// contains this sequence (pg2-7cnzc).
var literalUnicodeEscape = regexp.MustCompile(`\\u00[0-9a-fA-F]{2}`)

// findLiteralUnicodeEscapes decodes raw as JSON and returns one entry per
// string value or object key (addressed by JSON path) whose DECODED text
// contains a literal backslash-u00XX sequence.
func findLiteralUnicodeEscapes(t *testing.T, raw []byte) []string {
	t.Helper()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var bad []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case string:
			if m := literalUnicodeEscape.FindString(x); m != "" {
				bad = append(bad, path+": contains "+m+" in "+x)
			}
		case []any:
			for i, e := range x {
				walk(path+"["+strconv.Itoa(i)+"]", e)
			}
		case map[string]any:
			for k, e := range x {
				if m := literalUnicodeEscape.FindString(k); m != "" {
					bad = append(bad, path+": key contains "+m+" in "+k)
				}
				walk(path+"."+k, e)
			}
		}
	}
	walk("$", doc)
	return bad
}

// TestEmbeddedSpecsHaveNoDoubleEscapedText guards the shipped data: no decoded
// string in any data/*.json may hold a literal backslash-u00XX sequence. A
// back-fill once left 15 files with "\\u003c"/"\\u003e"/"\\u0026" where the
// citation text should carry "<", ">" and "&" (pg2-7cnzc); genspecs preserves
// prior citations verbatim, so nothing but this guard would catch a recurrence.
func TestEmbeddedSpecsHaveNoDoubleEscapedText(t *testing.T) {
	entries, err := fs.ReadDir(FS, "data")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := fs.ReadFile(FS, "data/"+e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		checked++
		for _, b := range findLiteralUnicodeEscapes(t, raw) {
			t.Errorf("%s: %s", e.Name(), b)
		}
	}
	if checked == 0 {
		t.Fatal("no data/*.json files checked")
	}
}

// TestFindLiteralUnicodeEscapesDetectsDoubleEscaping proves the guard bites: a
// double-escaped value and key are reported, while a single-escaped one (which
// decodes to a plain "<") and plain text are not.
func TestFindLiteralUnicodeEscapesDetectsDoubleEscaping(t *testing.T) {
	double := []byte(`{"a":{"source":"pgrep [options] \\u003cpattern\\u003e"},"k\\u0026":[1]}`)
	if got := findLiteralUnicodeEscapes(t, double); len(got) != 2 {
		t.Errorf("double-escaped fixture: got %d findings %v, want 2 (value and key)", len(got), got)
	}
	clean := []byte(`{"a":{"source":"pgrep [options] \u003cpattern\u003e <x> & y"}}`)
	if got := findLiteralUnicodeEscapes(t, clean); len(got) != 0 {
		t.Errorf("single-escaped/plain fixture flagged: %v", got)
	}
}
