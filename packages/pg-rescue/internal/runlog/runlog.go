// Package runlog writes pg-rescue's run log: one JSON line per run, including
// the happy path, appended to <state-root>/runs.jsonl. It is the measurement
// surface: every rate the README documents is a jq expression over it.
//
// A failed write never changes the wrapper's exit code; the caller prints a
// warning and carries on.
package runlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/config"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

// FileName is the run log's name inside the state root; the rotated copy is
// FileName+".1".
const FileName = "runs.jsonl"

// MaxBytes is the size past which the log is rotated.
const MaxBytes = 10 << 20

// Attempt is one handler attempt as the run log records it.
type Attempt struct {
	Handler    string          `json:"handler"`
	Position   int             `json:"position"`
	Tags       []string        `json:"tags"`
	Outcome    string          `json:"outcome"`
	Reason     string          `json:"reason"`
	Exit       int             `json:"exit"`
	DurationMS int64           `json:"duration_ms"`
	VerifyMS   *int64          `json:"verify_ms,omitempty"`
	Binary     string          `json:"binary"`
	Meta       json.RawMessage `json:"meta"`
}

// Entry is one line of runs.jsonl.
type Entry struct {
	RunID           string   `json:"run_id"`
	ParentRunID     *string  `json:"parent_run_id"`
	Depth           int      `json:"depth"`
	TS              string   `json:"ts"`
	PGRescueVersion string   `json:"pg_rescue_version"`
	Host            string   `json:"host"`
	Mode            string   `json:"mode"`
	Chain           *string  `json:"chain"`
	Handlers        []string `json:"handlers"`
	// Cmd is the quoted argv, redacted; null in --stdin mode.
	Cmd *string `json:"cmd"`
	Cwd string  `json:"cwd"`
	// Context is the caller's --context text, redacted.
	Context     string `json:"context"`
	Fingerprint string `json:"fingerprint"`
	// Exit is the command's own exit code; null when there was no command
	// exit to record (--stdin mode, or the command never started).
	Exit *int `json:"exit"`
	// Result is one of success, resolved, deferred, unhandled, error,
	// interrupted.
	Result     string  `json:"result"`
	ResolvedBy *string `json:"resolved_by"`
	DeferredBy *string `json:"deferred_by"`
	// InterruptedDuring names the handler a wrapper signal interrupted (an
	// interrupted verify counts as its handler); null otherwise. Signal is the
	// signal's name, set whenever result is interrupted.
	InterruptedDuring *string   `json:"interrupted_during"`
	Signal            *string   `json:"signal"`
	FinalExit         int       `json:"final_exit"`
	DurationMS        int64     `json:"duration_ms"`
	Attempts          []Attempt `json:"attempts"`
}

// Input is what a run log line is built from.
type Input struct {
	Result  *runner.Result
	Options *cli.Options
	Config  *config.Config
	Chain   string
	// Handlers are the instance names of the selected chain, in order.
	Handlers  []string
	Cwd       string
	RunID     string
	Host      string
	Version   string
	StartedAt time.Time
	// Redact is applied to cmd and context. nil means no redaction.
	Redact func(string) string
}

// Build makes the run log entry for a finished run.
func Build(in Input) Entry {
	res := in.Result
	redact := in.Redact
	if redact == nil {
		redact = func(s string) string { return s }
	}
	e := Entry{
		RunID:           in.RunID,
		ParentRunID:     res.ParentRunID,
		Depth:           res.Depth,
		TS:              in.StartedAt.UTC().Format("2006-01-02T15:04:05Z"),
		PGRescueVersion: in.Version,
		Host:            in.Host,
		Mode:            string(report.ModeArgv),
		Handlers:        append([]string{}, in.Handlers...),
		Cwd:             in.Cwd,
		Context:         redact(in.Options.Context),
		Fingerprint:     res.Fingerprint,
		Result:          string(res.Kind),
		FinalExit:       res.ExitCode,
		DurationMS:      res.Duration.Milliseconds(),
		Attempts:        []Attempt{},
	}
	if in.Chain != "" {
		c := in.Chain
		e.Chain = &c
	}
	if in.Options.Stdin {
		e.Mode = string(report.ModeStdin)
	} else {
		cmd := redact(report.QuoteArgv(in.Options.Argv))
		e.Cmd = &cmd
	}
	if res.CommandExit >= 0 {
		x := res.CommandExit
		e.Exit = &x
	}
	switch res.Kind {
	case runner.KindResolved:
		e.ResolvedBy = strPtr(res.ResolvedBy)
	case runner.KindDeferred:
		e.DeferredBy = strPtr(res.DeferredBy)
	case runner.KindInterrupted:
		if in := res.Interrupted; in != nil {
			if in.Handler != "" {
				e.InterruptedDuring = strPtr(in.Handler)
			}
			e.Signal = strPtr(signalName(in.Signal))
		}
	}
	if res.Report != nil {
		for i, a := range res.Report.Attempts {
			at := Attempt{
				Handler: a.Handler, Position: a.Position, Tags: append([]string{}, a.Tags...),
				Outcome: string(a.Outcome), Reason: a.Reason, Exit: a.Exit,
				DurationMS: a.DurationMS, VerifyMS: a.VerifyMS, Meta: a.Reported.Meta,
			}
			if i < len(res.Binaries) {
				at.Binary = res.Binaries[i]
			}
			if len(at.Meta) == 0 {
				at.Meta = json.RawMessage("null")
			}
			e.Attempts = append(e.Attempts, at)
		}
	}
	return e
}

func strPtr(s string) *string { return &s }

func signalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return fmt.Sprintf("signal %d", int(s))
}

// Line is the entry as one line of JSON ending in a newline.
func (e Entry) Line() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Append adds line to <stateRoot>/runs.jsonl with a single O_APPEND write, so
// concurrent runs never interleave within a line. When the file already
// exceeds MaxBytes it is first renamed to runs.jsonl.1, replacing the previous
// rotated copy.
func Append(stateRoot string, line []byte) error {
	return AppendMax(stateRoot, line, MaxBytes)
}

// AppendMax is Append with an explicit rotation size.
func AppendMax(stateRoot string, line []byte, max int64) error {
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return err
	}
	path := filepath.Join(stateRoot, FileName)
	if err := rotate(path, max); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	n, err := f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != len(line) {
		err = fmt.Errorf("short write to %s: %d of %d bytes", path, n, len(line))
	}
	return err
}

// rotate renames path to path+".1" when it is larger than max. Two runs that
// notice the same oversized file at once would each rename it, and the second
// would throw away the fresh log the first started; a flock on a sibling file
// makes the check-and-rename atomic between them.
func rotate(path string, max int64) error {
	fi, err := os.Lstat(path)
	if err != nil || fi.Size() <= max {
		return nil // nothing to rotate; a missing file is created by the append
	}
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	fi, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && fi.Size() <= max) {
		return nil // another run rotated it first
	}
	if err != nil {
		return err
	}
	return os.Rename(path, path+".1")
}

// WriteResultFile writes the entry to the --result-file as one JSON object.
func WriteResultFile(path string, line []byte) error {
	return os.WriteFile(path, line, 0o600)
}
