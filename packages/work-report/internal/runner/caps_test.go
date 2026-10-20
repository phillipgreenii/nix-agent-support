//go:build unix

package runner

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStdoutCapAcceptsExactlyOneMiBAndRejectsOneMore(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		kind string
	}{
		{"exactly 1 MiB", StdoutCap, KindOK},
		{"1 MiB plus 1 byte", StdoutCap + 1, KindStdoutCap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			s := okScript()
			s.StdoutLen = tc.size // the manifest padded with trailing spaces
			out := fx.run(fx.gen(s))
			requireKind(t, out, tc.kind)
			if tc.kind == KindStdoutCap && !strings.Contains(out.Record.Reason, fmt.Sprint(StdoutCap)) {
				t.Fatalf("reason = %q", out.Record.Reason)
			}
		})
	}
}

func TestRetainedStderrKeepsHeadAndTailOnUTF8Boundaries(t *testing.T) {
	fx := newFixture(t)
	// 3-byte runes: 32768 is not a multiple of 3, so both cuts fall inside a
	// rune and must move to a boundary.
	const runes = 100000
	s := okScript()
	s.StderrRep = &scriptRepeat{Text: "日", Count: runes}
	out := fx.run(fx.gen(s))
	requireOK(t, out)
	total := runes * 3
	if !utf8.ValidString(out.Stderr) {
		t.Fatal("retained stderr is not valid UTF-8")
	}
	head, rest, ok := strings.Cut(out.Stderr, "\n[truncated ")
	if !ok {
		t.Fatalf("no truncation marker in %d bytes of stderr", len(out.Stderr))
	}
	var dropped int
	marker, tail, ok := strings.Cut(rest, " bytes]\n")
	if !ok {
		t.Fatalf("malformed marker %q", rest[:40])
	}
	if _, err := fmt.Sscanf(marker, "%d", &dropped); err != nil {
		t.Fatal(err)
	}
	if len(head) > StderrKeep || len(tail) > StderrKeep || len(head) < StderrKeep-3 || len(tail) < StderrKeep-3 {
		t.Fatalf("kept head %d and tail %d bytes, want about %d each", len(head), len(tail), StderrKeep)
	}
	if got := len(head) + dropped + len(tail); got != total {
		t.Fatalf("head %d + dropped %d + tail %d = %d, want %d", len(head), dropped, len(tail), got, total)
	}
	if strings.Trim(head, "日") != "" || strings.Trim(tail, "日") != "" {
		t.Fatal("head or tail holds something other than the generator's text")
	}
}

func TestTailWriter(t *testing.T) {
	t.Run("short output is kept whole", func(t *testing.T) {
		w := &tailWriter{keep: 8}
		_, _ = w.Write([]byte("hello "))
		_, _ = w.Write([]byte("world"))
		if got := w.String(); got != "hello world" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("exactly two keeps is not truncated", func(t *testing.T) {
		w := &tailWriter{keep: 4}
		_, _ = w.Write([]byte("abcdefgh"))
		if got := w.String(); got != "abcdefgh" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("one byte more is truncated with the byte count", func(t *testing.T) {
		w := &tailWriter{keep: 4}
		_, _ = w.Write([]byte("abcdefghi"))
		if got, want := w.String(), "abcd\n[truncated 1 bytes]\nfghi"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("chunked writes match one write", func(t *testing.T) {
		data := strings.Repeat("0123456789", 50)
		a := &tailWriter{keep: 16}
		_, _ = a.Write([]byte(data))
		b := &tailWriter{keep: 16}
		for i := 0; i < len(data); i += 7 {
			_, _ = b.Write([]byte(data[i:min(i+7, len(data))]))
		}
		if a.String() != b.String() {
			t.Fatalf("chunked %q != whole %q", b.String(), a.String())
		}
	})
	t.Run("cuts inside a rune move to the boundary", func(t *testing.T) {
		w := &tailWriter{keep: 4}
		_, _ = w.Write([]byte("ab日" + "cdef" + "日gh")) // 2+3+4+3+2 = 14 bytes
		got := w.String()
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8: %q", got)
		}
		// head "ab日" would cut at 4 bytes inside the rune: it keeps "ab".
		if !strings.HasPrefix(got, "ab\n[truncated ") || !strings.HasSuffix(got, " bytes]\ngh") {
			t.Fatalf("got %q", got)
		}
	})
}

func TestFileCountAndSizeLimitsAreInvalidManifest(t *testing.T) {
	t.Run("more than 1000 files", func(t *testing.T) {
		fx := newFixture(t)
		paths := make([]string, MaxFiles+1)
		for i := range paths {
			paths[i] = fmt.Sprintf("f%04d.md", i)
		}
		out := fx.run(fx.gen(script{Stdout: okManifest(paths...)}))
		requireKind(t, out, KindInvalidManifest)
		requireRule(t, out, ruleFilesMax, "")
	})
	t.Run("exactly 1000 files is not a count violation", func(t *testing.T) {
		fx := newFixture(t)
		var files []scriptFile
		paths := make([]string, MaxFiles)
		for i := range paths {
			paths[i] = fmt.Sprintf("f%04d.md", i)
			files = append(files, scriptFile{Path: paths[i], Content: "x"})
		}
		out := fx.run(fx.gen(script{Files: files, Stdout: okManifest(paths...)}))
		requireOK(t, out)
	})
	t.Run("a file over 1 GiB", func(t *testing.T) {
		fx := newFixture(t)
		out := fx.run(fx.gen(script{
			Sparse: []scriptSparse{{Path: "big.bin", Size: MaxFileSize + 1}},
			Stdout: okManifest("big.bin"),
		}))
		requireKind(t, out, KindInvalidManifest)
		requireRule(t, out, ruleFileSize, "big.bin")
	})
	t.Run("more than 4 GiB in total", func(t *testing.T) {
		fx := newFixture(t)
		var sp []scriptSparse
		var paths []string
		for i := 0; i < 5; i++ { // 5 x 1 GiB: each file is at the limit, the sum is not
			p := fmt.Sprintf("part%d.bin", i)
			sp = append(sp, scriptSparse{Path: p, Size: MaxFileSize})
			paths = append(paths, p)
		}
		out := fx.run(fx.gen(script{Sparse: sp, Stdout: okManifest(paths...)}))
		requireKind(t, out, KindInvalidManifest)
		requireRule(t, out, ruleTotalSize, "part4.bin")
	})
}
