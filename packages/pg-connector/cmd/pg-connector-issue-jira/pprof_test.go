package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pprofEnvOf(v string) func(string) string {
	return func(k string) string {
		if k == pprofEnv {
			return v
		}
		return ""
	}
}

func TestPprofEnvNameIsThisDaemons(t *testing.T) {
	if pprofEnv != "PG_CONNECTOR_ISSUE_JIRA_PPROF_ADDR" {
		t.Errorf("pprofEnv = %q", pprofEnv)
	}
}

func TestStartPprofFromEnvUnsetIsSilent(t *testing.T) {
	var out bytes.Buffer
	stop := startPprofFromEnv(pprofEnvOf(""), &out)
	stop()
	if out.Len() != 0 {
		t.Fatalf("unset %s wrote %q, want nothing", pprofEnv, out.String())
	}
}

func TestStartPprofFromEnvRejectedAddressDoesNotStopTheDaemon(t *testing.T) {
	var out bytes.Buffer
	stop := startPprofFromEnv(pprofEnvOf("0.0.0.0:6060"), &out)
	stop()
	if !strings.Contains(out.String(), pprofEnv+" ignored") {
		t.Fatalf("rejection was not reported: %q", out.String())
	}
}

func TestStartPprofFromEnvLoopbackServesAndStops(t *testing.T) {
	var out bytes.Buffer
	stop := startPprofFromEnv(pprofEnvOf("127.0.0.1:0"), &out)
	const prefix = "daemon: pprof listening on "
	line := out.String()
	if !strings.HasPrefix(line, prefix) {
		stop()
		t.Fatalf("no listening line: %q", line)
	}
	addr := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	resp, err := http.Get("http://" + addr + "/debug/pprof/")
	if err != nil {
		stop()
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		stop()
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	stop()
	if _, err := http.Get("http://" + addr + "/debug/pprof/"); err == nil {
		t.Error("the listener must be closed after stop")
	}
}

func TestStartPprofFromEnvCreatesTheSocketParentDirectory(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "jp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "nested", "pprof.sock")
	var out bytes.Buffer
	stop := startPprofFromEnv(pprofEnvOf(sock), &out)
	defer stop()
	if fi, err := os.Stat(filepath.Dir(sock)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("parent directory: %v %v, want 0700 (output: %q)", fi, err, out.String())
	}
}
