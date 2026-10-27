// Package waitlog follows a log file until a line matches a regular
// expression, replacing the `timeout N tail -f FILE | grep -m1 RE` idiom.
//
// Two properties make it safe to grant to an unattended session:
//
//   - the file MUST resolve (after symlinks) to a regular file under one of a
//     fixed list of roots, so it is not a general "read any file" primitive;
//   - the pattern is an RE2 expression (Go regexp), so a hostile pattern cannot
//     cause catastrophic backtracking.
package waitlog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ErrTimeout is returned by Wait when the deadline passes with no match.
var ErrTimeout = errors.New("timed out waiting for a matching line")

// RefusedError reports a file refused by policy before it was opened.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return "refused: " + e.Reason }

// MaxLine bounds the bytes buffered for one line. A longer line is tested in
// MaxLine-sized pieces rather than growing without limit.
const MaxLine = 1 << 20

// MaxTimeout is the longest Timeout Wait accepts.
const MaxTimeout = time.Hour

// Options configures Wait.
type Options struct {
	// Path is the log file to follow.
	Path string
	// Pattern selects the line to return.
	Pattern *regexp.Regexp
	// Roots are the directories Path MUST resolve under.
	Roots []string
	// Timeout bounds the wait; it MUST be in (0, MaxTimeout].
	Timeout time.Duration
	// Poll is how often the file is re-read; defaults to 100ms.
	Poll time.Duration
	// Lines is how many existing trailing lines are tested first (tail -n);
	// 0 means only lines appended after the call. Ignored with FromStart.
	Lines int
	// FromStart tests the whole existing file first.
	FromStart bool
}

// ResolveUnderRoots resolves path through symlinks and returns it when it is a
// regular file under one of roots (roots are resolved through symlinks too, so
// /tmp and /private/tmp agree).
func ResolveUnderRoots(path string, roots []string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", &RefusedError{Reason: fmt.Sprintf("path %q is not absolute", path)}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("no such file: %s", path)
		}
		return "", err
	}
	for _, r := range roots {
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rr, resolved)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		fi, err := os.Stat(resolved)
		if err != nil {
			return "", err
		}
		if !fi.Mode().IsRegular() {
			return "", &RefusedError{Reason: fmt.Sprintf("%s is not a regular file", resolved)}
		}
		return resolved, nil
	}
	return "", &RefusedError{Reason: fmt.Sprintf("%s is not under an allowed log root", resolved)}
}

// Wait follows the file and returns the first matching line without its line
// terminator. It returns ErrTimeout when Timeout passes first, ctx.Err() when
// ctx ends first, and a *RefusedError for a policy refusal.
func Wait(ctx context.Context, o Options) (string, error) {
	if o.Pattern == nil {
		return "", errors.New("waitlog: nil pattern")
	}
	if o.Timeout <= 0 || o.Timeout > MaxTimeout {
		return "", &RefusedError{Reason: fmt.Sprintf("timeout must be in (0, %s]", MaxTimeout)}
	}
	if o.Lines < 0 {
		return "", &RefusedError{Reason: "lines must be >= 0"}
	}
	poll := o.Poll
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	resolved, err := ResolveUnderRoots(o.Path, o.Roots)
	if err != nil {
		return "", err
	}
	deadline := time.NewTimer(o.Timeout)
	defer deadline.Stop()

	f, err := os.Open(resolved)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", &RefusedError{Reason: fmt.Sprintf("%s is not a regular file", resolved)}
	}

	var offset int64
	switch {
	case o.FromStart:
		offset = 0
	default:
		offset, err = tailOffset(f, info.Size(), o.Lines)
		if err != nil {
			return "", err
		}
	}

	var pending []byte
	buf := make([]byte, 32*1024)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		// Drain everything currently readable.
		for {
			n, rerr := f.ReadAt(buf, offset)
			if n > 0 {
				offset += int64(n)
				pending = append(pending, buf[:n]...)
				var line string
				var ok bool
				line, pending, ok = scan(pending, o.Pattern)
				if ok {
					return line, nil
				}
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					return "", rerr
				}
				break
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", ErrTimeout
		case <-ticker.C:
		}

		// Detect rotation or truncation.
		cur, serr := os.Stat(resolved)
		if serr != nil {
			continue // briefly absent during rotation; keep the old handle
		}
		if !os.SameFile(info, cur) {
			resolved2, rerr := ResolveUnderRoots(o.Path, o.Roots)
			if rerr != nil {
				var ref *RefusedError
				if errors.As(rerr, &ref) {
					return "", rerr
				}
				continue
			}
			nf, oerr := os.Open(resolved2)
			if oerr != nil {
				continue
			}
			_ = f.Close()
			f, resolved = nf, resolved2
			if info, err = f.Stat(); err != nil {
				return "", err
			}
			offset, pending = 0, nil
			continue
		}
		if cur.Size() < offset {
			offset, pending = 0, nil
		}
	}
}

// scan tests every complete line of b in order and returns the first match
// plus the unconsumed remainder. A remainder longer than MaxLine is tested as
// a line of its own and dropped.
func scan(b []byte, re *regexp.Regexp) (match string, rest []byte, ok bool) {
	for {
		i := indexNL(b)
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(string(b[:i]), "\r")
		b = b[i+1:]
		if re.MatchString(line) {
			return line, b, true
		}
	}
	if len(b) > MaxLine {
		line := string(b[:MaxLine])
		if re.MatchString(line) {
			return line, nil, true
		}
		return "", nil, false
	}
	return "", b, false
}

func indexNL(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}

// tailOffset returns the offset at which the last n lines of the first size
// bytes of f begin (the file's end when n is 0). It reads only a bounded 1 MiB
// window from the end: if n lines do not fit in it, the first full line of the
// window is used.
func tailOffset(f *os.File, size int64, n int) (int64, error) {
	if n == 0 || size == 0 {
		return size, nil
	}
	const window = 1 << 20
	start := size - window
	if start < 0 {
		start = 0
	}
	b := make([]byte, size-start)
	if _, err := f.ReadAt(b, start); err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	// A trailing newline terminates the last line rather than starting one.
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end--
	}
	seen := 0
	for i := end - 1; i >= 0; i-- {
		if b[i] == '\n' {
			seen++
			if seen == n {
				return start + int64(i) + 1, nil
			}
		}
	}
	if start == 0 {
		return 0, nil
	}
	// The window began mid-line; skip to the first full line.
	if j := indexNL(b); j >= 0 {
		return start + int64(j) + 1, nil
	}
	return size, nil
}
