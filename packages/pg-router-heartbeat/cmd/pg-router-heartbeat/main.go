// pg-router-heartbeat is an out-of-stack dead-man's switch: run once a minute
// by a boot-time launchd daemon, it pages when no Prometheus sample for any job
// has arrived for 30 minutes of awake time. It depends on none of the stack it
// watches. Tick gaps longer than --max-gap count as sleep and add nothing.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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
			return exec.Command(f[0], f[1:]...).Run()
		},
	}
	if err := runOnce(d, params{*threshold, *maxGap, *renotify}); err != nil {
		fmt.Fprintln(stderr, "pg-router-heartbeat:", err)
		return 1
	}
	return 0
}
