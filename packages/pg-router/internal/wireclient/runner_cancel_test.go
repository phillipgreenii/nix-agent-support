//go:build unix

package wireclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// cancelStub writes an executable #!/bin/sh stub that installs trapLine, then
// signals readiness by creating the ready file and idles in short sleeps (so a
// pending trap runs promptly). It returns the stub path and the ready path.
func cancelStub(t *testing.T, trapLine string) (stub, ready string) {
	t.Helper()
	dir := t.TempDir()
	stub = filepath.Join(dir, "stub")
	ready = filepath.Join(dir, "ready")
	body := "#!/bin/sh\ncat >/dev/null\n" + trapLine + "\n: > " + ready + "\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub, ready
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// runUntilCancelled starts OSRunner.Run on stub, waits for ready, cancels the
// context and returns Run's exit code and error and how long Run took to return
// after the cancel.
func runUntilCancelled(t *testing.T, stub, ready string) (int, error, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		_, code, err := OSRunner{}.Run(ctx, []string{stub}, []byte("{}"))
		done <- result{code, err}
	}()
	waitForFile(t, ready)
	start := time.Now()
	cancel()
	select {
	case r := <-done:
		return r.code, r.err, time.Since(start)
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after cancel")
		return 0, nil, 0
	}
}

// TestOSRunner_cancelSendsSIGTERMFirst: a cancelled dispatch reaches the handler
// as SIGTERM, so its own TERM handler (the command adapter's payload cleanup)
// runs; SIGKILL would run nothing.
func TestOSRunner_cancelSendsSIGTERMFirst(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "term-seen")
	stub, ready := cancelStub(t, "trap ': > "+marker+"; exit 0' TERM")

	_, err, _ := runUntilCancelled(t, stub, ready)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("the handler's TERM trap did not run (no marker): %v", statErr)
	}
}

// TestOSRunner_cancelKillsAHandlerThatIgnoresTERM is the negative control: a
// handler that ignores SIGTERM is SIGKILLed once WaitDelay passes, so Run never
// hangs on it.
func TestOSRunner_cancelKillsAHandlerThatIgnoresTERM(t *testing.T) {
	old := runWaitDelay
	runWaitDelay = 300 * time.Millisecond
	t.Cleanup(func() { runWaitDelay = old })

	stub, ready := cancelStub(t, "trap '' TERM")
	code, err, took := runUntilCancelled(t, stub, ready)
	// A handler killed by a signal is reported as exit code -1 with a nil error
	// (Run hands the exit code to the caller), never as a clean exit 0.
	if code != -1 || err != nil {
		t.Fatalf("code, err = %d, %v; want -1, nil for a handler killed by SIGKILL", code, err)
	}
	if took < 250*time.Millisecond {
		t.Fatalf("Run returned after %v, before the WaitDelay: the handler was not given its TERM grace", took)
	}
	if took > 10*time.Second {
		t.Fatalf("Run took %v after cancel, want it bounded by about the WaitDelay", took)
	}
}
