package handlerkit

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func adapter() Adapter {
	return Adapter{
		Name: "x", Version: "v1", Usage: "usage: x", Help: "help text\n",
		Commands: map[string]func([]string) int{
			"go": func(a []string) int { return len(a) + 10 },
		},
	}
}

func TestExecuteDispatch(t *testing.T) {
	cases := []struct {
		args   []string
		code   int
		out    string
		errOut string
	}{
		{nil, 2, "", "x: a subcommand is required\nusage: x\n"},
		{[]string{"help"}, 0, "help text\n", ""},
		{[]string{"-h"}, 0, "help text\n", ""},
		{[]string{"version"}, 0, "v1\n", ""},
		{[]string{"--version"}, 0, "v1\n", ""},
		{[]string{"go", "a", "b"}, 12, "", ""},
		{[]string{"nope"}, 2, "", "x: unknown subcommand \"nope\"\nusage: x\n"},
	}
	for _, c := range cases {
		var o, e bytes.Buffer
		if got := Execute(adapter(), c.args, &o, &e); got != c.code || o.String() != c.out || e.String() != c.errOut {
			t.Errorf("%v: got %d %q %q", c.args, got, o.String(), e.String())
		}
	}
}

func TestExecuteRunAndSetupOrder(t *testing.T) {
	var order []string
	a := Adapter{
		Name:  "r",
		Setup: func() { order = append(order, "setup") },
		Run: func(args []string, _, _ io.Writer) int {
			order = append(order, "run:"+strings.Join(args, ","))
			return 7
		},
	}
	if got := Execute(a, []string{"p", "q"}, io.Discard, io.Discard); got != 7 {
		t.Fatalf("code %d", got)
	}
	if strings.Join(order, "|") != "setup|run:p,q" {
		t.Fatalf("order %v", order)
	}
}
