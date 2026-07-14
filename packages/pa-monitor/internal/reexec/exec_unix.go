//go:build unix

package reexec

import "syscall"

// sysExec is the production execFn: execve(2) via the Go runtime. On success it
// replaces the current process image and never returns; on failure it returns
// the syscall error. Go opens its own file descriptors O_CLOEXEC, so live
// sockets (gRPC, OTel, pollers) auto-close on exec — only stdio survives.
func sysExec(argv0 string, argv, envv []string) error {
	return syscall.Exec(argv0, argv, envv)
}
