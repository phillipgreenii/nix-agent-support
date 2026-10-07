package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// Mid-review head-moved check (bead pg2-a9yhn). All commits are placeholders.

const (
	headCheckOld = "4b1d7aabbbb0"
	headCheckNew = "9f3c1e2aaaa0"
)

func withHeadCheckConfig(t *testing.T) {
	t.Helper()
	orig := deskConfigLoad
	t.Cleanup(func() { deskConfigLoad = orig })
	deskConfigLoad = func(context.Context) (*config.Config, error) { return openTestConfig("o/r"), nil }
}

// runHeadCheckCmd runs a fresh head-check command with args and returns its
// stdout, the error and the process exit code the error maps to.
func runHeadCheckCmd(t *testing.T, args ...string) (stdout string, err error, code int) {
	t.Helper()
	var out bytes.Buffer
	c := newHeadCheckCmd()
	c.SetOut(&out)
	c.SetErr(&bytes.Buffer{})
	c.SetArgs(args)
	err = c.ExecuteContext(context.Background())
	code = 0
	if err != nil {
		code = exitCodeFor(err)
	}
	return out.String(), err, code
}

func TestHeadCheckRegisteredUnderPR(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"pr", "head-check"})
	if err != nil || c.Name() != "head-check" || c.Parent() != typeGroup(entityTypePR) {
		t.Fatalf("pr head-check not registered: %v, %v", c, err)
	}
}

// fakeShow installs a pg-connector whose `pr show` prints a PR at head with
// the given exit code, recording the argv it was called with.
func fakeShow(t *testing.T, head string, exit int) (argvFile string) {
	t.Helper()
	argvFile = filepath.Join(t.TempDir(), "argv.log")
	body := fmt.Sprintf("echo \"$*\" >> %q\n", argvFile)
	if exit == 0 {
		body += fmt.Sprintf(`echo '{"protocolVersion":1,"schemaVersion":4,"result":{"repo":"o/r","number":5,"state":"open","head_sha":%q}}'`, head) + "\nexit 0"
	} else {
		body += `echo '{"protocolVersion":1,"error":{"code":"unavailable","message":"offline"}}'` + fmt.Sprintf("\nexit %d", exit)
	}
	installFakePGConnector(t, body)
	return argvFile
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestHeadCheck_UnchangedExitsZero(t *testing.T) {
	withHeadCheckConfig(t)
	argv := fakeShow(t, headCheckOld, 0)
	out, err, code := runHeadCheckCmd(t, "5", "--head-sha", headCheckOld)
	if err != nil || code != 0 {
		t.Fatalf("err = %v, code = %d; want success", err, code)
	}
	if !strings.Contains(out, "head unchanged") || !strings.Contains(out, headCheckOld) {
		t.Errorf("stdout = %q, want it to say the head is unchanged", out)
	}
	// It must ask for a FRESH read: a cached head cannot say whether it moved.
	if got := readFile(t, argv); !strings.Contains(got, "pr show o/r#5 --fresh") {
		t.Errorf("pg-connector argv = %q, want `pr show o/r#5 --fresh`", got)
	}
}

func TestHeadCheck_MovedExitsFour(t *testing.T) {
	withHeadCheckConfig(t)
	fakeShow(t, headCheckNew, 0)
	_, err, code := runHeadCheckCmd(t, "5", "--head-sha", headCheckOld)
	if err == nil || code != exitHeadMoved {
		t.Fatalf("err = %v, code = %d; want exit %d", err, code, exitHeadMoved)
	}
	for _, want := range []string{"head moved", headCheckOld, headCheckNew, "stale"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err.Error(), want)
		}
	}
}

func TestHeadCheck_AbbreviatedHeadMatches(t *testing.T) {
	withHeadCheckConfig(t)
	fakeShow(t, headCheckOld, 0)
	if _, err, code := runHeadCheckCmd(t, "5", "--head-sha", headCheckOld[:8]); err != nil || code != 0 {
		t.Fatalf("an abbreviated head of the current commit: err = %v, code = %d; want success", err, code)
	}
	// ... but a too-short prefix never matches.
	if _, err, code := runHeadCheckCmd(t, "5", "--head-sha", headCheckOld[:3]); err == nil || code != exitHeadMoved {
		t.Fatalf("a 3-character prefix: err = %v, code = %d; want exit %d", err, code, exitHeadMoved)
	}
}

func TestHeadCheck_LookupFailureExitsOne(t *testing.T) {
	withHeadCheckConfig(t)
	fakeShow(t, "", 1)
	_, err, code := runHeadCheckCmd(t, "5", "--head-sha", headCheckOld)
	if err == nil || code != 1 {
		t.Fatalf("err = %v, code = %d; want exit 1 (nothing known about the head)", err, code)
	}
}

func TestHeadCheck_NotFoundExitsOne(t *testing.T) {
	withHeadCheckConfig(t)
	fakeShow(t, "", 4)
	_, err, code := runHeadCheckCmd(t, "5", "--head-sha", headCheckOld)
	if err == nil || code != 1 || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, code = %d; want exit 1 with a not-found message", err, code)
	}
}

func TestHeadCheck_RequiresHeadSHA(t *testing.T) {
	withHeadCheckConfig(t)
	if _, err, _ := runHeadCheckCmd(t, "5"); err == nil {
		t.Fatal("head-check without --head-sha succeeded")
	}
	if _, err, _ := runHeadCheckCmd(t, "5", "--head-sha", "  "); err == nil {
		t.Fatal("head-check with a blank --head-sha succeeded")
	}
}

func TestSameCommit(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"abcdef1234", "abcdef1234", true},
		{"ABCDEF1234", "abcdef1234", true},
		{"abcdef1", "abcdef1234", true},
		{"abcdef1234", "abcdef1", true},
		{"abcdef", "abcdef1234", false}, // 6 characters is below the prefix floor
		{"abcdef1234", "abcdef9999", false},
		{"", "abcdef1234", false},
		{"", "", true},
	} {
		if got := sameCommit(tc.a, tc.b); got != tc.want {
			t.Errorf("sameCommit(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
