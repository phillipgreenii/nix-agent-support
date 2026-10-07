package shim

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Exit codes of a rejecting shim.
const (
	ExitRejected = 77
)

// LogRow is one shim log line.
type LogRow struct {
	Time    string   `json:"time"`
	Tool    string   `json:"tool"`
	Argv    []string `json:"argv"`
	Verdict Verdict  `json:"verdict"`
	Reason  string   `json:"reason"`
}

// Options configures one shim invocation.
type Options struct {
	Tool string // "gh" or "bd"
	// Real is the absolute path of the real tool.
	Real string
	// Log is the shim log path (appended).
	Log string
	// HermeticBD makes a `bd` read answer from nothing instead of the real tool.
	HermeticBD bool
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

// Run classifies args, logs the row, and either runs the real tool (returning
// its exit code) or rejects with ExitRejected.
func Run(o Options, args []string) int {
	var v Verdict
	var why string
	switch o.Tool {
	case "gh":
		v, why = ClassifyGH(args)
	case "bd":
		v, why = ClassifyBD(args)
	default:
		v, why = RejectUnknown, "unknown tool "+o.Tool
	}
	appendLog(o.Log, LogRow{Time: time.Now().UTC().Format(time.RFC3339Nano), Tool: o.Tool, Argv: args, Verdict: v, Reason: why})
	if v != Allow {
		_, _ = fmt.Fprintf(o.Stderr, "pg-desk-shadow: %s %s rejected (%s): the shadow run is read-only\n", o.Tool, v, why)
		return ExitRejected
	}
	if o.Tool == "bd" && o.HermeticBD {
		return hermeticBD(args, o)
	}
	cmd := exec.Command(o.Real, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = o.Stdin, o.Stdout, o.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
				return ws.ExitStatus()
			}
			return ee.ExitCode()
		}
		_, _ = fmt.Fprintf(o.Stderr, "pg-desk-shadow: run %s: %v\n", o.Real, err)
		return 127
	}
	return 0
}

// hermeticBD answers a read-verb `bd` call without a beads database: list and
// ready return an empty array, show reports not found.
func hermeticBD(args []string, o Options) int {
	_, why := ClassifyBD(args)
	switch why {
	case "show":
		_, _ = fmt.Fprintln(o.Stderr, "Error: issue not found")
		return 1
	case "version":
		_, _ = fmt.Fprintln(o.Stdout, "bd version hermetic-shim")
		return 0
	}
	_, _ = fmt.Fprintln(o.Stdout, "[]")
	return 0
}

func appendLog(path string, r LogRow) {
	if path == "" {
		return
	}
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

// CountRejects reads a shim log and counts rejected rows; offset is the byte
// position to start from. It returns the counts of write and unknown
// rejections and the new offset.
func CountRejects(path string, offset int64) (writes, unknown int, newOffset int64, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, 0, offset, nil
	}
	if err != nil {
		return 0, 0, offset, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, 0, offset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return 0, 0, offset, err
	}
	last := lastNewline(data)
	for _, line := range splitLines(data[:last]) {
		var r LogRow
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		switch r.Verdict {
		case RejectWrite:
			writes++
		case RejectUnknown:
			unknown++
		}
	}
	return writes, unknown, offset + int64(last), nil
}

func lastNewline(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	return out
}
