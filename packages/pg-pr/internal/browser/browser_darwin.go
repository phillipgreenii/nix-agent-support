//go:build darwin

package browser

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// DefaultBin is Chrome's real executable inside the .app bundle.
	//
	// Invoking it DIRECTLY — rather than through open(1) — is what lets a
	// command line reach an already-running Chrome: the new process finds the
	// singleton lock in the profile directory, forwards its argv to the running
	// instance, prints "Opening in existing browser session." and exits. Neither
	// alternative works: macOS silently drops `open -a … --args` when the app is
	// already running, and `open -na` starts a second instance that then fights
	// the first over the profile lock.
	DefaultBin = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

	// DefaultProfileDir names the profile directory the window opens in. It is
	// passed explicitly so the tabs can never land in a different profile than
	// the operator's own, whatever Chrome's last-used state happens to be.
	//
	// Both constants are overridden by BinEnvVar / ProfileEnvVar (declared in
	// browser.go) for a Chrome installed outside /Applications, a multi-profile
	// setup, or a test stub.
	DefaultProfileDir = "Default"
)

// forwardWait bounds how long openWindow waits for the launched process to
// exit before concluding it has become the browser itself. See openWindow for
// why elapsing is a SUCCESS rather than a failure. Overridable in tests.
var forwardWait = 5 * time.Second

func openWindow(urls []string) error {
	if len(urls) == 0 {
		return nil
	}

	bin := DefaultBin
	if v := os.Getenv(BinEnvVar); v != "" {
		bin = v
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("cannot find the Google Chrome executable at %s (set %s to override): %w", bin, BinEnvVar, err)
	}

	profile := DefaultProfileDir
	if v := os.Getenv(ProfileEnvVar); v != "" {
		profile = v
	}

	args := append([]string{"--profile-directory=" + profile, "--new-window"}, urls...)

	// Deliberately exec.Command, NOT exec.CommandContext: CommandContext kills
	// the child when its context is cancelled, and main's signal context is
	// cancelled as this process returns — which would shoot down the very
	// browser this command just started in the cold-start case below.
	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", bin, err)
	}

	// Two outcomes, distinguishable only by timing:
	//
	//   Chrome ALREADY RUNNING (the common case) — argv is forwarded to the
	//   running instance and this process exits in a few hundred milliseconds.
	//   Its exit status is the real result, so report it.
	//
	//   Chrome NOT RUNNING — there is no instance to forward to, so this process
	//   BECOMES the browser and will not exit until the operator quits it.
	//   Waiting on that is indistinguishable from a hang. Once the window has
	//   had time to appear we therefore stop waiting and report success: the
	//   tabs ARE open, and the browser is meant to outlive this command.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s exited with error: %w (output: %s)", bin, err, strings.TrimSpace(out.String()))
		}
		return nil
	case <-time.After(forwardWait):
		// The child is not bound to a context and is never signalled here, so
		// it survives this process exiting. Nothing reads out on this path, so
		// leaving Wait in flight races nothing.
		return nil
	}
}
