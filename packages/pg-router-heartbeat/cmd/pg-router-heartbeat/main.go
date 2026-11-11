// pg-router-heartbeat is an out-of-stack dead-man's switch: run once a minute
// by a boot-time launchd daemon, it pages when no Prometheus sample for any job
// has arrived for 30 minutes of awake time. It depends on none of the stack it
// watches. Tick gaps longer than --max-gap count as sleep and add nothing.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Version is injected at build time by mkGoApp.
var Version = "dev"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pg-router-heartbeat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prom := fs.String("prometheus-url", "http://127.0.0.1:9090", "Prometheus base URL")
	stateDir := fs.String("state-dir", "/var/db/pg-router-heartbeat", "state directory")
	threshold := fs.Duration("threshold", 30*time.Minute, "awake silence that pages")
	maxGap := fs.Duration("max-gap", 3*time.Minute, "tick gap above this counts as sleep")
	renotify := fs.Duration("renotify", time.Hour, "re-page interval during an outage")
	notifyCmd := fs.String("notify-command", "", "command run on a page; {message} is replaced (split on spaces, no shell)")
	notifyTimeout := fs.Duration("notify-timeout", 5*time.Second, "bound on the notify command; it gets SIGTERM at this point (keep well under the 60 s interval)")
	notifyWait := fs.Duration("notify-wait-delay", 2*time.Second, "grace after SIGTERM before the notify command is killed")
	version := fs.Bool("version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *version {
		fmt.Fprintln(stdout, Version)
		return 0
	}
	d := deps{
		now:      time.Now,
		probe:    func() bool { return promHasSamples(*prom, 10*time.Second) },
		stateDir: *stateDir,
		notify: func(msg string) error {
			if *notifyCmd == "" {
				fmt.Fprintln(stderr, msg)
				return nil
			}
			f := strings.Fields(*notifyCmd)
			for i := range f {
				f[i] = strings.ReplaceAll(f[i], "{message}", msg)
			}
			return runNotify(f, *notifyTimeout, *notifyWait)
		},
	}
	if err := runOnce(d, params{*threshold, *maxGap, *renotify}); err != nil {
		fmt.Fprintln(stderr, "pg-router-heartbeat:", err)
		return 1
	}
	return 0
}

// runNotify runs the notify command bounded by timeout. On expiry the command
// gets SIGTERM, and after waitDelay it is killed and its pipes are abandoned,
// so a hung notifier cannot keep the one-shot alive past the next launchd tick.
func runNotify(argv []string, timeout, waitDelay time.Duration) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty notify command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = waitDelay
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("notify command timed out after %s: %w", timeout, err)
		}
		return err
	}
	return nil
}
