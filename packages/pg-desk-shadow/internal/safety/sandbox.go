package safety

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultSandboxExec is the macOS sandbox tool.
const DefaultSandboxExec = "/usr/bin/sandbox-exec"

// Profile is the sandbox profile: everything allowed except file writes,
// which are allowed only under the scratch directory and on the few /dev
// nodes a shell redirect needs (without them even `echo > /dev/null` fails).
// /dev/dtracehelper is a tracing device every Go binary on macOS opens for
// write at start-up (observed 2026-10-07: 29 denials from one bd run); it is
// harmless and would otherwise drown the real denials.
func Profile(scratch string) string {
	return fmt.Sprintf(`(version 1)(allow default)(deny file-write*)(allow file-write* (subpath %q) (literal "/dev/null") (literal "/dev/tty") (literal "/dev/dtracehelper") (regex #"^/dev/fd/"))`, resolve(scratch))
}

// Wrap returns the argv that runs argv under the sandbox profile. The profile
// is passed inline (-p) so no file outside the scratch tree is needed.
func Wrap(sandboxExec, scratch string, argv []string) []string {
	out := []string{sandboxExec, "-p", Profile(scratch)}
	return append(out, argv...)
}

var denialRE = regexp.MustCompile(`(?i)operation not permitted`)

// DenialInOutput reports whether captured child output shows a sandbox
// denial.
func DenialInOutput(s string) bool { return denialRE.MatchString(s) }

// SelfTest proves the sandbox works before anything is run under it: the tool
// exists, a write outside the scratch directory is DENIED, and a write inside
// the scratch tree, to /dev/null and a lock file under the scratch tmp are
// ALLOWED. A probe write outside that succeeds means the sandbox is not
// enforcing, and the run must not start.
func SelfTest(ctx context.Context, sandboxExec, scratch, scratchTmp string) error {
	if fi, err := os.Stat(sandboxExec); err != nil || fi.IsDir() {
		return fmt.Errorf("safety: sandbox tool %s is missing: refusing to run unsandboxed (it is deprecated; if it has been removed the collector cannot start)", sandboxExec)
	}
	outsideDir, err := os.MkdirTemp("", "pg-desk-shadow-probe-")
	if err != nil {
		return fmt.Errorf("safety: make probe dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(outsideDir) }()
	if Under(outsideDir, scratch) {
		outsideDir, err = os.MkdirTemp("/tmp", "pg-desk-shadow-probe-")
		if err != nil {
			return fmt.Errorf("safety: make probe dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(outsideDir) }()
	}
	run := func(script string) (string, error) {
		argv := Wrap(sandboxExec, scratch, []string{"/bin/sh", "-c", script})
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	outside := filepath.Join(outsideDir, "probe")
	if _, err := run(fmt.Sprintf("echo x > %q", outside)); err == nil {
		return errors.New("safety: sandbox self-test FAILED: a probe write outside the scratch directory succeeded")
	}
	if _, err := os.Stat(outside); err == nil {
		return errors.New("safety: sandbox self-test FAILED: the probe file outside the scratch directory exists")
	}
	inside := filepath.Join(scratchTmp, "probe-inside")
	lock := filepath.Join(scratchTmp, "probe.lock")
	if out, err := run(fmt.Sprintf("echo x > %q && echo > /dev/null && : > %q", inside, lock)); err != nil {
		return fmt.Errorf("safety: sandbox self-test FAILED: a permitted write was denied: %v: %s", err, strings.TrimSpace(out))
	}
	_ = os.Remove(inside)
	_ = os.Remove(lock)
	return nil
}

// ScanSystemLog counts sandbox file-write denials logged since `since` for the
// named processes, through `log show`. It is the second detector: tools swallow
// some write failures (the connector's event log is best effort), so stderr
// alone can miss a denial. An error means the log could not be read (the
// caller records that as unverified, not as zero).
func ScanSystemLog(ctx context.Context, since time.Time, procs []string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/log", "show", "--style", "compact",
		"--start", since.Local().Format("2006-01-02 15:04:05"),
		"--predicate", `sender == "Sandbox" AND eventMessage CONTAINS "deny"`)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("log show: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return CountDenials(out.String(), procs), nil
}

// CountDenials counts `deny ... file-write` lines of the named processes in
// `log show` output.
func CountDenials(logOut string, procs []string) int {
	n := 0
	for _, line := range strings.Split(logOut, "\n") {
		if !strings.Contains(line, "file-write") || !strings.Contains(line, "deny") {
			continue
		}
		for _, p := range procs {
			if strings.Contains(line, "Sandbox: "+p+"(") || strings.Contains(line, " "+p+"(") {
				n++
				break
			}
		}
	}
	return n
}
