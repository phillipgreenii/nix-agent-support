package transcript

import (
	"fmt"
	"path/filepath"
	"testing"
)

// genTranscript builds an n-line transcript alternating user/assistant events
// with realistic per-line size, ending in a newline.
func genTranscript(n int) string {
	var b []byte
	for i := range n {
		var line string
		if i%2 == 0 {
			line = fmt.Sprintf(`{"type":"user","uuid":"u%d","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"message number %d with some filler text to look realistic"}}`, i, i)
		} else {
			line = fmt.Sprintf(`{"type":"assistant","uuid":"a%d","timestamp":"2026-04-23T10:00:05Z","message":{"model":"claude-opus-4-7","role":"assistant","content":[{"type":"text","text":"reply %d"}],"usage":{"input_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":%d,"output_tokens":%d}}}`, i, i, i, i*7, i*3)
		}
		b = append(b, line...)
		b = append(b, '\n')
	}
	return string(b)
}

// BenchmarkFullScan measures the per-tick cost the daemon paid BEFORE this
// change: every poll of an active session fully re-parsed the whole transcript.
func BenchmarkFullScan(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("lines=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			path := filepath.Join(dir, "t.jsonl")
			if err := writeTestFile(path, genTranscript(n)); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := Scan(path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkIncrementalUnchanged measures the per-tick cost AFTER this change for
// a session whose transcript did not grow since the last poll — the common case
// across many alive sessions. It is independent of transcript size.
func BenchmarkIncrementalUnchanged(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("lines=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			path := filepath.Join(dir, "t.jsonl")
			if err := writeTestFile(path, genTranscript(n)); err != nil {
				b.Fatal(err)
			}
			_, acc, err := ScanIncremental(path, nil) // warm to n lines
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, acc, err = ScanIncremental(path, acc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
