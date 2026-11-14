//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The real ps on a real descendant: the sample must list it and, since it is
// not ccpool, name no component.
func TestSampleProcessTree_realPsSeesAChildOfThisProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip("cannot start sleep:", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	s := sampleProcessTree(context.Background(), os.Getpid())
	found := false
	for _, p := range s.Procs {
		if strings.HasPrefix(p, fmt.Sprintf("%d ", cmd.Process.Pid)) {
			found = true
		}
	}
	if !found || s.Stuck != "none" {
		t.Fatalf("sample = %+v, want the child %d listed and stuck none", s, cmd.Process.Pid)
	}
}
