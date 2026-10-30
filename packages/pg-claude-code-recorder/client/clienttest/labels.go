package clienttest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
)

// The label table is one testdata file of the client package
// (client/testdata/labels.json): one row per fixture of the crash query, with the
// raw inputs (steps), the evidence they must produce and the label the evidence
// must derive. Three consumers load every row: the daemon test (inputs to
// evidence), the client test (evidence to label) and the command test (labels
// through conversations --ended). Covered fails a consumer that did not load
// every row.

// LabelEvidence is the evidence a row expects (the booleans of client.Evidence).
type LabelEvidence struct {
	Ended                    bool `json:"ended"`
	RegistryPresentAtExit    bool `json:"registry_present_at_exit"`
	ExitObserved             bool `json:"exit_observed"`
	ClearEntry               bool `json:"clear_entry"`
	ExitEntry                bool `json:"exit_entry"`
	RegistryStaleAtDiscovery bool `json:"registry_stale_at_discovery"`
}

// Evidence returns the client's evidence value.
func (e LabelEvidence) Evidence() client.Evidence {
	return client.Evidence{
		Ended: e.Ended, RegistryPresentAtExit: e.RegistryPresentAtExit, ExitObserved: e.ExitObserved,
		ClearEntry: e.ClearEntry, ExitEntry: e.ExitEntry, RegistryStaleAtDiscovery: e.RegistryStaleAtDiscovery,
	}
}

// LabelStep is one raw input of a row: a process starting, a registry file
// written, a history line typed, an exit, a restart.
type LabelStep struct {
	// Do is start, registry, history, history_torn, history_complete, exit_kept
	// (SIGKILL: the registry file stays), exit_clean (the registry file is
	// deleted, then the Process exits), vanish (the Process ends unwatched),
	// delete_registry, restart, end_file or settle.
	Do      string `json:"do"`
	Pid     int    `json:"pid,omitempty"`
	StartMs int64  `json:"start_ms,omitempty"`
	Session string `json:"session,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Ms      int64  `json:"ms,omitempty"`
	Display string `json:"display,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// LabelRow is one row of the label table.
type LabelRow struct {
	Name string `json:"name"`
	Doc  string `json:"doc,omitempty"`
	// Control gives the daemon a control state directory (end files).
	Control bool `json:"control,omitempty"`
	// Session is the Conversation the row judges.
	Session  string        `json:"session"`
	Steps    []LabelStep   `json:"steps"`
	Evidence LabelEvidence `json:"evidence"`
	// Label is the expected label ("" when the Conversation has no end).
	Label string `json:"label"`
	// Suspected is whether --suspected-crash lists it.
	Suspected bool `json:"suspected,omitempty"`
}

// LabelRows loads the table.
func LabelRows(t testing.TB) []LabelRow {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the label table")
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "testdata", "labels.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tab struct {
		Rows []LabelRow `json:"rows"`
	}
	if err := json.Unmarshal(b, &tab); err != nil {
		t.Fatalf("labels.json: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range tab.Rows {
		if r.Name == "" || seen[r.Name] {
			t.Fatalf("labels.json: row name %q is empty or repeated", r.Name)
		}
		seen[r.Name] = true
	}
	return tab.Rows
}

// Covered fails the test unless names is exactly the set of row names: a row a
// consumer did not load is a defect of the consumer.
func Covered(t testing.TB, consumer string, names []string) {
	t.Helper()
	want := map[string]bool{}
	for _, r := range LabelRows(t) {
		want[r.Name] = true
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	var missing []string
	for n := range want {
		if !got[n] {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the %s consumer did not load label rows %v", consumer, missing)
	}
}

// RunRow starts a daemon for the row and realizes its steps; the caller reads
// the facts afterwards.
func RunRow(t *testing.T, row LabelRow) *Env {
	t.Helper()
	e := Start(t, Options{Control: row.Control, Mutate: func(c *config.Config) { c.GraceWindow = 5 * time.Millisecond }})
	for i, s := range row.Steps {
		e.step(t, row, i, s)
	}
	e.Settle()
	return e
}

func (e *Env) registryPath(pid int) string { return e.Path("sessions", strconv.Itoa(pid)+".json") }

func (e *Env) historyPath() string { return e.Path("history.jsonl") }

func (e *Env) step(t *testing.T, row LabelRow, i int, s LabelStep) {
	t.Helper()
	fail := func(err error) {
		t.Helper()
		t.Fatalf("%s step %d (%s): %v", row.Name, i, s.Do, err)
	}
	cwd := s.Cwd
	if cwd == "" {
		cwd = "/example/work"
	}
	switch s.Do {
	case "start":
		e.Procs.Add(s.Pid, time.UnixMilli(s.StartMs), map[string]string{"TERM_PROGRAM": "example-term"})
	case "registry":
		body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":%d,"procStart":%q,"version":"2.1.0","kind":"interactive","status":"idle","updatedAt":%d}`,
			s.Pid, s.Session, cwd, s.StartMs, proc.FormatStart(time.UnixMilli(s.StartMs)), s.Ms)
		if err := os.WriteFile(e.registryPath(s.Pid), []byte(body), 0o600); err != nil {
			fail(err)
		}
		e.Settle()
	case "history", "history_torn":
		line := fmt.Sprintf(`{"display":%q,"timestamp":%d,"project":%q,"sessionId":%q}`, s.Display, s.Ms, cwd, s.Session)
		if s.Do == "history" {
			line += "\n"
		}
		e.Append("history.jsonl", line)
		e.Settle()
	case "history_complete":
		e.Append("history.jsonl", "\n")
		e.Settle()
	case "exit_kept", "exit_clean":
		if s.Do == "exit_clean" {
			if err := os.Remove(e.registryPath(s.Pid)); err != nil {
				fail(err)
			}
		}
		e.Procs.Exit(s.Pid, time.UnixMilli(s.Ms))
		e.waitExit(t, int64(s.Pid))
		e.Settle()
	case "vanish":
		e.Procs.Vanish(s.Pid)
	case "delete_registry":
		if err := os.Remove(e.registryPath(s.Pid)); err != nil {
			fail(err)
		}
		e.Settle()
	case "restart":
		e.Restart()
		e.Settle()
	case "end_file":
		body := fmt.Sprintf(`{"sessionId":%q,"reason":%q,"timeMs":%d}`, s.Session, s.Reason, s.Ms)
		if err := os.WriteFile(filepath.Join(e.ControlDir, "ends", fmt.Sprintf("%s.%d.json", s.Session, s.Ms)), []byte(body), 0o600); err != nil {
			fail(err)
		}
		e.Settle()
	case "settle":
		e.Settle()
	default:
		fail(fmt.Errorf("unknown step"))
	}
}

// waitExit waits until the recorder has recorded the exit of pid.
func (e *Env) waitExit(t *testing.T, pid int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := e.Daemon.Store().Procs()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.Pid == pid && r.ExitNS > 0 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no exit recorded for pid %d", pid)
}
