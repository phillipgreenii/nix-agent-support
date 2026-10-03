package runner

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLimitTail(t *testing.T) {
	lines := func(from, to int) string {
		var b strings.Builder
		for i := from; i <= to; i++ {
			b.WriteString("l")
			b.WriteString(string(rune('0' + i%10)))
			b.WriteString("\n")
		}
		return b.String()
	}
	cases := []struct {
		name      string
		in        string
		lines     int
		bytes     int
		lineStart bool
		want      string
	}{
		{"empty", "", 3, 100, true, ""},
		{"fewer lines than the limit", "a\nb\n", 3, 100, true, "a\nb\n"},
		{"exactly the limit", "a\nb\nc\n", 3, 100, true, "a\nb\nc\n"},
		{"one over the limit", "a\nb\nc\nd\n", 3, 100, true, "b\nc\nd\n"},
		{"no final newline counts the last line", "a\nb\nc\nd", 3, 100, true, "b\nc\nd"},
		{"a lone newline is one empty line", "\n", 3, 100, true, "\n"},
		{"blank lines count", "a\n\n\n\nb\n", 3, 100, true, "\n\nb\n"},
		{"partial first line dropped", "tial\nb\nc\n", 5, 100, false, "b\nc\n"},
		{"partial first line kept when it is all there is", "tial", 5, 100, false, "tial"},
		{"byte cap drops whole lines", "aaaa\nbbbb\ncccc\n", 10, 10, true, "bbbb\ncccc\n"},
		{"byte cap exactly met", "aaaa\nbbbb\ncccc\n", 10, 15, true, "aaaa\nbbbb\ncccc\n"},
		{"one under the byte cap drops a line", "aaaa\nbbbb\ncccc\n", 10, 9, true, "cccc\n"},
		{"one over the byte cap", "aaaa\nbbbb\ncccc\n", 10, 14, true, "bbbb\ncccc\n"},
		{"an over-long single line is cut from the front", "0123456789", 10, 4, true, "6789"},
		{"an over-long line with newline keeps the newline", "0123456789\n", 10, 4, true, "789\n"},
		{"the cut never splits a rune", "aé€\n", 10, 5, true, "€\n"},
		{"line limit then byte limit", lines(1, 50), 10, 12, true, lines(47, 50)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LimitTail(c.in, c.lines, c.bytes, c.lineStart)
			if got != c.want {
				t.Errorf("LimitTail(%q, %d, %d, %v) = %q; want %q", c.in, c.lines, c.bytes, c.lineStart, got, c.want)
			}
			if len(got) > c.bytes || !utf8.ValidString(got) {
				t.Errorf("result breaks the caps: %q", got)
			}
		})
	}
}

func TestRedactorReplacesEveryMatchInEveryPattern(t *testing.T) {
	r := newRedactor([]*regexp.Regexp{regexp.MustCompile(`sk-[a-z]+`), regexp.MustCompile(`Bearer \S+`)})
	if got := r.apply("a sk-abc b sk-def Bearer xyz.z c"); got != "a [REDACTED] b [REDACTED] [REDACTED] c" {
		t.Errorf("got %q", got)
	}
	if got := newRedactor(nil).apply("sk-abc"); got != "sk-abc" {
		t.Errorf("no patterns must change nothing: %q", got)
	}
	// "$1" in the text is data, not a replacement template.
	r2 := newRedactor([]*regexp.Regexp{regexp.MustCompile(`(x)`)})
	if got := r2.apply("x"); got != "[REDACTED]" {
		t.Errorf("got %q", got)
	}
}

func TestTruncateBytes(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{{"abc", 5, "abc"}, {"abc", 3, "abc"}, {"abc", 2, "ab"}, {"aéb", 2, "a"}, {"aéb", 3, "aé"}, {"€", 2, ""}, {"", 0, ""}} {
		if got := truncateBytes(c.in, c.max); got != c.want {
			t.Errorf("truncateBytes(%q, %d) = %q; want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestWriteFileAtomicReplacesASymlinkAndLeavesNoTemporaries(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	_ = os.WriteFile(target, []byte("sentinel"), 0o600)
	p := filepath.Join(dir, "report.json")
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "sentinel" {
		t.Errorf("wrote through the symlink: %q", b)
	}
	fi, _ := os.Lstat(p)
	if b, _ := os.ReadFile(p); string(b) != "new" || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("report = %q mode=%v", b, fi.Mode())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Errorf("leftover temporaries: %v", ents)
	}
	if err := writeFileAtomic(filepath.Join(dir, "no-such-dir", "x"), nil); err == nil {
		t.Error("writing into a missing directory must fail")
	}
}

func TestWriteAttemptFileNeverFollowsOrOverwrites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a")
	if err := writeAttemptFile(p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writeAttemptFile(p, []byte("y")); err == nil {
		t.Error("an existing file must not be overwritten")
	}
}

func TestFormatDurationAndSignalName(t *testing.T) {
	for in, want := range map[string]string{"10m0s": "10m", "90s": "90s", "1h0m0s": "1h", "200ms": "200ms", "1m30s": "90s", "2s": "2s", "1h30m0s": "90m"} {
		d, _ := parseDur(in)
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%s) = %q; want %q", in, got, want)
		}
	}
}

func TestLimitTailDropsAnEmptyPartialFirstLine(t *testing.T) {
	// The cut fell exactly on a newline: the "partial" line is empty and goes.
	if got := LimitTail("\nb\nc\n", 10, 100, false); got != "b\nc\n" {
		t.Errorf("got %q", got)
	}
	if got := LimitTail("\nb\nc\n", 10, 100, true); got != "\nb\nc\n" {
		t.Errorf("got %q", got)
	}
}

func TestFormatDurationPicksTheLargestExactUnit(t *testing.T) {
	for in, want := range map[string]string{"2h": "2h", "2m": "2m", "3s": "3s", "1m1s": "61s", "1h1m": "61m", "1500ms": "1.5s"} {
		d, _ := parseDur(in)
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%s) = %q; want %q", in, got, want)
		}
	}
}
