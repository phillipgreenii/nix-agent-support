package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

// TestExitCodeTable has a row for every line of the exit-code table that does
// not involve a signal sent to the wrapper (those are in the signal matrix).
func TestExitCodeTable(t *testing.T) {
	type row struct {
		name     string
		handlers []hd
		cmd      []string
		opts     []string
		stdin    string
		want     int
		kind     runner.Kind
	}
	declines := result("declines", "declined")
	fails := hd{name: "fails", argv: helperArgv("exit", "code=1")}
	resolves := hd{name: "resolves", argv: helperArgv("result", "outcome=resolved", "touch={dir}/fixed")}
	defers := result("defers", "deferred")
	needs := func(dir string) []string { return helperArgv("needs", "file="+dir+"/fixed", "code=7") }

	rows := []row{
		{name: "command succeeded", handlers: []hd{resolves}, cmd: helperArgv("exit", "code=0"), want: 0, kind: runner.KindSuccess},
		{name: "resolved and verify passed", handlers: []hd{resolves}, cmd: nil, want: 0, kind: runner.KindResolved},
		{name: "deferred", handlers: []hd{declines, defers}, cmd: helperArgv("exit", "code=4"), want: 75, kind: runner.KindDeferred},
		{name: "every handler declined: the command's own code", handlers: []hd{declines, declines}, cmd: helperArgv("exit", "code=4"), want: 4, kind: runner.KindUnhandled},
		{name: "every handler failed: the command's own code", handlers: []hd{fails, fails}, cmd: helperArgv("exit", "code=9"), want: 9, kind: runner.KindUnhandled},
		{name: "mixed declined and failed", handlers: []hd{declines, fails}, cmd: helperArgv("exit", "code=3"), want: 3, kind: runner.KindUnhandled},
		{name: "the command's own 70 passes through when unhandled", handlers: []hd{declines}, cmd: helperArgv("exit", "code=70"), want: 70, kind: runner.KindUnhandled},
		{name: "the command's own 75 passes through when unhandled", handlers: []hd{declines}, cmd: helperArgv("exit", "code=75"), want: 75, kind: runner.KindUnhandled},
		{name: "128+signo passthrough: killed by SIGTERM it was not sent by the wrapper", handlers: []hd{declines}, cmd: helperArgv("selfkill", "sig=15"), want: 143, kind: runner.KindUnhandled},
		{name: "128+signo passthrough: SIGKILL", handlers: []hd{declines}, cmd: helperArgv("selfkill", "sig=9"), want: 137, kind: runner.KindUnhandled},
		{name: "--stdin, every handler declined: 1", handlers: []hd{declines}, opts: []string{"--stdin", "--verify", "true"}, stdin: "boom\n", want: 1, kind: runner.KindUnhandled},
		{name: "--stdin, handlers failed: 1", handlers: []hd{fails}, opts: []string{"--stdin", "--verify", "true"}, stdin: "boom\n", want: 1, kind: runner.KindUnhandled},
		{name: "--stdin, deferred: 75", handlers: []hd{defers}, opts: []string{"--stdin", "--verify", "true"}, stdin: "boom\n", want: 75, kind: runner.KindDeferred},
		{name: "--stdin, resolved and verified: 0", handlers: []hd{resolves}, opts: []string{"--stdin", "--verify", "true"}, stdin: "boom\n", want: 0, kind: runner.KindResolved},
		{name: "resolved but verify failed, nothing else: the command's code", handlers: []hd{resolves}, cmd: helperArgv("exit", "code=6"), opts: []string{"--verify", "exit 1"}, want: 6, kind: runner.KindUnhandled},
		{name: "--stdin, verify failed: 1", handlers: []hd{resolves}, opts: []string{"--stdin", "--verify", "exit 3"}, stdin: "boom\n", want: 1, kind: runner.KindUnhandled},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			dir := t.TempDir()
			hs := append([]hd(nil), r.handlers...)
			for i := range hs {
				argv := make([]string, len(hs[i].argv))
				for j, a := range hs[i].argv {
					argv[j] = strings.ReplaceAll(a, "{dir}", dir)
				}
				hs[i].argv = argv
			}
			var uniq []hd
			seen := map[string]bool{}
			for _, h := range hs {
				if !seen[h.name] {
					seen[h.name] = true
					uniq = append(uniq, h)
				}
			}
			e := newE2E(t, "", uniq, chainOf(names(hs)...))
			cmd := r.cmd
			if cmd == nil && len(r.opts) == 0 {
				cmd = needs(dir) // fails until the handler creates {dir}/fixed
			}
			if r.stdin != "" {
				in, err := os.CreateTemp(e.root, "stdin")
				if err != nil {
					t.Fatal(err)
				}
				in.WriteString(r.stdin)
				in.Seek(0, 0)
				e.rt.Stdin = in
			}
			opts := r.opts
			if len(opts) == 0 && r.cmd == nil {
				opts = []string{"--verify", "test -e " + dir + "/fixed"}
			}
			code, stdout, stderr := e.wrap(strings.Join(names(hs), ","), cmd, opts...)
			if code != r.want {
				t.Errorf("exit = %d; want %d\nstdout: %s\nstderr: %s", code, r.want, stdout, stderr)
			}
			if got := e.result().Kind; got != r.kind {
				t.Errorf("result kind = %s; want %s", got, r.kind)
			}
			if got := e.result().ExitCode; got != code {
				t.Errorf("result exit code %d != process exit code %d", got, code)
			}
		})
	}
}

// TestWrapperSignalExitCodes: the wrapper exits 128+signo once the command
// it forwarded the signal to is gone, without running any handler.
func TestWrapperSignalExitCodes(t *testing.T) {
	for _, c := range []struct {
		sig  syscall.Signal
		want int
	}{{syscall.SIGINT, 130}, {syscall.SIGTERM, 143}, {syscall.SIGHUP, 129}} {
		t.Run(signalLabel(c.sig), func(t *testing.T) {
			e := newE2E(t, "", []hd{{name: "h", argv: helperArgv("ran", "file="+t.TempDir()+"/handler-ran")}}, chainOf("h"))
			started := filepath.Join(e.root, "started")
			code, _, _ := e.signalDuring(started, c.sig, "h", helperArgv("sleep", "started="+started))
			if code != c.want {
				t.Errorf("exit = %d; want %d", code, c.want)
			}
			r := e.result()
			if r.Kind != runner.KindInterrupted || r.Interrupted == nil || r.Interrupted.Phase != runner.PhaseCommand || r.Interrupted.Signal != c.sig {
				t.Errorf("result = %+v interrupted=%+v", r, r.Interrupted)
			}
			if len(r.Report.Attempts) != 0 {
				t.Errorf("no handler may run after an interrupt: %+v", r.Report.Attempts)
			}
		})
	}
}
