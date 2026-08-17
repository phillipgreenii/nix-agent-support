package claudetranscript

import (
	"bufio"
	"io"
)

// Claude transcript JSONL lines routinely exceed bufio.Scanner's default
// maximum token size (bufio.MaxScanTokenSize, 64 KiB): a single assistant event
// carrying a large tool_result or a pasted file runs to megabytes on one line.
// Read with a default scanner, Scan() stops at the first such line and Err()
// reports bufio.ErrTooLong ("bufio.Scanner: token too long") — so every
// consumer would silently truncate its read of any large transcript.
//
// How the two sizes below combine is NOT obvious, and getting it wrong is the
// easy mistake here. Per (*bufio.Scanner).Buffer — Buffer sets
// s.buf = buf[0:cap(buf)] and s.maxTokenSize = max, and Scan reports ErrTooLong
// only once len(s.buf) >= s.maxTokenSize — the effective ceiling on one line is
// the LARGER of the two values, not scannerMaxTokenSize alone. So
// scannerInitialBufferSize is a floor as well as an allocation hint: it both
// spares the repeated doubling up from 4 KiB and, on its own, already admits
// lines up to 1 MiB. scannerMaxTokenSize is what lifts the ceiling to 16 MiB.
//
// Both values are asserted by value in scanner_test.go. That assertion is
// deliberate rather than redundant: no behavioural test can observe
// scannerInitialBufferSize at all (shrinking it only makes Scan grow the buffer
// back up to scannerMaxTokenSize), so a direct check is the only thing pinning
// it.
const (
	// scannerInitialBufferSize is the buffer Scan starts with — 1 MiB, so a
	// typical oversized line is read without any regrowth.
	scannerInitialBufferSize = 1024 * 1024
	// scannerMaxTokenSize is the ceiling a single JSONL line may reach — 16 MiB.
	// Beyond it Scan stops and Err() reports bufio.ErrTooLong rather than
	// growing without bound.
	scannerMaxTokenSize = 16 * 1024 * 1024
)

// newTranscriptScanner returns a line scanner sized for the oversized JSONL
// lines real Claude transcripts contain. Every transcript reader in this package
// MUST obtain its scanner here rather than calling bufio.NewScanner directly:
// the sizes are one tuning decision, and duplicating them per reader is what
// let a single missing test disable the protection in six code paths at once.
func newTranscriptScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, scannerInitialBufferSize), scannerMaxTokenSize)
	return sc
}
