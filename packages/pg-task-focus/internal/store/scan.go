// Package store keeps the append-only event log: the strict scan that decides
// which lines of a log are committed (this file), and later the lock, the
// recovery, the append with its rollback and the read-only mode.
package store

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// readBufferSize is the size of the buffered reader. It is also the most the
// scan ever holds beyond one event line: a line is accumulated only up to
// event.MaxEventBytes plus one read chunk.
const readBufferSize = 64 * 1024

// CorruptError reports a log that cannot be read and is not merely torn at its
// tail: the 1-based Line is the first line that proves it, and Cause says why
// in plain words. The store refuses to start on it and never repairs it.
type CorruptError struct {
	Line  int
	Cause error
}

// Error implements error.
func (e *CorruptError) Error() string {
	return fmt.Sprintf("the event log is corrupt at line %d: %v", e.Line, e.Cause)
}

// Unwrap returns the cause.
func (e *CorruptError) Unwrap() error { return e.Cause }

// UnknownVersionError reports a line written in an event version this build
// does not read. It is never a torn tail: the store refuses to start. Unwrap
// returns the *event.UnknownVersionError it wraps, so errors.As finds either
// type.
type UnknownVersionError struct {
	Line, V int
	cause   *event.UnknownVersionError
}

// Error implements error.
func (e *UnknownVersionError) Error() string {
	return fmt.Sprintf("the event log cannot be read at line %d: %v", e.Line, e.Unwrap())
}

// Unwrap returns the event package's error for the version.
func (e *UnknownVersionError) Unwrap() error {
	if e.cause != nil {
		return e.cause
	}
	return &event.UnknownVersionError{V: e.V}
}

// scanReport describes what scan found beyond the committed events, for the
// recovery of the next start and for the offline check. Offsets are bytes from
// the start of the input, lines are 1-based.
type scanReport struct {
	// Lines is every line read: the committed ones, the events of an
	// uncommitted trailing batch and a torn tail.
	Lines int
	// Size is the number of bytes read.
	Size int64
	// Batches is the number of batches closed by a batch.committed.
	Batches int

	// TornTail is set when the last line was never acknowledged: it has no
	// terminating newline, is blank, or does not decode. The torn bytes run
	// from TornStart to Size (TornBytes of them), starting at line TornLine.
	TornTail  bool
	TornLine  int
	TornStart int64
	TornBytes int64
	// TornCause says why the line is torn.
	TornCause error

	// UncommittedEvents is the number of events of a batch that never got its
	// batch.committed: zero when there is none. They start at line
	// UncommittedLine, byte UncommittedStart, and belong to UncommittedBatch.
	UncommittedEvents int
	UncommittedLine   int
	UncommittedStart  int64
	UncommittedBatch  event.ID
}

// scan reads a whole log and separates what was acknowledged from what a
// crash left behind. events holds the committed events, in log order, with
// their 1-based Line set; batch.committed events are included as written.
// endOfLastCommitted is the offset just after the last committed line: the
// length the log is truncated to by recovery, and the size of a clean log.
//
// The rules:
//   - A final line that has no terminating newline, is blank, or does not
//     decode (garbage, or JSON that fails its schema) is a torn tail and is
//     excluded (INV-LOG-9, operator ruling 2026-10-08). The same line followed
//     by another line is corruption (INV-LOG-11).
//   - A line whose version is not the supported one is an
//     *UnknownVersionError wherever it is, including as the final line, with
//     or without its newline; it is never torn (INV-LOG-2).
//   - An event that carries a membership batch (Payload.BatchID) opens that
//     batch, and the batch's events are contiguous and closed by a
//     batch.committed of the same id. An open batch at the end is an
//     uncommitted trailing batch: its events are excluded and reported. A
//     batch.committed with no open batch or for another batch, any other event
//     inside an open batch, and the reuse of a committed batch id are
//     corruption (INV-LOG-11). event.retracted names a batch only as its
//     target_batch, which is not membership.
//   - An event id is unique: a line that repeats the id of an earlier
//     decoded line, committed or not, is corruption at the repeating line
//     (an event's id is a unique ULID; INV-LOG-11). A final line with no terminating newline is torn whatever
//     it holds, so it is not judged.
//   - A blank line before the final line, and any line longer than
//     event.MaxEventBytes, are corruption (INV-LOG-11, INV-LOG-27).
//
// A corruption is reported at the first line, in file order, that proves it:
// for a batch broken by another event that is the other event's line. On an
// error the results are zero.
//
// Memory is bounded: a line is accumulated only while it is within
// event.MaxEventBytes. The moment a line is known to be longer, scan fails
// without reading the rest of it, so a huge line costs at most one read chunk
// more than the limit. (bufio.Scanner is not used: it would fail on a line
// over its token limit with a bare error and no offset, and ReadBytes alone
// would buffer an unbounded line.)
func scan(r io.Reader) (events []event.Event, endOfLastCommitted int64, rep scanReport, err error) {
	s := &scanner{
		br:        bufio.NewReaderSize(r, readBufferSize),
		committed: make(map[event.ID]int),
		seen:      make(map[event.ID]int),
	}
	if err := s.run(); err != nil {
		return nil, 0, scanReport{}, err
	}
	s.finish()
	return s.events, s.committedEnd, s.rep, nil
}

// openBatch is a batch that has members and no batch.committed yet.
type openBatch struct {
	id    event.ID
	line  int   // the line of its first member
	start int64 // the offset of its first member
	// events holds its members so far, which are not yet committed.
	events []event.Event
}

// tornLine is a final-line candidate: it did not decode, and whether it is a
// torn tail or corruption depends on whether another line follows.
type tornLine struct {
	line  int
	start int64
	cause error
}

type scanner struct {
	br  *bufio.Reader
	buf []byte

	rep    scanReport
	offset int64 // bytes consumed so far

	events       []event.Event
	committedEnd int64
	committed    map[event.ID]int // batch id to the line of its batch.committed
	seen         map[event.ID]int // event id to the line that first carried it
	open         *openBatch
	torn         *tornLine
}

// errLineTooLong is readLine's signal for a line over the size limit.
var errLineTooLong = fmt.Errorf("the line is longer than %d bytes, the most an event may be: %w", event.MaxEventBytes, event.ErrTooLarge)

// readLine returns the next line without its newline (a slice of an internal
// buffer, valid until the next call) and the number of bytes it took,
// newline included. terminated says whether a newline ended it. At the end of
// the input with nothing left it returns io.EOF.
func (s *scanner) readLine() (line []byte, terminated bool, size int, err error) {
	s.buf = s.buf[:0]
	for {
		chunk, readErr := s.br.ReadSlice('\n')
		s.buf = append(s.buf, chunk...)
		terminated = readErr == nil
		atEnd := errors.Is(readErr, io.EOF)
		if readErr != nil && !atEnd && !errors.Is(readErr, bufio.ErrBufferFull) {
			return nil, false, 0, fmt.Errorf("reading the event log: %w", readErr)
		}
		if atEnd && len(s.buf) == 0 {
			return nil, false, 0, io.EOF
		}
		content := len(s.buf)
		if terminated {
			content--
		}
		if content > event.MaxEventBytes {
			return nil, false, 0, errLineTooLong
		}
		if terminated || atEnd {
			return s.buf[:content], terminated, len(s.buf), nil
		}
		// bufio.ErrBufferFull: the line goes on, within the limit so far.
	}
}

func (s *scanner) run() error {
	for {
		line, terminated, size, err := s.readLine()
		lineNo := s.rep.Lines + 1
		tooLong := errors.Is(err, errLineTooLong)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil && !tooLong {
			return err // a read failure says nothing about the lines
		}
		// Another line exists, so a candidate before it was not the last.
		if s.torn != nil {
			return s.tornIsCorrupt()
		}
		if tooLong {
			return &CorruptError{Line: lineNo, Cause: err}
		}
		start := s.offset
		s.offset += int64(size)
		s.rep.Lines++
		if err := s.handle(line, terminated, lineNo, start); err != nil {
			return err
		}
	}
}

// tornIsCorrupt turns the torn candidate into the error it is when more lines
// follow it.
func (s *scanner) tornIsCorrupt() error {
	return &CorruptError{
		Line:  s.torn.line,
		Cause: fmt.Errorf("it is not a valid event and more lines follow it, so it is not the torn end of an interrupted write: %w", s.torn.cause),
	}
}

var (
	errBlankLine = errors.New("the line is blank")
	errNoNewline = errors.New("the last line has no terminating newline")
)

func (s *scanner) handle(line []byte, terminated bool, lineNo int, start int64) error {
	if len(bytes.TrimSpace(line)) == 0 {
		s.torn = &tornLine{line: lineNo, start: start, cause: errBlankLine}
		return nil
	}
	ev, err := event.Decode(line)
	var unknown *event.UnknownVersionError
	if errors.As(err, &unknown) {
		return &UnknownVersionError{Line: lineNo, V: unknown.V, cause: unknown}
	}
	if !terminated {
		// A prefix of a line this library wrote never decodes as another
		// version, so decoding an unterminated line only ever finds a version
		// error above; whatever else it holds, it is torn.
		s.torn = &tornLine{line: lineNo, start: start, cause: errNoNewline}
		return nil
	}
	if err != nil {
		s.torn = &tornLine{line: lineNo, start: start, cause: err}
		return nil
	}
	ev.Line = lineNo
	if first, repeated := s.seen[ev.ID]; repeated {
		return &CorruptError{Line: lineNo, Cause: fmt.Errorf(
			"event id %s was already used at line %d, and event ids are unique", ev.ID, first,
		)}
	}
	s.seen[ev.ID] = lineNo
	return s.classify(ev, start, s.offset)
}

// classify applies the batch rules to one decoded event whose line spans
// [start, end).
func (s *scanner) classify(ev event.Event, start, end int64) error {
	batch := ev.Payload.BatchID()
	line := ev.Line
	switch {
	case ev.Type == event.TypeBatchCommitted:
		return s.commit(ev, batch, end)
	case batch != "":
		return s.member(ev, batch, start)
	case s.open != nil:
		return &CorruptError{Line: line, Cause: fmt.Errorf(
			"a %s event that is not part of a batch follows batch %s, which began at line %d and has no batch.committed",
			ev.Type, s.open.id, s.open.line,
		)}
	}
	s.events = append(s.events, ev)
	s.committedEnd = end
	return nil
}

func (s *scanner) member(ev event.Event, batch event.ID, start int64) error {
	switch {
	case s.open != nil && s.open.id == batch:
		s.open.events = append(s.open.events, ev)
	case s.open != nil:
		return &CorruptError{Line: ev.Line, Cause: fmt.Errorf(
			"a %s event of batch %s begins inside batch %s, which began at line %d and has no batch.committed",
			ev.Type, batch, s.open.id, s.open.line,
		)}
	default:
		if at, reused := s.committed[batch]; reused {
			return &CorruptError{Line: ev.Line, Cause: fmt.Errorf(
				"a %s event names batch %s, which was already committed at line %d, so its id cannot be reused",
				ev.Type, batch, at,
			)}
		}
		s.open = &openBatch{id: batch, line: ev.Line, start: start, events: []event.Event{ev}}
	}
	return nil
}

func (s *scanner) commit(ev event.Event, batch event.ID, end int64) error {
	switch {
	case s.open == nil:
		return &CorruptError{Line: ev.Line, Cause: fmt.Errorf(
			"batch.committed closes batch %s, but no batch is open", batch,
		)}
	case s.open.id != batch:
		return &CorruptError{Line: ev.Line, Cause: fmt.Errorf(
			"batch.committed closes batch %s while batch %s, which began at line %d, is still open",
			batch, s.open.id, s.open.line,
		)}
	}
	s.events = append(s.events, s.open.events...)
	s.events = append(s.events, ev)
	s.committed[batch] = ev.Line
	s.rep.Batches++
	s.committedEnd = end
	s.open = nil
	return nil
}

// finish fills in what the end of the input leaves unacknowledged.
func (s *scanner) finish() {
	s.rep.Size = s.offset
	if s.open != nil {
		s.rep.UncommittedEvents = len(s.open.events)
		s.rep.UncommittedLine = s.open.line
		s.rep.UncommittedStart = s.open.start
		s.rep.UncommittedBatch = s.open.id
	}
	if s.torn != nil {
		s.rep.TornTail = true
		s.rep.TornLine = s.torn.line
		s.rep.TornStart = s.torn.start
		s.rep.TornBytes = s.offset - s.torn.start
		s.rep.TornCause = s.torn.cause
	}
}
