//go:build unix

package wireclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

type pdRunner struct{ disabled bool }

func (r *pdRunner) Run(ctx context.Context, _ []string, _ []byte) ([]byte, int, error) {
	r.disabled = parentDeathDisabled(ctx)
	return []byte(`{"schemaVersion":"1","id":"x","outcome":"{}"}`), 0, nil
}

func TestClient_survivesShutdownRoleGetsNoParentDeathPipe(t *testing.T) {
	for _, survives := range []bool{false, true} {
		r := &pdRunner{}
		c := &Client{Runner: r, Command: fixedCommand("h")}
		_, _ = c.Dispatch(context.Background(), roles.Role{Name: "r", SurvivesShutdown: survives}, eventqueue.Event{ID: "e", Type: "t"})
		if r.disabled != survives {
			t.Errorf("survives=%v: parent-death disabled = %v", survives, r.disabled)
		}
	}
}

// TestOSRunner_passesParentDeathPipe runs a real sh that reports fd 3's env var
// and whether fd 3 is open, with and without WithoutParentDeath.
func TestOSRunner_passesParentDeathPipe(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "stub")
	body := "#!/bin/sh\ncat >/dev/null\nif [ -e /dev/fd/3 ]; then fd=open; else fd=closed; fi\necho \"$" + ParentDeathFDEnv + " $fd\"\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	out, _, err := OSRunner{}.Run(context.Background(), []string{stub}, nil)
	if err != nil || strings.TrimSpace(string(out)) != "3 open" {
		t.Fatalf("with pipe: out %q err %v", out, err)
	}
	out, _, err = OSRunner{}.Run(WithoutParentDeath(context.Background()), []string{stub}, nil)
	if err != nil || strings.TrimSpace(string(out)) != "closed" {
		t.Fatalf("without pipe: out %q err %v", out, err)
	}
}
