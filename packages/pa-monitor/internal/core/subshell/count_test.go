package subshell

import "testing"

func TestCountParsesPgrepOutput(t *testing.T) {
	c := &Counter{
		RunPs: func(parent int) (string, error) {
			if parent != 42 {
				t.Fatalf("unexpected parent %d", parent)
			}
			return "100 zsh\n101 bash\n102 ssh-agent\n103 /bin/sh\n", nil
		},
	}
	n, err := c.Count(42)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("Count = %d, want 3 (zsh+bash+sh)", n)
	}
}

func TestCountShellsFakeTable(t *testing.T) {
	procs := []Proc{
		{PID: 10, PPID: 1, Comm: "claude"},
		{PID: 11, PPID: 10, Comm: "zsh"},
		{PID: 12, PPID: 10, Comm: "bash"},
		{PID: 13, PPID: 10, Comm: "sh"},
		{PID: 14, PPID: 10, Comm: "sleep"},   // non-shell child
		{PID: 15, PPID: 10, Comm: "ssh"},     // not sh
		{PID: 16, PPID: 11, Comm: "zsh"},     // grandchild
		{PID: 17, PPID: 12, Comm: "bash"},    // grandchild
		{PID: 20, PPID: 1, Comm: "zsh"},      // unrelated tree
		{PID: 30, PPID: 29, Comm: "/bin/sh"}, // path-form comm, other parent
	}
	cases := []struct {
		name   string
		procs  []Proc
		parent int
		want   int
	}{
		{"nested grandchildren not counted", procs, 10, 3},
		{"no children", procs, 99, 0},
		{"only non-shell children", []Proc{{PID: 14, PPID: 10, Comm: "sleep"}, {PID: 15, PPID: 10, Comm: "ssh"}}, 10, 0},
		{"leaf shell has no children", procs, 16, 0},
		{"empty table", nil, 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Counter{ListProcs: func() ([]Proc, error) { return tc.procs, nil }}
			got, err := c.Count(tc.parent)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Count(%d) = %d, want %d", tc.parent, got, tc.want)
			}
		})
	}
}

func TestCountShellsExitedChild(t *testing.T) {
	before := []Proc{{PID: 11, PPID: 10, Comm: "zsh"}, {PID: 12, PPID: 10, Comm: "bash"}}
	after := before[:1]
	if got := CountShells(before, 10); got != 2 {
		t.Fatalf("before exit = %d, want 2", got)
	}
	if got := CountShells(after, 10); got != 1 {
		t.Fatalf("after exit = %d, want 1", got)
	}
}
