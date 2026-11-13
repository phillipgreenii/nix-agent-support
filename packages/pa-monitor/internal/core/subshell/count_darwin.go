//go:build darwin

package subshell

import (
	"golang.org/x/sys/unix"
)

// listProcs snapshots the process table through the kern.proc.all sysctl; it
// launches no process.
func listProcs() ([]Proc, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	procs := make([]Proc, 0, len(kps))
	for i := range kps {
		kp := &kps[i]
		comm := kp.Proc.P_comm[:]
		b := make([]byte, 0, len(comm))
		for _, ch := range comm {
			if ch == 0 {
				break
			}
			b = append(b, byte(ch))
		}
		procs = append(procs, Proc{
			PID:  int(kp.Proc.P_pid),
			PPID: int(kp.Eproc.Ppid),
			Comm: string(b),
		})
	}
	return procs, nil
}

// defaultCount counts direct shell children in-process: p_comm basename zsh,
// bash or sh, ppid == parent.
func defaultCount(parent int) (int, error) {
	procs, err := listProcs()
	if err != nil {
		return 0, err
	}
	return CountShells(procs, parent), nil
}
