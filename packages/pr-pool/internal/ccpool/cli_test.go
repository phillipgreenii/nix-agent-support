package ccpool

import (
	"context"
	"reflect"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

var _ Runner = (*CLIRunner)(nil)

func newSpy() (*CLIRunner, *[][]string, func(out []byte)) {
	var got [][]string
	var canned []byte
	cli := NewCLIRunner(config.Default())
	cli.run = func(args []string) ([]byte, error) {
		got = append(got, args)
		return canned, nil
	}
	setOut := func(out []byte) { canned = out }
	return cli, &got, setOut
}

func TestEnsure_argv(t *testing.T) {
	cli, got, _ := newSpy()
	err := cli.Ensure(context.Background(), "pr-pool-worker-zr-1", "/repo",
		map[string]string{"WORKSPACE_ROOT": "/repo", "BEADS_ACTOR": "pgii-pool__worker", "BEADS_DIR": "/repo/.beads"})
	if err != nil {
		t.Fatal(err)
	}
	// env keys sorted: BEADS_ACTOR, BEADS_DIR, WORKSPACE_ROOT
	want := []string{
		"new", "pr-pool-worker-zr-1", "--cwd", "/repo",
		"--env", "BEADS_ACTOR=pgii-pool__worker",
		"--env", "BEADS_DIR=/repo/.beads",
		"--env", "WORKSPACE_ROOT=/repo",
		"--dangerously-skip-permissions", "--effort", "max",
	}
	if !reflect.DeepEqual((*got)[0], want) {
		t.Errorf("argv =\n %v\nwant\n %v", (*got)[0], want)
	}
}

func TestEnsure_argv_withModel_noDangerous(t *testing.T) {
	var got [][]string
	cfg := config.Default()
	cfg.Model = "claude-opus-4-8"
	cfg.Dangerous = false
	cfg.Effort = "high"
	cli := NewCLIRunner(cfg)
	cli.run = func(args []string) ([]byte, error) { got = append(got, args); return nil, nil }
	if err := cli.Ensure(context.Background(), "s", "/r", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"new", "s", "--cwd", "/r", "--effort", "high", "--model", "claude-opus-4-8"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v, want %v", got[0], want)
	}
}

func TestSend_modes(t *testing.T) {
	cases := []struct {
		mode SendMode
		flag string
	}{
		{ModeNoWait, "--no-wait"},
		{ModeInterrupt, "--interrupt"},
		{ModeQueue, "--queue-message"},
	}
	for _, tc := range cases {
		cli, got, _ := newSpy()
		if err := cli.Send(context.Background(), "s", "hello world", tc.mode); err != nil {
			t.Fatal(err)
		}
		want := []string{"reply", "s", "hello world", tc.flag}
		if !reflect.DeepEqual((*got)[0], want) {
			t.Errorf("mode %d argv = %v, want %v", tc.mode, (*got)[0], want)
		}
	}
}

func TestCancelCloseList_argv(t *testing.T) {
	cli, got, setOut := newSpy()
	_ = cli.Cancel(context.Background(), "s")
	_ = cli.Close(context.Background(), "s")
	setOut([]byte(`[{"name":"pr-pool-worker-zr-1","state":"working","live":true,"transcript_path":"/t/x.jsonl"}]`))
	sessions, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual((*got)[0], []string{"cancel", "s"}) {
		t.Errorf("cancel argv = %v", (*got)[0])
	}
	if !reflect.DeepEqual((*got)[1], []string{"close", "s"}) {
		t.Errorf("close argv = %v", (*got)[1])
	}
	if !reflect.DeepEqual((*got)[2], []string{"list", "--all", "--json"}) {
		t.Errorf("list argv = %v", (*got)[2])
	}
	if len(sessions) != 1 || sessions[0].Name != "pr-pool-worker-zr-1" ||
		sessions[0].State != StateWorking || !sessions[0].Live ||
		sessions[0].TranscriptPath != "/t/x.jsonl" {
		t.Errorf("parsed session = %+v", sessions)
	}
}
