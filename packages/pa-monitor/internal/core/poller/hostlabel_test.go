package poller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/corpus/corpustest"
	"github.com/phillipgreenii/pa-monitor/internal/core/provider"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// hostDiscovery gives each discovered session the recorder Host fact a fixture
// names (a nil entry leaves the session without one).
type hostDiscovery struct {
	inner session.Discovery
	hosts map[string]*session.Host
}

func (d hostDiscovery) Discover() ([]*session.Session, error) {
	out, err := d.inner.Discover()
	for _, s := range out {
		s.Host = d.hosts[s.SessionID]
	}
	return out, err
}

// installExecRecorder puts shim `ps` and `pgrep` executables first on PATH that
// append their argv to a log file, and returns a func that reads the log. Any
// process the code under test starts through PATH lookup of those names, as
// SentinelOne would see it, shows up as a line.
func installExecRecorder(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "exec.log")
	for _, name := range []string{"ps", "pgrep"} {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> \"" + logPath + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// TestAssemble_TerminalHostFromRecorderFactRunsNoExec: one Discover/Assemble pass
// labels every session from its recorder Host fact and starts no process on the
// label path (no ps or pgrep). The control launches a ps through the same
// PATH shim to show the recorder does see an exec when one happens.
func TestAssemble_TerminalHostFromRecorderFactRunsNoExec(t *testing.T) {
	e := corpustest.Start(t)
	hosts := map[string]*session.Host{
		"h-cmux":    {Kind: "cmux"},
		"h-tmux":    {Kind: "tmux"},
		"h-ghostty": {Kind: "other", TermProgram: "ghostty"},
		"h-vscode":  {Kind: "other", TermProgram: "vscode"},
		"h-other":   {Kind: "other", TermProgram: "example-term"},
		"h-nil":     nil,
	}
	want := map[string]string{
		"h-cmux": "cmux", "h-tmux": "tmux", "h-ghostty": "ghostty",
		"h-vscode": "vscode", "h-other": "unknown", "h-nil": "unknown",
	}
	pids := map[string]int{"h-cmux": 801, "h-tmux": 802, "h-ghostty": 803, "h-vscode": 804, "h-other": 805, "h-nil": 806}
	for sid, pid := range pids {
		e.Register(pid, sid, "/w/"+sid, "", true)
	}

	calls := installExecRecorder(t)
	now := time.Unix(1_776_000_300, 0)
	p := &Poller{
		IdleThreshold: time.Hour,
		Now:           func() time.Time { return now },
		Monitor:       corpus.New(e.Facts, hostDiscovery{inner: e.Discovery(), hosts: hosts}),
		Providers:     provider.New(func() time.Time { return now }),
	}
	tree, _, err := assembleAndSnapshot(t, p, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range tree.Dirs {
		for _, s := range d.Sessions {
			got[s.SessionID] = s.TerminalHost
		}
	}
	for sid, w := range want {
		if got[sid] != w {
			t.Errorf("%s: terminal host = %q, want %q", sid, got[sid], w)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("Discover/Assemble started %d ps/pgrep processes, want 0: %v", len(got), got)
	}

	// Negative control: the recorder does see a ps launched through PATH.
	if err := exec.CommandContext(t.Context(), "ps", "-ax").Run(); err != nil {
		t.Fatalf("control ps: %v", err)
	}
	if len(calls()) == 0 {
		t.Fatal("control: a ps launch was not recorded, so the zero count proves nothing")
	}
}
