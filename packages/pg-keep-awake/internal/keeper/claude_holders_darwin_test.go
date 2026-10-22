//go:build darwin

package keeper

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

// Real-process proof for Claude Code's own inhibitor (bead pg2-0n6og, V-3).
// Every process here is a copy of the test binary that the test itself
// spawned: a stand-in `claude` parent (argv[0] "claude", executable
// .claude-wrapped, as the nix launcher's `exec -a claude` produces) with a
// child run as `caffeinate -i -t 300`, and a stand-in non-claude parent with
// the same child. The real /usr/bin/caffeinate is never started, and no
// process the test did not spawn is looked at or signalled; the injected
// ClaudeCaffeinateExe is the copy standing in for /usr/bin/caffeinate.

// stand-in tree: a parent with a caffeinate child.
type inhibitorTree struct {
	parent *child
	holder int
}

func copyTestBinary(t *testing.T, dir, name string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, src, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// spawnInhibitorTree starts a parent whose argv[0] is parentArgv0 and whose
// child is cafeinateBin run as `caffeinate -i -t 300`.
func spawnInhibitorTree(t *testing.T, parentBin, parentArgv0, caffeinateBin string) inhibitorTree {
	t.Helper()
	cmd := exec.Command(parentBin)
	cmd.Args = []string{parentArgv0}
	cmd.Env = append(os.Environ(), "KEEPER_TEST_HELPER=parent:"+caffeinateBin)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &child{cmd: cmd}
	t.Cleanup(p.kill)
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("stand-in parent printed no child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	// The child's exec replaces the forked image: wait until it reports the
	// copy's path and the caffeinate argv.
	for i := 0; i < 500; i++ {
		if info, err := lease.ProcessInfo(pid); err == nil && info.Exe != "" && isClaudeInhibitorArgv(info.Argv) {
			return inhibitorTree{parent: p, holder: pid}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child %d never became `caffeinate -i -t 300`", pid)
	return inhibitorTree{}
}

func extOf(pids ...int) []lease.StatusExternal {
	var out []lease.StatusExternal
	for _, pid := range pids {
		out = append(out, lease.StatusExternal{PID: pid, Process: "caffeinate", Type: "PreventUserIdleSystemSleep"})
	}
	return out
}

func pidGone(pid int) bool {
	for i := 0; i < 300; i++ {
		if st := (lease.SysProber{}).Probe(pid); !st.Exists || st.Zombie {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

type claudeWorld struct {
	claude  inhibitorTree // parent argv[0] "claude"
	foreign inhibitorTree // parent argv[0] "notclaude"
	cafBin  string
}

func newClaudeWorld(t *testing.T) claudeWorld {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cafBin := copyTestBinary(t, dir, "caffeinate")
	claudeBin := copyTestBinary(t, dir, ".claude-wrapped")
	otherBin := copyTestBinary(t, dir, "other-parent")
	return claudeWorld{
		claude:  spawnInhibitorTree(t, claudeBin, "claude", cafBin),
		foreign: spawnInhibitorTree(t, otherBin, "notclaude", cafBin),
		cafBin:  cafBin,
	}
}

func TestRealProcs_ClaudeHolderIsRecognisedOnlyUnderClaude(t *testing.T) {
	w := newClaudeWorld(t)
	ch := ClaudeHolders{Procs: NewOSProcs(), Exe: w.cafBin}
	if _, ok := ch.identify(w.claude.holder); !ok {
		t.Error("the caffeinate under the claude stand-in was not recognised")
	}
	if _, ok := ch.identify(w.foreign.holder); ok {
		t.Error("the caffeinate under a non-claude parent was recognised")
	}
	// The injected path pins the executable: the same process is not Claude
	// Code's inhibitor when the real /usr/bin/caffeinate is what is expected.
	if _, ok := (ClaudeHolders{Procs: NewOSProcs()}).identify(w.claude.holder); ok {
		t.Error("a process not running /usr/bin/caffeinate was recognised")
	}
}

func TestRealProcs_ReapStopsOnlyTheClaudeParentedHolder(t *testing.T) {
	w := newClaudeWorld(t)
	res := ClaudeHolders{Procs: NewOSProcs(), Exe: w.cafBin}.Reap(extOf(w.claude.holder, w.foreign.holder))
	if len(res.Errs) != 0 || len(res.Reaped) != 1 || res.Reaped[0] != w.claude.holder {
		t.Fatalf("Reap = %+v, want only pid %d", res, w.claude.holder)
	}
	if !pidGone(w.claude.holder) {
		t.Fatal("the claude-parented holder is still running")
	}
	for name, pid := range map[string]int{
		"the claude stand-in":        w.claude.parent.pid(),
		"the non-claude parent":      w.foreign.parent.pid(),
		"the non-claude one's child": w.foreign.holder,
	} {
		if st := (lease.SysProber{}).Probe(pid); !st.Exists || st.Zombie {
			t.Errorf("%s (pid %d) did not survive", name, pid)
		}
	}
}

func realKeeperRig(t *testing.T, w claudeWorld, reading policy.Reading) *krig {
	t.Helper()
	g := newKRig(t, func(o *KeeperOptions) {
		o.Procs = NewOSProcs() // real process control
		o.ClaudeCaffeinateExe = w.cafBin
		o.External = func(int) []lease.StatusExternal { return extOf(w.claude.holder, w.foreign.holder) }
		o.Config = config.Defaults()
	})
	g.reader.Set(reading)
	return g
}

func TestRealProcs_KeeperTickReapsUnderReleasedGateOnly(t *testing.T) {
	w := newClaudeWorld(t)

	// no_spawn (25% on battery): the holder is marked, not stopped.
	g := realKeeperRig(t, w, policy.Reading{OnBattery: true, Percent: 25})
	g.tick()
	if st := (lease.SysProber{}).Probe(w.claude.holder); !st.Exists {
		t.Fatal("the holder was stopped under no_spawn")
	}
	var marked []int
	for _, e := range g.status().External {
		if e.ManagedBy == lease.ManagedByClaude {
			marked = append(marked, e.PID)
		}
	}
	if len(marked) != 1 || marked[0] != w.claude.holder {
		t.Fatalf("managed holders in status = %v, want [%d]", marked, w.claude.holder)
	}

	// released (15% on battery): the claude-parented holder is stopped, the
	// other one is not.
	g.reader.Set(policy.Reading{OnBattery: true, Percent: 15})
	g.advance(g.k.Interval())
	g.tick()
	if !pidGone(w.claude.holder) {
		t.Fatal("the holder survived a released gate")
	}
	if st := (lease.SysProber{}).Probe(w.foreign.holder); !st.Exists || st.Zombie {
		t.Fatal("the non-claude caffeinate was stopped")
	}
	if st := (lease.SysProber{}).Probe(w.claude.parent.pid()); !st.Exists || st.Zombie {
		t.Fatal("the claude stand-in itself was stopped")
	}
}
