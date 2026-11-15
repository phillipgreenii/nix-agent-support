package keychain

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

const (
	// SecurityPath is the absolute path of the system tool; a bare name is
	// never resolved through PATH.
	SecurityPath = "/usr/bin/security"
	// DefaultTimeout bounds one read.
	DefaultTimeout = 10 * time.Second
)

// RunFunc starts the executable at path with args and returns its stdout and
// exit code. A non-nil error means the process did not run to an exit (start
// failure or the context ended). Stderr is never returned. It is the seam that
// lets a unit test answer in process.
type RunFunc func(ctx context.Context, path string, args []string) (stdout []byte, exitCode int, err error)

// execRun is the production RunFunc.
func execRun(ctx context.Context, path string, args []string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil // discarded: stderr is never read
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return nil, ee.ExitCode(), nil
	}
	return nil, -1, err
}

// Security reads the keychain through `security find-generic-password -w`.
// The zero value uses SecurityPath, DefaultTimeout and a real process.
type Security struct {
	// Path is the executable to run; tests inject a stand-in. Empty means
	// SecurityPath.
	Path string
	// Timeout bounds one read; zero means DefaultTimeout.
	Timeout time.Duration
	// Run starts the process; nil means a real os/exec run.
	Run RunFunc
}

// Password runs the tool and returns the trimmed password. Failures are
// *Error values; stdout and stderr are never included.
func (s Security) Password(ctx context.Context, service, account string) (string, error) {
	path := s.Path
	if path == "" {
		path = SecurityPath
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	run := s.Run
	if run == nil {
		run = execRun
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, code, err := run(cctx, path, []string{"find-generic-password", "-a", account, "-s", service, "-w"})
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return "", &Error{Kind: KindTimeout, ExitCode: -1}
	}
	if err != nil {
		return "", &Error{Kind: KindOther, ExitCode: -1}
	}
	if code != 0 {
		return "", &Error{Kind: KindForExitCode(code), ExitCode: code}
	}
	return strings.TrimSpace(string(out)), nil
}
