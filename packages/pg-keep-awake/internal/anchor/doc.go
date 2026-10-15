// Package anchor identifies a process by its kernel start time so a lease can
// tell a live holder from a recycled PID. It is filled in by a later packet.
package anchor

// The darwin process-identity code reads unix.SysctlKinfoProc (P_starttime and
// P_stat). Importing the package here pins golang.org/x/sys in go.mod from the
// scaffold onward, so the later packets do not have to re-run go mod tidy for it.
import _ "golang.org/x/sys/unix"
