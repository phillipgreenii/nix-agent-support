// Package oplog is the operation log and the events file of pg-git-lander
// (design sections 14.5 to 14.7): a per-operation log under the git common
// dir, the append-only events.jsonl with its rotation, and the retention
// pruning rule. Hook environment values are never written anywhere here (only
// their names reach a CommandRecord), and nothing here writes into a lock
// record.
package oplog

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

// File and directory modes (design section 14.7).
const (
	DirMode  fs.FileMode = 0o700
	FileMode fs.FileMode = 0o600
)

// StateDir is <git-common-dir>/pg-git-lander.
func StateDir(commonDir string) string { return filepath.Join(commonDir, "pg-git-lander") }

// LogsDir is the directory of the operation logs.
func LogsDir(commonDir string) string { return filepath.Join(StateDir(commonDir), "logs") }

// LogPath is the log file of one operation; the op id is the log id that
// pg-git-lander log takes.
func LogPath(commonDir, opID string) string { return filepath.Join(LogsDir(commonDir), opID+".log") }

// EventsPath is the events file.
func EventsPath(commonDir string) string { return filepath.Join(StateDir(commonDir), "events.jsonl") }

// Log is the operation log of one operation. It is safe for concurrent use;
// every entry reaches the file in one write call.
type Log struct {
	// ID is the log id (the op id).
	ID string
	// Path is the log file.
	Path string

	mu    sync.Mutex
	f     *os.File
	clock seams.Clock
}

// Open creates (or reopens for append) the log of operation opID. The first
// line of a new log names BRANCH, TARGET and op_id, so pg-git-lander log
// BRANCH can find it after the operation finished and its record is gone.
// The logs directory is made 0700 and the file 0600.
func Open(commonDir, opID, branch, target string, clock seams.Clock) (*Log, error) {
	dir := LogsDir(commonDir)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, DirMode); err != nil {
		return nil, err
	}
	path := LogPath(commonDir, opID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, FileMode)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(FileMode); err != nil {
		_ = f.Close()
		return nil, err
	}
	l := &Log{ID: opID, Path: path, f: f, clock: clock}
	l.entry("START", "op_id="+opID+" branch="+branch+" target="+target)
	return l, nil
}

// Close closes the log file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// entry writes one timestamped entry; every continuation line of text is
// indented so that a line starting with a timestamp always starts an entry.
func (l *Log) entry(kind, text string) {
	var b strings.Builder
	b.WriteString(l.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	b.WriteByte(' ')
	b.WriteString(kind)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	b.WriteByte(' ')
	b.WriteString(lines[0])
	b.WriteByte('\n')
	for _, ln := range lines[1:] {
		b.WriteString("    | ")
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.f.WriteString(b.String())
}

// Line writes a free-form entry of the given kind.
func (l *Log) Line(kind, text string) { l.entry(kind, text) }

// Git logs one git command: argv, exit code and duration.
func (l *Log) Git(argv []string, code int, d time.Duration) {
	l.entry("GIT", fmt.Sprintf("%s exit=%d duration=%s", quoteArgv(argv), code, d.Round(time.Millisecond)))
}

// Command logs one hook, resolver or verify command: argv0, the NAMES of the
// variables it added (never their values), exit code, duration and its full
// output. kind is "hook", "resolver" or "verify".
func (l *Log) Command(kind string, rec hooks.CommandRecord) {
	text := rec.LogLine()
	if rec.TimedOut {
		text += " timed_out=true"
	}
	if rec.Stdout != "" {
		text += "\nstdout:\n" + rec.Stdout
	}
	if rec.Stderr != "" {
		text += "\nstderr:\n" + rec.Stderr
	}
	l.entry(strings.ToUpper(kind), text)
}

// LockWait logs a wait for a held lock.
func (l *Log) LockWait(w locks.LockWait) {
	l.entry("LOCK", fmt.Sprintf("wait %s result=%s waited=%s holder_op=%s holder_stage=%s",
		w.Lock, w.Result, w.Waited.Round(time.Millisecond), w.Holder.OpID, w.Holder.Stage))
}

// Conversion logs the conversion or takeover of a dead or expired holder's lock.
func (l *Log) Conversion(c locks.Conversion) {
	l.entry("LOCK", fmt.Sprintf("converted %s why=%s previous_owner=%s pid=%d takeover=%t",
		c.Lock, c.Why, c.PreviousOwner.Host, c.PreviousOwner.PID, c.Takeover))
}

// MergeDriver logs the diagnostics of a merge driver (design section 9.4).
func (l *Log) MergeDriver(text string) { l.entry("MERGE-DRIVER", text) }

func quoteArgv(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\") {
			q[i] = strconv.Quote(a)
		} else {
			q[i] = a
		}
	}
	return strings.Join(q, " ")
}

// Header is what the first line of a log names.
type Header struct {
	OpID, Branch, Target string
}

// ParseHeader reads the first line of the log at path.
func ParseHeader(path string) (Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, err
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return Header{}, fmt.Errorf("%s: no header line", path)
	}
	_, rest, ok := strings.Cut(strings.TrimSpace(line), " START ")
	if !ok {
		return Header{}, fmt.Errorf("%s: first line is not a header", path)
	}
	var h Header
	for _, kv := range strings.Fields(rest) {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "op_id":
			h.OpID = v
		case "branch":
			h.Branch = v
		case "target":
			h.Target = v
		}
	}
	return h, nil
}

// FindByBranch returns the op id of the current or most recent operation's
// log for branch (the newest by modification time), or "" when none exists.
func FindByBranch(commonDir, branch string) (string, error) {
	ents, err := os.ReadDir(LogsDir(commonDir))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	type cand struct {
		id string
		mt time.Time
	}
	var cs []cand
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		p := filepath.Join(LogsDir(commonDir), e.Name())
		h, err := ParseHeader(p)
		if err != nil || h.Branch != branch {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		cs = append(cs, cand{strings.TrimSuffix(e.Name(), ".log"), info.ModTime()})
	}
	if len(cs) == 0 {
		return "", nil
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].mt.After(cs[j].mt) })
	return cs[0].id, nil
}
