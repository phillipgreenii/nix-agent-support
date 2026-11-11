package store

import (
	"strings"
	"testing"
)

func TestDumpDiffNamesTableAndRow(t *testing.T) {
	a := "[meta]\nepoch=1\n[files]\n1|a\n2|b\n[paths]\n"
	b := "[meta]\nepoch=1\n[files]\n1|a\n2|c\n[paths]\n"
	got := DumpDiff(a, b)
	if !strings.Contains(got, "files: replay has 2|b") || !strings.Contains(got, "files: index has 2|c") {
		t.Fatalf("got %q", got)
	}
	if DumpDiff(a, a) != "" {
		t.Fatal("equal dumps must give no diff")
	}
}

func TestDumpDiffIsBounded(t *testing.T) {
	var a strings.Builder
	a.WriteString("[files]\n")
	for i := 0; i < 100; i++ {
		a.WriteString(strings.Repeat("x", 500) + "\n")
	}
	got := DumpDiff(a.String(), "[files]\n")
	if len(got) > maxDumpDiffLines*(maxDumpDiffLine+60)+10 || !strings.HasSuffix(got, "...") {
		t.Fatalf("not bounded: %d bytes", len(got))
	}
}
