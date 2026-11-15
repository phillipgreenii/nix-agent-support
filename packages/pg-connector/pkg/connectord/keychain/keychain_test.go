package keychain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const fixtureToken = "FIXTURE-TOKEN-do-not-use-0001"

func TestFakeScriptedInOrderAndCounts(t *testing.T) {
	boom := errors.New("boom")
	f := NewFake(Result{Token: "a"}, Result{Err: boom}, Result{Token: "b"})
	if f.Calls() != 0 {
		t.Fatalf("calls = %d, want 0", f.Calls())
	}
	if tok, err := f.Password(context.Background(), "svc", "acc"); tok != "a" || err != nil {
		t.Fatalf("call 1 = %q, %v", tok, err)
	}
	if _, err := f.Password(context.Background(), "svc2", "acc2"); !errors.Is(err, boom) {
		t.Fatalf("call 2 err = %v", err)
	}
	if tok, _ := f.Password(context.Background(), "s", "a"); tok != "b" {
		t.Fatalf("call 3 = %q", tok)
	}
	if f.Calls() != 3 {
		t.Fatalf("calls = %d, want 3", f.Calls())
	}
	if _, err := f.Password(context.Background(), "s", "a"); KindOf(err) != KindOther {
		t.Fatalf("past script err = %v", err)
	}
	if f.Calls() != 4 {
		t.Fatalf("calls = %d, want 4", f.Calls())
	}
	if s, a := f.LastRequest(); s != "s" || a != "a" {
		t.Fatalf("last = %q %q", s, a)
	}
	if KindOf(Fail(KindLocked).Err) != KindLocked {
		t.Fatal("Fail kind")
	}
}

func TestDefaults(t *testing.T) {
	if SecurityPath != "/usr/bin/security" || DefaultTimeout != 10*time.Second {
		t.Fatal("defaults changed")
	}
}

// answer returns a RunFunc that records its call and replies as scripted.
func answer(gotPath *string, gotArgs *[]string, out string, code int, err error) RunFunc {
	return func(_ context.Context, path string, args []string) ([]byte, int, error) {
		*gotPath, *gotArgs = path, args
		return []byte(out), code, err
	}
}

func TestSecuritySuccessTrimsAndPassesArgs(t *testing.T) {
	var path string
	var args []string
	s := Security{Path: "/injected/security", Run: answer(&path, &args, "  tok \n", 0, nil)}
	got, err := s.Password(context.Background(), "svc", "acc")
	if err != nil || got != "tok" {
		t.Fatalf("got %q, %v", got, err)
	}
	if path != "/injected/security" {
		t.Fatalf("path = %q", path)
	}
	if want := "find-generic-password -a acc -s svc -w"; strings.Join(args, " ") != want {
		t.Fatalf("args = %v", args)
	}
}

func TestSecurityDefaultPathIsAbsoluteSecurity(t *testing.T) {
	var path string
	var args []string
	_, _ = Security{Run: answer(&path, &args, "x", 0, nil)}.Password(context.Background(), "s", "a")
	if path != "/usr/bin/security" {
		t.Fatalf("path = %q", path)
	}
}

func TestSecurityExitCodeKinds(t *testing.T) {
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
			var p string
			var a []string
			_, err := Security{Run: answer(&p, &a, "", c.code, nil)}.Password(context.Background(), "svc", "acc")
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

func TestSecurityHungRunTimesOut(t *testing.T) {
	hang := func(ctx context.Context, _ string, _ []string) ([]byte, int, error) {
		<-ctx.Done()
		return nil, -1, ctx.Err()
	}
	start := time.Now()
	_, err := Security{Timeout: 50 * time.Millisecond, Run: hang}.Password(context.Background(), "svc", "acc")
	if KindOf(err) != KindTimeout {
		t.Fatalf("err = %v, want timeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestSecurityStartFailureIsOther(t *testing.T) {
	var p string
	var a []string
	_, err := Security{Run: answer(&p, &a, "", -1, errors.New("fork/exec: "+fixtureToken))}.Password(context.Background(), "s", "a")
	if KindOf(err) != KindOther || leaks(err) {
		t.Fatalf("err = %v", err)
	}
}

// leaks reports whether any error text in the chain contains the fixture token.
func leaks(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if strings.Contains(err.Error(), fixtureToken) {
			return true
		}
	}
	return false
}

// A run that "prints" the token on stdout and stderr and exits non-zero: the
// adapter must keep both out of the error.
func leakyRun(_ context.Context, _ string, _ []string) ([]byte, int, error) {
	return []byte(fixtureToken), 44, nil
}

func TestLeakNoTokenInErrors(t *testing.T) {
	_, err := Security{Run: leakyRun}.Password(context.Background(), "svc", "acc")
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

// leakyPassword is the deliberately leaking negative control: the same read,
// but it folds the tool's output into the error.
func leakyPassword(run RunFunc) error {
	out, code, _ := run(context.Background(), SecurityPath, nil)
	if code != 0 {
		return fmt.Errorf("keychain read failed: exit %d: %s", code, out)
	}
	return nil
}

func TestLeakNegativeControl(t *testing.T) {
	if !leaks(leakyPassword(leakyRun)) {
		t.Fatal("negative control: the leaking variant must trip the leak check")
	}
}
