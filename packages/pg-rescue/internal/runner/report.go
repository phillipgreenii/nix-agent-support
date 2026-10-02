package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-rescue/internal/capture"
	"github.com/phillipgreenii/pg-rescue/internal/report"
)

// Size caps of the report's copies of captured text.
const (
	// TailLines and TailBytes cap output_tail.
	TailLines = 200
	TailBytes = 64 << 10
	// DetailsBytes caps reported.details; the full text is in details_file.
	DetailsBytes = 16 << 10
)

const redacted = "[REDACTED]"

// redactor applies the config's redact patterns to a copy of captured text.
type redactor struct{ res []*regexp.Regexp }

func newRedactor(res []*regexp.Regexp) *redactor { return &redactor{res: res} }

func (d *redactor) apply(s string) string {
	for _, re := range d.res {
		s = re.ReplaceAllLiteralString(s, redacted)
	}
	return s
}

// buildReport creates the in-memory report once the command has failed (or
// the stdin text has been read).
func (r *run) buildReport(out *capture.File, commandExit int) *report.Report {
	p := r.p
	mode := report.ModeArgv
	var cmd *report.Command
	if p.Options.Stdin {
		mode = report.ModeStdin
	} else {
		cmd = &report.Command{Argv: append([]string(nil), p.Options.Argv...), Cwd: p.Cwd, Exit: commandExit}
	}
	rep := &report.Report{
		SchemaVersion: report.SchemaVersion,
		RunID:         p.RunID,
		Depth:         r.depth(),
		Handlers:      append([]string{}, p.Handlers...),
		Mode:          mode,
		Command:       cmd,
		Fingerprint:   report.Fingerprint(mode, p.Options.Argv, p.Cwd, p.Options.Context),
		OutputFile:    out.Path(),
		Context:       p.Options.Context,
		Host:          p.Host,
		StartedAt:     p.StartedAt.UTC(),
		Attempts:      []report.Attempt{},
	}
	if parent := p.Getenv("PG_RESCUE_RUN_ID"); parent != "" {
		rep.ParentRunID = &parent
	}
	if p.Chain != "" {
		c := p.Chain
		rep.Chain = &c
	}
	if p.Options.HasVerify {
		v := p.Options.Verify
		rep.Verify = &v
	}
	raw, lineStart := out.Window(TailBytes)
	rep.OutputTail = r.tailText(raw, lineStart)
	return rep
}

// depth is the nesting depth: 1, or the inherited PG_RESCUE_DEPTH plus one.
func (r *run) depth() int {
	inherited, err := strconv.Atoi(r.p.Getenv("PG_RESCUE_DEPTH"))
	if err != nil || inherited < 0 {
		inherited = 0
	}
	return inherited + 1
}

// tailText turns the last bytes of the output into output_tail: valid UTF-8,
// redacted, at most TailLines lines and TailBytes bytes. lineStart says the
// first byte begins a line; otherwise the partial first line is dropped.
func (r *run) tailText(raw []byte, lineStart bool) string {
	s := strings.ToValidUTF8(string(raw), "�")
	s = r.redact.apply(s)
	return LimitTail(s, TailLines, TailBytes, lineStart)
}

// LimitTail keeps the last maxLines lines of s, and at most maxBytes bytes of
// it. A trailing newline does not start a new line, and is kept. When
// lineStart is false, s begins in the middle of a line, and that partial line
// is dropped (unless it is all there is). Cuts never split a UTF-8 sequence.
func LimitTail(s string, maxLines, maxBytes int, lineStart bool) string {
	if !lineStart {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	body, trailer := s, ""
	if strings.HasSuffix(s, "\n") {
		body, trailer = s[:len(s)-1], "\n"
	}
	if n := strings.Count(body, "\n") + 1; n > maxLines {
		cut := 0
		for range n - maxLines {
			cut += strings.IndexByte(body[cut:], '\n') + 1
		}
		body = body[cut:]
	}
	for len(body)+len(trailer) > maxBytes {
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			body = body[i+1:]
			continue
		}
		// One line is still too long: cut it from the front.
		cut := min(len(body)+len(trailer)-maxBytes, len(body))
		for cut < len(body) && !utf8.RuneStart(body[cut]) {
			cut++
		}
		body = body[cut:]
		break
	}
	return body + trailer
}

// truncateBytes caps s at max bytes, on a rune boundary.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

var tmpSeq atomic.Uint64

// writeReport writes the in-memory report to report.json: a temporary file in
// the run directory is created exclusively (so a symlink planted at its name
// is never followed) with mode 0600, and then renamed over report.json (which
// replaces a symlink at that name rather than writing through it).
func (r *run) writeReport() error {
	data, err := json.MarshalIndent(r.rep, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(r.p.RunDir, "report.json"), append(data, '\n'))
}

func writeFileAtomic(path string, data []byte) error {
	dir, name := filepath.Split(path)
	var f *os.File
	var tmp string
	for range 100 {
		tmp = filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d-%d", name, os.Getpid(), tmpSeq.Add(1)))
		var err error
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	if f == nil {
		return fmt.Errorf("cannot create a temporary file next to %s", path)
	}
	_, err := f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// writeAttemptFile writes a per-attempt file (raw, mode 0600, never
// following an existing path).
func writeAttemptFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
