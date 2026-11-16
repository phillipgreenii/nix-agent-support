package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// A scrape reads the files without any repository lock, and a refusal caused
// by a lock timeout shows up in refusals_total.
func TestExporterNeverTakesLockAndCountsBusy(t *testing.T) {
	c := newCLI(t)
	rp, err := repo.Resolve(context.Background(), c.a.git, c.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pg-gwm", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[repos.%q]\nlock-wait = \"1s\"\n", rp.Main)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := c.run("lease", "--name", "one", "--detach"); code != 0 {
		t.Fatalf("lease: %d %s", code, errOut)
	}

	lk, err := state.Open(rp.CommonDir).Lock(t.Context(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Unlock()
	if code, _, _ := c.run("lease", "--name", "two", "--detach"); code != 9 {
		t.Fatalf("lease against a held lock exit %d, want 9", code)
	}

	srv := httptest.NewServer(c.a.metricsHandler())
	defer srv.Close()
	done := make(chan struct{})
	var code int
	var ctype, body string
	go func() {
		defer close(done)
		code, ctype, body = get(t, srv.URL+"/metrics")
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the scrape blocked on the repository lock")
	}
	if code != 200 || !strings.HasPrefix(ctype, "text/plain") {
		t.Fatalf("scrape: %d %q", code, ctype)
	}
	for _, w := range []string{
		fmt.Sprintf("pg_gwm_worktrees{repo=%q,status=\"leased\"} 1", rp.Label),
		fmt.Sprintf("pg_gwm_refusals_total{repo=%q,error=\"busy\"} 1", rp.Label),
		"pg_gwm_build_info{version=",
		"pg_gwm_config_valid 1",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q in:\n%s", w, body)
		}
	}
	// No reap ran: no reaper series.
	if strings.Contains(body, "reaper_last_success_age_seconds") {
		t.Errorf("reaper series without a reap:\n%s", body)
	}
}
