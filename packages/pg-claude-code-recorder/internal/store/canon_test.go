package store

import (
	"context"
	"reflect"
	"testing"
)

// Known values map to one canonical string, unknown values pass through.
func TestCanonicalizersKnownAndUnknown(t *testing.T) {
	cases := []struct {
		name string
		fn   func(string) string
		in   string
	}{
		{"source transcript", canonSource, "claude-transcript"},
		{"source body", canonSource, "claude-tool-result-body"},
		{"source registry", canonSource, "session-registry"},
		{"source unknown", canonSource, "something-else"},
		{"source empty", canonSource, ""},
		{"mode append", canonMode, "append"},
		{"mode snapshot", canonMode, "snapshot"},
		{"mode link", canonMode, "link"},
		{"mode unknown", canonMode, "weird"},
		{"state ok", canonState, "ok"},
		{"state copy_mismatch", canonState, "copy_mismatch"},
		{"state deleted", canonState, "deleted"},
		{"state unknown", canonState, "limbo"},
	}
	for _, c := range cases {
		// A fresh copy so equality cannot rely on a shared literal.
		in := string(append([]byte(nil), c.in...))
		if got := c.fn(in); got != c.in {
			t.Errorf("%s: %q became %q", c.name, c.in, got)
		}
	}
}

// The scan returns exactly what the previous (non-canonicalizing) scan did.
func TestScanFileRoundTripEqualsPlainScan(t *testing.T) {
	s, _ := openCounting(t, Options{})
	for i := 0; i < 4; i++ {
		seed(t, s)
	}
	got, err := s.AllFiles()
	if err != nil || len(got) != 4 {
		t.Fatalf("AllFiles: %d rows, %v", len(got), err)
	}
	rows, err := s.rdb.QueryContext(context.Background(), `SELECT `+fileCols+` FROM files ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var want []File
	for rows.Next() {
		var f File
		var dev, ino, fh int64
		if err := rows.Scan(&f.ID, &f.Source, &f.Mode, &dev, &ino, &f.Birth, &fh, &f.Link, &f.State, &f.FirstSeen, &f.LastSize, &f.CurGen, &f.DeletedNS); err != nil {
			t.Fatal(err)
		}
		f.Dev, f.Ino, f.FirstHash = uint64(dev), uint64(ino), uint64(fh)
		want = append(want, f)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical scan differs from plain scan:\n got %+v\nwant %+v", got, want)
	}
	// A second call uses the row-count hint and returns the same rows.
	again, err := s.AllFiles()
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("second AllFiles differs: %v", err)
	}
}

// BenchmarkAllFiles reports the allocations of the file-row scan (run with
// `go test -bench AllFiles -benchmem ./internal/store/`).
func BenchmarkAllFiles(b *testing.B) {
	s, err := Open(b.TempDir(), Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	const n = 5000
	recs := make([]*Rec, 0, n)
	for i := 0; i < n; i++ {
		recs = append(recs, &Rec{T: TFile, At: 10, File: s.NewFileID(), Source: "claude-transcript", Mode: "append", Dev: 1, Ino: uint64(i + 1), Birth: 3})
	}
	if err := s.Commit(recs...); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fs, err := s.AllFiles()
		if err != nil || len(fs) != n {
			b.Fatalf("%d rows, %v", len(fs), err)
		}
	}
}
