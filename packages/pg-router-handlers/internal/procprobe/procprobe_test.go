package procprobe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

func TestHeld(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		cwds []string
		dir  string
		want bool
	}{
		{"exact", []string{dir}, dir, true},
		{"inside", []string{filepath.Join(dir, "a", "b")}, dir, true},
		{"sibling prefix", []string{dir + "-other"}, dir, false},
		{"parent", []string{filepath.Dir(dir)}, dir, false},
		{"none", nil, dir, false},
		{"resolved form of a symlinked dir", []string{dir}, link, true},
	}
	for _, tc := range cases {
		if got := Held(tc.cwds, tc.dir); got != tc.want {
			t.Errorf("%s: Held = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParse(t *testing.T) {
	got := Parse([]byte("p1\nn/a\nf\nn/b c\nn\n"))
	if want := []string{"/a", "/b c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %v, want %v", got, want)
	}
}

func TestKeep(t *testing.T) {
	ctx := context.Background()
	if keep, err := Keep(ctx, func(context.Context) ([]string, error) { return nil, nil }, "/x"); keep || err != nil {
		t.Errorf("no holder: keep=%v err=%v", keep, err)
	}
	if keep, _ := Keep(ctx, func(context.Context) ([]string, error) { return []string{"/x/y"}, nil }, "/x"); !keep {
		t.Error("a holder must keep")
	}
	if keep, err := Keep(ctx, func(context.Context) ([]string, error) { return nil, errors.New("boom") }, "/x"); !keep || err == nil {
		t.Errorf("a failed probe must keep and report: keep=%v err=%v", keep, err)
	}
}

func TestCWDsWith_parsesAndTrustsListingDespiteExit(t *testing.T) {
	f := cmdruntest.New(t)
	f.On(cmdruntest.Any).Return(cmdrun.Result{Stdout: []byte("p1\nn/a/b\np2\nn/c\n"), Code: 1})
	got, err := CWDsWith(context.Background(), f.Runner())
	if err != nil || len(got) != 2 || got[0] != "/a/b" {
		t.Fatalf("got %v err %v", got, err)
	}
	if c := f.Calls()[0]; c.Name != "lsof" || c.Timeout != Timeout {
		t.Fatalf("call %+v", c)
	}
}

func TestCWDsWith_emptyListingIsAFailure(t *testing.T) {
	for name, res := range map[string]cmdrun.Result{
		"exit":    cmdruntest.Fail(1, ""),
		"start":   {Code: -1, StartErr: errors.New("no lsof")},
		"timeout": {Code: -1, Signal: "killed", CtxErr: context.DeadlineExceeded, TimedOut: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := cmdruntest.New(t)
			f.On(cmdruntest.Any).Return(res)
			if got, err := CWDsWith(context.Background(), f.Runner()); err == nil {
				t.Fatalf("got %v, want an error", got)
			}
		})
	}
}
