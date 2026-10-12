package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(v string) func(string) string {
	return func(k string) string {
		if k == pprofEnv {
			return v
		}
		return ""
	}
}

func TestStartPprofFromEnvUnsetIsSilent(t *testing.T) {
	var out bytes.Buffer
	stop := startPprofFromEnv(envOf(""), &out)
	stop()
	if out.Len() != 0 {
		t.Fatalf("unset %s wrote %q, want nothing", pprofEnv, out.String())
	}
}

func TestStartPprofFromEnvRejectedAddressDoesNotStopTheDaemon(t *testing.T) {
	var out bytes.Buffer
	stop := startPprofFromEnv(envOf("0.0.0.0:6060"), &out)
	stop() // must be callable and harmless
	if !strings.Contains(out.String(), pprofEnv+" ignored") {
		t.Fatalf("rejection was not reported: %q", out.String())
	}
}

func TestStartPprofFromEnvServesAndStops(t *testing.T) {
	var out bytes.Buffer
	stop := startPprofFromEnv(envOf("127.0.0.1:0"), &out)
	line := out.String()
	const prefix = "daemon: pprof listening on "
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
		t.Fatal("still serving after stop")
	}
}

func TestStartPprofFromEnvUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "pp")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sock := filepath.Join(dir, "pprof.sock")

	var out bytes.Buffer
	stop := startPprofFromEnv(envOf(sock), &out)
	defer stop()
	if !strings.Contains(out.String(), sock) {
		t.Fatalf("listening line does not name the socket: %q", out.String())
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("socket not created: %v", err)
	}
}
