// helper_test.go: the fake command, handler and verify. The test binary
// re-execs itself with GO_WANT_HELPER_PROCESS=1 (the pattern of
// packages/pg-router-probe/cmd/pg-router-probe/testmain_test.go); TestMain
// notices the variable and runs helperMain instead of the suite. The behavior
// is the first argument after "--"; leading key=value arguments after it
// parameterize the behavior and anything after those is literal.
package runner_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// runHelper runs the helper behavior named after the "--" in os.Args and exits.
func runHelper() {
	for i, a := range os.Args {
		if a == "--" {
			helperMain(os.Args[i+1:])
		}
	}
	os.Exit(98)
}

// helperArgv is the argv that runs the helper with a behavior.
func helperArgv(behavior string, args ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestHelperProcess$", "--", behavior}, args...)
}

// helperShell is a shell command line that runs the helper (for --verify).
func helperShell(behavior string, args ...string) string {
	parts := []string{`exec "$HELPER_BIN"`, "-test.run='^TestHelperProcess$'", "--", behavior}
	for _, a := range args {
		parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(parts, " ")
}

func splitKV(args []string) (kv map[string]string, rest []string) {
	kv = map[string]string{}
	for i, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" || strings.ContainsAny(k, " $;'\"\\") {
			return kv, args[i:]
		}
		kv[k] = v
	}
	return kv, nil
}

func octal(s string, def uint64) uint64 {
	if n, err := strconv.ParseUint(s, 8, 32); err == nil {
		return n
	}
	return def
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// publish writes a file atomically so a polling test never sees half of it.
func publish(path, content string) {
	if path == "" {
		return
	}
	tmp := path + ".tmp"
	_ = os.WriteFile(tmp, []byte(content), 0o600)
	_ = os.Rename(tmp, path)
}

// expand substitutes {pos} (the handler's position) in a path.
func expand(s string) string {
	s = strings.ReplaceAll(s, "{pos}", os.Getenv("PG_RESCUE_POSITION"))
	return strings.ReplaceAll(s, "{rundir}", os.Getenv("PG_RESCUE_RUN_DIR"))
}

var helperBehaviors = map[string]func(kv map[string]string, rest []string){}

func helperMain(args []string) {
	if len(args) == 0 {
		os.Exit(98)
	}
	behavior := args[0]
	kv, rest := splitKV(args[1:])
	code := atoi(kv["code"], 0)
	if f := kv["touch"]; f != "" {
		_ = os.WriteFile(expand(f), nil, 0o600)
	}
	if f, ok := helperBehaviors[behavior]; ok {
		f(kv, rest)
	}
	switch behavior {
	case "exit":
		fmt.Fprint(os.Stdout, kv["out"])
		fmt.Fprint(os.Stderr, kv["err"])
		os.Exit(code)
	case "ran":
		// The "was I spawned?" probe.
		_ = os.WriteFile(kv["file"], []byte("ran"), 0o600)
		os.Exit(code)
	case "needs":
		// Succeeds only once a handler has created the file.
		if _, err := os.Stat(kv["file"]); err == nil {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "needs: missing", kv["file"])
		os.Exit(code)
	case "flip":
		// Fails the first time it runs, succeeds afterwards.
		if _, err := os.Stat(kv["file"]); err == nil {
			os.Exit(0)
		}
		_ = os.WriteFile(kv["file"], nil, 0o600)
		os.Exit(1)
	case "argv":
		b, _ := json.Marshal(rest)
		_ = os.WriteFile(kv["file"], b, 0o600)
		os.Exit(code)
	case "interleave":
		for i := 1; i <= 3; i++ {
			fmt.Fprintf(os.Stdout, "out%d\n", i)
			time.Sleep(30 * time.Millisecond)
			fmt.Fprintf(os.Stderr, "err%d\n", i)
			time.Sleep(30 * time.Millisecond)
		}
		os.Exit(code)
	case "cat":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(code)
	case "lines":
		// n lines "line K", optionally without the final newline.
		n := atoi(kv["n"], 1)
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "line %d", i)
			if i < n || kv["final_newline"] != "0" {
				b.WriteByte('\n')
			}
		}
		fmt.Fprint(os.Stdout, b.String())
		os.Exit(code)
	case "bytes":
		// Raw bytes, hex-encoded in "hex", or "n" copies of the byte "b".
		if h := kv["hex"]; h != "" {
			var out []byte
			for i := 0; i+1 < len(h); i += 2 {
				v, _ := strconv.ParseUint(h[i:i+2], 16, 8)
				out = append(out, byte(v))
			}
			_, _ = os.Stdout.Write(out)
		} else {
			chunk := strings.Repeat(kv["b"], 4096)
			for left := atoi(kv["n"], 0); left > 0; left -= len(chunk) {
				_, _ = io.WriteString(os.Stdout, chunk[:min(left, len(chunk))])
			}
		}
		os.Exit(code)
	case "sleep":
		exitOnSignal()
		publish(expand(kv["started"]), strconv.Itoa(os.Getpid()))
		time.Sleep(time.Duration(atoi(kv["secs"], 60)) * time.Second)
		os.Exit(code)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
		publish(expand(kv["started"]), strconv.Itoa(os.Getpid()))
		time.Sleep(time.Duration(atoi(kv["secs"], 60)) * time.Second)
		os.Exit(code)
	case "selfkill":
		_ = syscall.Kill(os.Getpid(), syscall.Signal(atoi(kv["sig"], 9)))
		time.Sleep(5 * time.Second)
		os.Exit(97)
	case "grandchild":
		// Leaves a sleeping child behind in the same process group, then
		// exits. hold=1 lets the child keep our stdout and stderr open.
		// child= picks the child's behavior (default sleep); setsid=1 moves it into
		// its own session, out of reach of a process-group kill.
		kind := kv["child"]
		if kind == "" {
			kind = "sleep"
		}
		child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "--", kind, "secs="+kv["secs"], "ms="+kv["ms"], "text="+kv["text"], "started="+kv["ready"])
		child.Env = os.Environ()
		if kv["setsid"] == "1" {
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		if kv["hold"] == "1" {
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
		}
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "grandchild:", err)
			os.Exit(96)
		}
		publish(expand(kv["pid"]), strconv.Itoa(child.Process.Pid))
		// ready= names a file the child publishes once it is set up (for example
		// once it ignores TERM); wait for it so the handler exits only then.
		if ready := kv["ready"]; ready != "" {
			for i := 0; i < 2000 && !fileExists(ready); i++ {
				time.Sleep(5 * time.Millisecond)
			}
		}
		os.Exit(code)
	case "late":
		time.Sleep(time.Duration(atoi(kv["ms"], 0)) * time.Millisecond)
		fmt.Fprint(os.Stdout, kv["text"])
		os.Exit(code)
	case "chmod-rundir":
		_ = os.Chmod(os.Getenv("PG_RESCUE_RUN_DIR"), os.FileMode(octal(kv["mode"], 0o700)))
		os.Exit(code)
	case "record":
		record(kv, rest)
		os.Exit(code)
	case "overwrite-report":
		_ = os.WriteFile(os.Getenv("PG_RESCUE_REPORT"), []byte("not json at all"), 0o600)
		os.Exit(code)
	case "symlink-report":
		// Replaces report.json with a symlink to kv["target"].
		p := os.Getenv("PG_RESCUE_REPORT")
		_ = os.Remove(p)
		if err := os.Symlink(kv["target"], p); err != nil {
			fmt.Fprintln(os.Stderr, "symlink-report:", err)
			os.Exit(95)
		}
		os.Exit(code)
	case "result":
		res := map[string]any{}
		for _, k := range []string{"outcome", "summary", "details"} {
			if v, ok := kv[k]; ok {
				res[k] = v
			}
		}
		if m := kv["meta"]; m != "" {
			res["meta"] = json.RawMessage(m)
		}
		b, _ := json.Marshal(res)
		if raw, ok := kv["raw"]; ok {
			b = []byte(raw)
		}
		fmt.Fprintln(os.Stdout, string(b))
		fmt.Fprint(os.Stderr, kv["err"])
		if _, ok := kv["code"]; !ok {
			switch kv["outcome"] {
			case "declined":
				code = 2
			case "deferred":
				code = 3
			}
		}
		os.Exit(code)
	case "bigout":
		// A valid result padded with whitespace to exactly size bytes.
		size := atoi(kv["size"], 0)
		head := `{"summary":"big"}`
		out := head + strings.Repeat(" ", max(size-len(head), 0))
		_, _ = io.WriteString(os.Stdout, out)
		os.Exit(code)
	}
	fmt.Fprintln(os.Stderr, "unknown helper behavior:", behavior)
	os.Exit(99)
}

// record writes what the process saw to kv["dest"] (with {pos} expanded).
func record(kv map[string]string, rest []string) {
	env := map[string]string{}
	for _, e := range os.Environ() {
		if k, v, _ := strings.Cut(e, "="); strings.HasPrefix(k, "PG_RESCUE_") || strings.HasPrefix(k, "XT_") {
			env[k] = v
		}
	}
	wd, _ := os.Getwd()
	stdin, stdinErr := io.ReadAll(os.Stdin)
	st, _ := os.Stdin.Stat()
	dn, _ := os.Stat(os.DevNull)
	parentPgid, _ := syscall.Getpgid(os.Getppid())
	report, _ := os.ReadFile(os.Getenv("PG_RESCUE_REPORT"))
	rec := map[string]any{
		"env": env, "cwd": wd, "args": rest,
		"stdin_len": len(stdin), "stdin_err": fmt.Sprint(stdinErr),
		"stdin_is_devnull": st != nil && dn != nil && os.SameFile(st, dn),
		"pid":              os.Getpid(), "pgid": syscall.Getpgrp(), "parent_pgid": parentPgid,
		"report": string(report),
	}
	b, _ := json.Marshal(rec)
	_ = os.MkdirAll(filepath.Dir(expand(kv["dest"])), 0o700)
	publish(expand(kv["dest"]), string(b))
}

// exitOnSignal makes INT, TERM and HUP end the process with 128+signo even if
// the test run inherited one of them as ignored (a backgrounded shell does
// that), so a forwarded or delivered signal always ends the fake.
func exitOnSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		s := <-ch
		os.Exit(128 + int(s.(syscall.Signal)))
	}()
}

func init() { helperBehaviors["count-signals"] = countSignals }

// countSignals records every signal it receives (one per line) and exits when
// it sees TERM.
func countSignals(kv map[string]string, _ []string) {
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	publish(kv["started"], strconv.Itoa(os.Getpid()))
	var got []string
	for s := range ch {
		got = append(got, s.String())
		if s == syscall.SIGTERM {
			break
		}
	}
	publish(kv["file"], strings.Join(got, "\n"))
	os.Exit(0)
}

func init() { helperBehaviors["probe-pid"] = probePID }

// probePID reports whether the process whose pid is in kv["pidfile"] is still
// alive, as "alive" or "dead", into kv["dest"], and exits kv["code"].
func probePID(kv map[string]string, _ []string) {
	b, _ := os.ReadFile(kv["pidfile"])
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	state := "dead"
	if pid > 0 && syscall.Kill(pid, 0) == nil {
		state = "alive"
	}
	publish(kv["dest"], state)
	os.Exit(atoi(kv["code"], 0))
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
