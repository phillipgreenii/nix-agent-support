package query

import (
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

func TestOSParticipantRunner_PassesCallAndMapsResult(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdrun.Result{Stdout: []byte("body"), Code: 9})
	out, code, err := OSParticipantRunner{Exec: fake.Runner()}.Run(context.Background(), []string{"p", "query"}, []byte("in"))
	if err != nil || string(out) != "body" || code != 9 {
		t.Fatalf("out=%q code=%d err=%v", out, code, err)
	}
	c := fake.Calls()[0]
	if c.Name != "p" || len(c.Args) != 1 || string(c.Stdin) != "in" || c.Timeout != participantTimeout {
		t.Fatalf("call = %+v", c)
	}
}

func TestOSParticipantRunner_Failures(t *testing.T) {
	for name, tc := range map[string]struct {
		res  cmdrun.Result
		want string
	}{
		"start":   {cmdrun.Result{Code: -1, StartErr: context.Canceled}, "query: run"},
		"timeout": {cmdrun.Result{Code: -1, TimedOut: true, CtxErr: context.DeadlineExceeded}, "timed out"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := cmdruntest.New(t)
			fake.On(cmdruntest.Any).Return(tc.res)
			_, _, err := OSParticipantRunner{Exec: fake.Runner()}.Run(context.Background(), []string{"p"}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if _, _, err := (OSParticipantRunner{}).Run(context.Background(), nil, nil); err == nil {
		t.Fatal("empty argv must be an error")
	}
}
