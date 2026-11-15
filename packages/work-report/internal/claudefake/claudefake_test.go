package claudefake_test

import (
	"bytes"
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/claudefake"
)

// The in-process Fake MUST answer exactly as the installed script does, so a
// test may use either: the same behavior and call give the same output, the
// same failure text and the same recorded call.
func TestFakeAnswersLikeTheInstalledScript(t *testing.T) {
	for _, b := range []claudefake.Behavior{
		{Stdout: "ok"},
		{Stdout: "partial", Stderr: "rate limited", Exit: 3},
		{},
	} {
		rec := claudefake.Install(t, b)
		args := []string{"-p", "--append-system-prompt", "multi\nline", "--allowed-tools", ""}
		cmd := exec.CommandContext(context.Background(), "claude", args...)
		cmd.Stdin = strings.NewReader("in\nput")
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		realErr := cmd.Run()

		f := claudefake.NewFake(b)
		stdout, stderr, err := f.Run(context.Background(), "", "", args, "in\nput")
		if stdout != so.String() || stderr != se.String() {
			t.Errorf("%+v: in-process %q %q, script %q %q", b, stdout, stderr, so.String(), se.String())
		}
		if (err == nil) != (realErr == nil) || (err != nil && err.Error() != realErr.Error()) {
			t.Errorf("%+v: in-process error %v, script error %v", b, err, realErr)
		}
		if !reflect.DeepEqual(f.Calls(), rec.Calls()) {
			t.Errorf("%+v: calls in-process %+v, script %+v", b, f.Calls(), rec.Calls())
		}
	}
}

func TestFakeFailsOnADoneContextWithoutRecording(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := claudefake.NewFake(claudefake.Behavior{Stdout: "ok"})
	if _, _, err := f.Run(ctx, "", "", nil, ""); err == nil {
		t.Fatal("want the context error")
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("calls = %+v, want none", f.Calls())
	}
}
