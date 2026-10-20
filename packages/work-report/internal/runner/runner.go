//go:build unix

package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
)

// Runner runs generators. The exported fields are the test seams; the zero
// value of each means the production default. A Runner is safe for concurrent
// use as long as its fields are not changed after first use.
type Runner struct {
	// ToolVersion is recorded as tool_version.
	ToolVersion string
	// Now is the clock for timestamps and run-directory names (time.Now).
	Now func() time.Time
	// Rand supplies the 4 hex digits of a run-directory name (crypto/rand).
	Rand io.Reader
	// Grace is how long a terminated group gets between SIGTERM and SIGKILL
	// (DefaultGrace).
	Grace time.Duration
	// WaitDelay bounds the final Wait (DefaultWaitDelay).
	WaitDelay time.Duration
	// LookupEnv reads the caller's environment for the allowlist
	// (os.LookupEnv).
	LookupEnv func(string) (string, bool)

	// beforeWrite is a test hook called before each file work-report writes
	// into a run directory.
	beforeWrite func(dir, name string)

	randMu sync.Mutex
}

// New returns a Runner with every seam at its production default.
func New(toolVersion string) *Runner { return &Runner{ToolVersion: toolVersion} }

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) grace() time.Duration {
	if r.Grace > 0 {
		return r.Grace
	}
	return DefaultGrace
}

func (r *Runner) waitDelay() time.Duration {
	if r.WaitDelay > 0 {
		return r.WaitDelay
	}
	return DefaultWaitDelay
}

func (r *Runner) lookup() func(string) (string, bool) {
	if r.LookupEnv != nil {
		return r.LookupEnv
	}
	return os.LookupEnv
}

// Environ builds the allowlisted environment for g in mode, reading the
// caller's environment through the Runner's LookupEnv seam.
func (r *Runner) Environ(g config.Generator, mode string) []string {
	return Environ(g, mode, r.lookup())
}

func (r *Runner) suffix() string {
	var b [2]byte
	src := r.Rand
	if src == nil {
		src = rand.Reader
	}
	r.randMu.Lock()
	_, err := io.ReadFull(src, b[:])
	r.randMu.Unlock()
	if err != nil {
		// A broken random source must not stop a report: fall back to the
		// clock's sub-second digits.
		n := r.now().UnixNano()
		b[0], b[1] = byte(n>>8), byte(n)
	}
	return hex.EncodeToString(b[:])
}

const stampLayout = "20060102T150405Z"

func (r *Runner) newRunID(started time.Time) string {
	return started.UTC().Format(stampLayout) + "-" + r.suffix()
}

// run is the state of one Run call.
type run struct {
	r       *Runner
	rec     Record
	dir     string
	parent  string
	stderr  string
	t0      time.Time
	started time.Time
	notices []string
}

// Run executes req and returns its outcome. It never returns an error: every
// failure is an Outcome whose Record carries the outcome kind and reason.
func (r *Runner) Run(ctx context.Context, req Request) Outcome {
	t0 := time.Now()
	started := r.now().UTC()
	gen := req.Generator
	builtin := req.Builtin != nil

	cfg := req.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	argv0 := ""
	if builtin {
		argv0 = "builtin:" + gen.Name
	} else if len(gen.Command) > 0 {
		argv0 = gen.Command[0]
	}
	rng := envelopeRange(req.Range)
	nar := normalizeNarrowing(req.Narrowing)
	entries := envelopeEntries(req)
	footer := envelopeFooter(req.Footer)
	var baseline *string
	if slices.Contains(gen.Inputs, config.BaselineName) {
		b := req.Baseline
		baseline = &b
	}

	st := &run{r: r, t0: t0, started: started, parent: req.OutputParent, rec: Record{
		Status:          StatusFailed,
		ToolVersion:     r.ToolVersion,
		ContractVersion: ContractVersion,
		StartedAt:       fmtTime(started),
		Generator:       RecordGenerator{Name: gen.Name, Argv0: argv0},
		Config:          config.Redact(cfg),
		ConfigSHA256:    sha256JSON(cfg),
		InputSHA256: sha256JSON(struct {
			Range     EnvelopeRange   `json:"range"`
			Narrowing Narrowing       `json:"narrowing"`
			Entries   []EnvelopeEntry `json:"entries"`
			Config    map[string]any  `json:"config"`
			Baseline  *string         `json:"baseline"`
		}{rng, nar, entries, cfg, baseline}),
		Range:      rng,
		Narrowing:  nar,
		EntryCount: len(entries),
		Overrides:  nonNil(req.Overrides),
	}}
	if req.Preset != "" {
		p := req.Preset
		st.rec.Preset = &p
	}
	// An executable that does not exist is detected before the run directory
	// is created, so a typo leaves no directory behind.
	if !builtin {
		if len(gen.Command) == 0 || gen.Command[0] == "" {
			return st.fail(KindNotFound, "the generator has no command")
		}
		if !strings.Contains(gen.Command[0], "/") {
			if _, err := exec.LookPath(gen.Command[0]); err != nil {
				return st.fail(KindNotFound, fmt.Sprintf("command %q not found on PATH", gen.Command[0]))
			}
		}
	}

	if !filepath.IsAbs(req.OutputParent) {
		return st.fail(KindOutputDirUnwritable, fmt.Sprintf("output directory %q is not absolute", req.OutputParent))
	}
	dir, runID, err := r.makeRunDir(req.OutputParent, started)
	st.rec.RunID = runID
	if err != nil {
		return st.fail(KindOutputDirUnwritable, err.Error())
	}
	st.dir = dir

	env := Envelope{
		ContractVersion: ContractVersion,
		Mode:            ModeGenerate,
		RunID:           runID,
		Report:          EnvelopeReport{Preset: st.rec.Preset, Generator: gen.Name},
		Range:           rng,
		Narrowing:       nar,
		Entries:         entries,
		Footer:          footer,
		Baseline:        baseline,
		Config:          cfg,
		OutputDir:       dir,
	}

	var stdout []byte
	genStart := time.Now()
	if builtin {
		m, err := req.Builtin(ctx, env, dir)
		st.rec.GeneratorMS = time.Since(genStart).Milliseconds()
		zero := 0
		st.rec.GeneratorExit = &zero
		if err != nil {
			one := 1
			st.rec.GeneratorExit = &one
			if ctx.Err() != nil {
				return st.fail(KindCancelled, "cancelled")
			}
			return st.fail(KindExitNonzero, "built-in generator failed: "+err.Error())
		}
		if m.ContractVersion == 0 {
			m.ContractVersion = ContractVersion
		}
		stdout, err = json.Marshal(m)
		if err != nil {
			return st.fail(KindInvalidManifest, "built-in manifest cannot be encoded: "+err.Error())
		}
	} else {
		in, err := json.Marshal(env)
		if err != nil {
			return st.fail(KindExitNonzero, "envelope cannot be encoded: "+err.Error())
		}
		res := r.Exec(ctx, ExecSpec{
			Argv:    gen.Command,
			Dir:     dir,
			Env:     Environ(gen, ModeGenerate, r.lookup()),
			Stdin:   in,
			Timeout: gen.Timeout,
		})
		st.rec.GeneratorMS = res.Duration.Milliseconds()
		st.rec.TimedOut = res.TimedOut
		st.rec.GeneratorExit = res.ExitCode
		if res.Signal != "" {
			s := res.Signal
			st.rec.Signal = &s
		}
		st.stderr = res.Stderr
		switch {
		case res.StartErr != nil && res.NotFound:
			return st.fail(KindNotFound, fmt.Sprintf("command %q not found", gen.Command[0]))
		case res.StartErr != nil:
			return st.fail(KindExitNonzero, fmt.Sprintf("command %q could not start: %v", gen.Command[0], res.StartErr))
		case res.StdoutCapped:
			return st.fail(KindStdoutCap, fmt.Sprintf("generator wrote more than %d bytes to stdout", StdoutCap))
		case res.TimedOut:
			return st.fail(KindTimeout, fmt.Sprintf("generator exceeded its timeout of %s", effectiveTimeout(gen.Timeout)))
		case res.Cancelled:
			return st.fail(KindCancelled, "the run was cancelled")
		case res.ExitCode == nil:
			return st.fail(KindExitNonzero, fmt.Sprintf("generator was terminated by %s", res.Signal))
		case *res.ExitCode == 3:
			return st.fail(KindConfigInvalid, withStderr("generator rejected its config (exit 3)", res.Stderr))
		case *res.ExitCode != 0:
			return st.fail(KindExitNonzero, withStderr(fmt.Sprintf("generator exited with status %d", *res.ExitCode), res.Stderr))
		}
		stdout = res.Stdout
	}

	v, f := validateManifest(stdout, dir)
	if f != nil {
		return st.fail(f.kind, f.reason)
	}
	st.rec.Status = StatusOK
	st.rec.OutcomeKind = KindOK
	st.rec.Files = v.files
	st.rec.Summary = v.summary
	st.rec.Warnings = append(append([]string{}, v.warnings...), req.ExtraWarnings...)
	return st.finish()
}

func effectiveTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return config.DefaultGeneratorTimeout
	}
	return d
}

// makeRunDir creates the output parent (mode 0700) when missing and a unique
// <UTC timestamp>-<4 hex> child with an atomic mkdir, retrying with a new
// suffix on EEXIST at most MaxCollisionRetries times.
func (r *Runner) makeRunDir(parent string, started time.Time) (dir, runID string, err error) {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", r.newRunID(started), fmt.Errorf("output directory %q cannot be created: %w", parent, err)
	}
	for attempt := 0; attempt < MaxCollisionRetries; attempt++ {
		runID = r.newRunID(started)
		dir = filepath.Join(parent, runID)
		err := os.Mkdir(dir, 0o700)
		switch {
		case err == nil:
			// The umask must not widen or narrow the documented mode.
			if err := os.Chmod(dir, 0o700); err != nil {
				return "", runID, fmt.Errorf("run directory %q cannot be secured: %w", dir, err)
			}
			return dir, runID, nil
		case errors.Is(err, fs.ErrExist):
			continue
		default:
			return "", runID, fmt.Errorf("run directory %q cannot be created: %w", dir, err)
		}
	}
	return "", runID, fmt.Errorf("no unused run directory name in %q after %d attempts", parent, MaxCollisionRetries)
}

// fail records a failure outcome and finishes the run.
func (s *run) fail(kind, reason string) Outcome {
	if s.rec.RunID == "" {
		s.rec.RunID = s.r.newRunID(s.started)
	}
	s.rec.Status = StatusFailed
	s.rec.OutcomeKind = kind
	s.rec.Reason = reason
	s.rec.Files, s.rec.Summary, s.rec.Warnings = nil, "", nil
	return s.finish()
}

// finish writes stderr.log and then manifest.json (last), updates latest after
// an ok run, and builds the Outcome.
func (s *run) finish() Outcome {
	rec := &s.rec
	if s.dir != "" {
		if err := s.write(StderrName, []byte(s.stderr)); err != nil {
			s.degrade(KindOutputDirUnwritable, StderrName+" cannot be written: "+err.Error())
		}
		rec.EndedAt = fmtTime(s.r.now())
		rec.TotalMS = time.Since(s.t0).Milliseconds()
		data, err := json.MarshalIndent(rec, "", "  ")
		if err == nil {
			err = s.write(ManifestName, append(data, '\n'))
		}
		if err != nil {
			s.degrade(KindOutputDirUnwritable, ManifestName+" cannot be written: "+err.Error())
		}
	} else {
		rec.EndedAt = fmtTime(s.r.now())
		rec.TotalMS = time.Since(s.t0).Milliseconds()
	}
	if rec.Status == StatusOK && s.dir != "" {
		if n := updateLatest(s.parent, rec.RunID, s.r); n != "" {
			s.notices = append(s.notices, n)
		}
	}
	return Outcome{Record: *rec, RunDir: s.dir, Stderr: s.stderr, Notices: s.notices}
}

// degrade turns an ok run into a failed one when the run record itself cannot
// be written; a run that already failed keeps its own outcome and the problem
// becomes a notice.
func (s *run) degrade(kind, reason string) {
	if s.rec.Status == StatusOK {
		s.rec.Status = StatusFailed
		s.rec.OutcomeKind = kind
		s.rec.Reason = reason
		s.rec.Files, s.rec.Summary, s.rec.Warnings = nil, "", nil
		return
	}
	s.notices = append(s.notices, reason)
}

// updateLatest points parent/latest at the new run by creating a temporary
// symlink and renaming it over latest. A latest that exists and is not a
// symlink is left alone. It returns a notice for the operator, or "".
func updateLatest(parent, runID string, r *Runner) string {
	link := filepath.Join(parent, LatestLink)
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		return fmt.Sprintf("%s exists and is not a symlink; left alone", link)
	}
	tmp := filepath.Join(parent, ".latest.tmp-"+randHex(8))
	if err := os.Symlink(runID, tmp); err != nil {
		return fmt.Sprintf("%s could not be updated: %v", link, err)
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return fmt.Sprintf("%s could not be updated: %v", link, err)
	}
	return ""
}

// writeAtomic writes data to dir/name by unlinking any existing entry (an
// existing directory is an error and is never written through), creating a
// temporary file with O_EXCL in the same directory, and renaming it into
// place. The file is mode 0600.
func writeAtomic(dir, name string, data []byte) error {
	target := filepath.Join(dir, name)
	if fi, err := os.Lstat(target); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("%s is a directory", target)
		}
		if err := os.Remove(target); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var f *os.File
	var tmp string
	for i := 0; ; i++ {
		tmp = filepath.Join(dir, "."+name+".tmp-"+randHex(8))
		var err error
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i >= MaxCollisionRetries {
			return err
		}
	}
	_, err := f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, target)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic("runner: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func sha256JSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Unreachable for JSON-compatible config and the envelope types; a
		// value that cannot be encoded hashes as its error so the digest is
		// still deterministic.
		b = []byte("unencodable:" + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func normalizeNarrowing(n Narrowing) Narrowing {
	return Narrowing{Labels: nonNil(n.Labels), Sources: nonNil(n.Sources), Types: nonNil(n.Types)}
}

func envelopeRange(r Range) EnvelopeRange {
	out := EnvelopeRange{
		Spec:      r.Spec,
		Before:    r.Before.Format(time.RFC3339),
		OpenStart: r.OpenStart,
		Zone:      r.Zone,
	}
	if !r.OpenStart {
		s := r.Since.Format(time.RFC3339)
		out.Since = &s
	}
	return out
}

func envelopeEntries(req Request) []EnvelopeEntry {
	out := make([]EnvelopeEntry, 0, len(req.Entries))
	for _, e := range req.Entries {
		fields := e.Fields
		if len(fields) == 0 || string(fields) == "null" || !json.Valid(fields) {
			fields = json.RawMessage("{}")
		}
		out = append(out, EnvelopeEntry{
			ID:         e.ID,
			ExternalID: e.ExternalID,
			SourceID:   e.SourceID,
			Type:       e.Type,
			OccurredAt: e.OccurredAt.UTC().Format(time.RFC3339),
			Summary:    e.Summary,
			URL:        e.URL,
			Labels:     nonNil(e.Labels),
			Fields:     fields,
		})
	}
	return out
}

func envelopeFooter(in []FooterItem) []EnvelopeFooter {
	out := make([]EnvelopeFooter, 0, len(in))
	for _, f := range in {
		item := EnvelopeFooter{SourceID: f.SourceID, Count: f.Count, LastOutcome: f.LastOutcome}
		if !f.LastSuccessAt.IsZero() {
			s := f.LastSuccessAt.UTC().Format(time.RFC3339)
			item.LastSuccessAt = &s
		}
		out = append(out, item)
	}
	return out
}

// withStderr appends the first line of the retained stderr (at most 200
// runes) to msg.
func withStderr(msg, stderr string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(stderr), "\n", 2)[0])
	if line == "" {
		return msg
	}
	if utf8.RuneCountInString(line) > 200 {
		line = string([]rune(line)[:200]) + "..."
	}
	return msg + ": " + line
}

// MarshalJSON emits the files, summary and warnings keys for an ok run only.
func (r Record) MarshalJSON() ([]byte, error) {
	type plain Record
	if r.Status != StatusOK {
		return json.Marshal(plain(r))
	}
	files := r.Files
	if files == nil {
		files = []RecordFile{}
	}
	warnings := r.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return json.Marshal(struct {
		plain
		Files    []RecordFile `json:"files"`
		Summary  string       `json:"summary"`
		Warnings []string     `json:"warnings"`
	}{plain: plain(r), Files: files, Summary: r.Summary, Warnings: warnings})
}

// write writes one work-report-owned file into the run directory.
func (s *run) write(name string, data []byte) error {
	if s.r.beforeWrite != nil {
		s.r.beforeWrite(s.dir, name)
	}
	return writeAtomic(s.dir, name, data)
}
