//go:build darwin

package lease

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// szomb is SZOMB from <sys/proc.h> (SIDL=1 SRUN=2 SSLEEP=3 SSTOP=4 SZOMB=5);
// golang.org/x/sys/unix does not export it.
const szomb = 5

// ProcStateOf returns start time and zombie state for pid through
// unix.SysctlKinfoProc (sysctl KERN_PROC_PID). A pid that is not in the
// process table is ErrNoProcess.
func ProcStateOf(pid int) (ProcState, error) {
	if pid <= 0 {
		return ProcState{}, ErrNoProcess
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
			return ProcState{}, ErrNoProcess
		}
		return ProcState{}, fmt.Errorf("sysctl kern.proc.pid %d: %w", pid, err)
	}
	// An empty answer (size 0) can come back as a zeroed struct for a pid
	// that raced out of the table.
	if int(kp.Proc.P_pid) != pid {
		return ProcState{}, ErrNoProcess
	}
	tv := kp.Proc.P_starttime
	return ProcState{
		Exists:  true,
		StartUS: int64(tv.Sec)*1_000_000 + int64(tv.Usec),
		Zombie:  kp.Proc.P_stat == szomb,
		PPID:    int(kp.Eproc.Ppid),
	}, nil
}

// ListPIDs returns every pid in the process table (kern.proc.all).
func ListPIDs() ([]int, error) {
	all, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("sysctl kern.proc.all: %w", err)
	}
	pids := make([]int, 0, len(all))
	for i := range all {
		if p := int(all[i].Proc.P_pid); p > 0 {
			pids = append(pids, p)
		}
	}
	return pids, nil
}

// ProcessInfo returns pid's executable name and argv from KERN_PROCARGS2. It
// fails for a pid that is gone, a zombie (no arguments are readable) and a
// process owned by another user.
func ProcessInfo(pid int) (ProcInfo, error) {
	if pid <= 0 {
		return ProcInfo{}, ErrNoProcess
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			return ProcInfo{}, ErrNoProcess
		}
		return ProcInfo{}, fmt.Errorf("sysctl kern.procargs2 %d: %w", pid, err)
	}
	return parseProcArgs2(raw)
}

// parseProcArgs2 decodes a KERN_PROCARGS2 buffer: int32 argc, the executable
// path and NUL padding, then argc NUL-terminated argv strings (the
// environment follows and is ignored).
func parseProcArgs2(raw []byte) (ProcInfo, error) {
	if len(raw) < 4 {
		return ProcInfo{}, errors.New("procargs2: short buffer")
	}
	argc := int(int32(binary.LittleEndian.Uint32(raw[:4])))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return ProcInfo{}, errors.New("procargs2: unterminated executable path")
	}
	exe := string(rest[:end])
	rest = bytes.TrimLeft(rest[end:], "\x00")
	argv := make([]string, 0, argc)
	for i := 0; i < argc && len(rest) > 0; i++ {
		e := bytes.IndexByte(rest, 0)
		if e < 0 {
			argv = append(argv, string(rest))
			rest = nil
			break
		}
		argv = append(argv, string(rest[:e]))
		rest = rest[e+1:]
	}
	return ProcInfo{Exe: exe, Name: filepath.Base(exe), Argv: argv}, nil
}
