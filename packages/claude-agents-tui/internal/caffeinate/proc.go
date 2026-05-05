package caffeinate

import (
	"fmt"
	"os/exec"
	"runtime"
	"sync"
)

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
	cmd := exec.Command("caffeinate", "-w", fmt.Sprintf("%d", tuiPID))
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd = cmd
	return nil
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
