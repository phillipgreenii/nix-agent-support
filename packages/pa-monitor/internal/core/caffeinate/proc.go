package caffeinate

import (
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
)

// caffeinatePath is the absolute path of the macOS system caffeinate. It is
// pinned (never resolved through PATH) so that pa-monitor does not become a
// client of a caffeinate shim installed earlier on PATH, and so Proc.Kill()
// never signals a shim.
const caffeinatePath = "/usr/bin/caffeinate"

type Proc struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func (p *Proc) Spawn(tuiPID int) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return nil
	}
	cmd := exec.Command(caffeinatePath, "-w", fmt.Sprintf("%d", tuiPID))
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd = cmd
	return nil
}

// IsAlive returns true if the caffeinate process was spawned and is still running.
func (p *Proc) IsAlive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	return p.cmd.Process.Signal(syscall.Signal(0)) == nil
}

func (p *Proc) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
	p.cmd = nil
	return nil
}
