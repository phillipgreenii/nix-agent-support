package query

import (
	"context"
	"strings"
	"testing"
)

func TestOSCommanderRunEnv_UsesExactlyTheGivenEnv(t *testing.T) {
	t.Setenv("QUERY_TEST_INHERITED", "yes")
	out, err := OSCommander{}.RunEnv(context.Background(), []string{"/usr/bin/env"}, []string{"ONLY=1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "ONLY=1" {
		t.Fatalf("env = %q, want only ONLY=1", got)
	}
}

func TestOSCommanderRun_StillInheritsEnv(t *testing.T) {
	t.Setenv("QUERY_TEST_INHERITED", "yes")
	out, err := OSCommander{}.Run(context.Background(), []string{"/usr/bin/env"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "QUERY_TEST_INHERITED=yes") {
		t.Fatalf("Run must keep today's inherited env; got %q", out)
	}
}

func TestOSCommander_EmptyArgv(t *testing.T) {
	if _, err := (OSCommander{}).RunEnv(context.Background(), nil, nil); err == nil {
		t.Fatal("empty argv must error")
	}
	var _ EnvCommander = OSCommander{}
}
