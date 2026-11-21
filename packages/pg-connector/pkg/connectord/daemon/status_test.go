package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type fakeReport struct {
	exit    int
	textErr error
}

func (f fakeReport) WriteText(w io.Writer) error {
	_, _ = io.WriteString(w, "text\n")
	return f.textErr
}
func (f fakeReport) WriteJSON(w io.Writer) error { _, _ = io.WriteString(w, "{}\n"); return nil }
func (f fakeReport) ExitCode() int               { return f.exit }

func runStatus(args []string, r fakeReport) (int, string, string) {
	var out, errb bytes.Buffer
	code := RunStatus(context.Background(), "d", "usage\n", args, &out, &errb, func(context.Context) StatusReport { return r })
	return code, out.String(), errb.String()
}

func TestRunStatusTextAndJSONExitByHealth(t *testing.T) {
	code, out, _ := runStatus(nil, fakeReport{exit: 5})
	if code != 5 || out != "text\n" {
		t.Fatalf("text: code=%d out=%q", code, out)
	}
	for _, flag := range []string{"--json", "-json"} {
		code, out, _ = runStatus([]string{flag}, fakeReport{exit: 3})
		if code != 3 || out != "{}\n" {
			t.Fatalf("%s: code=%d out=%q", flag, code, out)
		}
	}
}

func TestRunStatusUnknownArgumentIsUsage(t *testing.T) {
	code, out, errs := runStatus([]string{"--bogus"}, fakeReport{})
	if code != ExitUsage || out != "" || !strings.Contains(errs, `d status: unknown argument "--bogus"`) || !strings.HasSuffix(errs, "usage\n") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestRunStatusWriteFailureExitsOne(t *testing.T) {
	code, _, errs := runStatus(nil, fakeReport{textErr: errors.New("boom")})
	if code != 1 || !strings.Contains(errs, "d status: boom") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestFuncReportDelegates(t *testing.T) {
	var out bytes.Buffer
	r := FuncReport{
		Text: func(w io.Writer) error { _, _ = io.WriteString(w, "t"); return nil },
		JSON: func(w io.Writer) error { _, _ = io.WriteString(w, "j"); return nil },
		Code: 7,
	}
	_ = r.WriteText(&out)
	_ = r.WriteJSON(&out)
	if out.String() != "tj" || r.ExitCode() != 7 {
		t.Fatalf("out=%q code=%d", out.String(), r.ExitCode())
	}
}
