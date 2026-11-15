//go:build integration

package keychain

// Real-process tests: a shell script stands in for /usr/bin/security. They
// never launch the real security tool.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeExe writes a shell script standing in for /usr/bin/security.
func fakeExe(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecSuccessTrimsAndPassesArgs(t *testing.T) {
	exe := fakeExe(t, `[ "$*" = "find-generic-password -a acc -s svc -w" ] || exit 9
echo "  tok  "`)
	got, err := Security{Path: exe}.Password(context.Background(), "svc", "acc")
	if err != nil || got != "tok" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestExecExitCodeKinds(t *testing.T) {
	cases := []struct {
		code int
		want Kind
	}{
		{44, KindItemMissing},
		{36, KindLocked},
		{51, KindLocked},
		{1, KindOther},
		{128, KindOther},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.code), func(t *testing.T) {
			exe := fakeExe(t, fmt.Sprintf("exit %d", c.code))
			_, err := Security{Path: exe}.Password(context.Background(), "svc", "acc")
			var ke *Error
			if !errors.As(err, &ke) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if ke.Kind != c.want || ke.ExitCode != c.code || KindOf(err) != c.want {
				t.Fatalf("got kind=%s code=%d, want kind=%s code=%d", ke.Kind, ke.ExitCode, c.want, c.code)
			}
		})
	}
}

func TestExecHungExecutableTimesOut(t *testing.T) {
	exe := fakeExe(t, "exec sleep 30")
	start := time.Now()
	_, err := Security{Path: exe, Timeout: 100 * time.Millisecond}.Password(context.Background(), "svc", "acc")
	if KindOf(err) != KindTimeout {
		t.Fatalf("err = %v, want timeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestExecMissingExecutableIsOther(t *testing.T) {
	_, err := Security{Path: filepath.Join(t.TempDir(), "nope")}.Password(context.Background(), "s", "a")
	if KindOf(err) != KindOther {
		t.Fatalf("err = %v", err)
	}
}

// leakyExecPassword is the deliberately leaking negative control: it reads the
// same executable but folds its stderr into the error.
func leakyExecPassword(exe string) error {
	out, err := execCombined(exe)
	if err != nil {
		return fmt.Errorf("keychain read failed: %w: %s", err, out)
	}
	return nil
}

func TestExecLeakNoTokenInErrors(t *testing.T) {
	exe := fakeExe(t, fmt.Sprintf("echo %s\necho %s >&2\nexit 44", fixtureToken, fixtureToken))
	_, err := Security{Path: exe}.Password(context.Background(), "svc", "acc")
	if err == nil {
		t.Fatal("want error")
	}
	if leaks(err) || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), fixtureToken) {
		t.Fatalf("token leaked: %v", err)
	}
	if KindOf(err) != KindItemMissing {
		t.Fatalf("kind = %s", KindOf(err))
	}
}

func TestExecLeakNegativeControl(t *testing.T) {
	exe := fakeExe(t, fmt.Sprintf("echo %s\necho %s >&2\nexit 44", fixtureToken, fixtureToken))
	if !leaks(leakyExecPassword(exe)) {
		t.Fatal("negative control: the leaking variant must trip the leak check")
	}
}

func execCombined(exe string) (string, error) {
	out, err := exec.CommandContext(context.Background(), exe).CombinedOutput()
	return string(out), err
}
