//go:build darwin

package metrics

import (
	"bytes"
	"encoding/binary"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ListProcs reads the process table in-process (kern.proc.all plus
// kern.procargs2), launching no ps. Only processes named git are asked for
// their arguments, which avoids one sysctl per process.
func ListProcs() ([]Proc, error) {
	return listProcsComm(func(comm string) bool { return comm == "git" })
}

func listProcsComm(wantComm func(string) bool) ([]Proc, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	var procs []Proc
	for i := range kps {
		kp := &kps[i]
		comm := string(kp.Proc.P_comm[:])
		if j := strings.IndexByte(comm, 0); j >= 0 {
			comm = comm[:j]
		}
		if !wantComm(comm) {
			continue
		}
		pid := int(kp.Proc.P_pid)
		raw, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil {
			continue // the process exited, or is not ours to read
		}
		st := kp.Proc.P_starttime
		procs = append(procs, Proc{
			Pid:   pid,
			Start: time.Unix(int64(st.Sec), int64(st.Usec)*1000),
			Args:  strings.Join(parseProcArgs2(raw), " "),
		})
	}
	return procs, nil
}

// parseProcArgs2 decodes a kern.procargs2 buffer: int32 argc, the exec path,
// NUL padding, then argc NUL-terminated argv strings.
func parseProcArgs2(b []byte) []string {
	if len(b) < 4 {
		return nil
	}
	argc := int(int32(binary.NativeEndian.Uint32(b[:4])))
	b = b[4:]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[i:]
	} else {
		return nil
	}
	b = bytes.TrimLeft(b, "\x00")
	var args []string
	for len(args) < argc && len(b) > 0 {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			i = len(b)
		}
		args = append(args, string(b[:i]))
		if i >= len(b) {
			break
		}
		b = b[i+1:]
	}
	return args
}
