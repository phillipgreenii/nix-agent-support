package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const profileScript = "../../scripts/profile-cpu.sh"

func runScript(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{profileScript}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestProfileScriptUsageAndMissingSocket(t *testing.T) {
	if out, code := runScript(t, nil, "--help"); code != 0 || !strings.Contains(out, "cpu_percent_of_one_core") {
		t.Fatalf("help: %d %q", code, out)
	}
	if _, code := runScript(t, nil, "--bogus"); code != 2 {
		t.Fatalf("unknown arg exit %d", code)
	}
	if _, code := runScript(t, nil, "--seconds", "ten"); code != 2 {
		t.Fatalf("bad seconds exit %d", code)
	}
	out, code := runScript(t, nil, "--sock", filepath.Join(t.TempDir(), "absent.sock"))
	if code != 3 || !strings.Contains(out, "no pprof socket") {
		t.Fatalf("missing socket: %d %q", code, out)
	}
}

// TestProfileScriptReportsTheCPUFigureFromPprofOutput drives the script with a
// stub curl and a stub pprof (the real recipe needs a daemon, which the live
// check covers) and proves the figure is the percentage pprof prints.
func TestProfileScriptReportsTheCPUFigureFromPprofOutput(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ccrec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	stub := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	curl := stub("curl", `out=""; while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done; echo profile-bytes >"$out"`)
	pprof := stub("pprof", `echo "File: pg-claude-code-recorder"; echo "Duration: 5.01s, Total samples = 60ms ( 1.20%)"`)
	out, code := runScript(t, []string{"CURL=" + curl, "PPROF=" + pprof}, "--sock", sock, "--seconds", "5", "--out", filepath.Join(dir, "o"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, want := range []string{"window_seconds=5", "cpu_percent_of_one_core=1.20", "profile=" + filepath.Join(dir, "o", "cpu.pb.gz")} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "o", "cpu.pb.gz")); err != nil {
		t.Fatal(err)
	}
}
