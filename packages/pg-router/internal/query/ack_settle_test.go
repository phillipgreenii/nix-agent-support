package query

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type recCmd struct {
	argv [][]string
	env  [][]string
	err  error
	out  string
}

func (r *recCmd) Run(_ context.Context, argv []string) ([]byte, error) {
	r.argv = append(r.argv, argv)
	return []byte(r.out), r.err
}

func (r *recCmd) RunEnv(_ context.Context, argv []string, env []string) ([]byte, error) {
	r.argv = append(r.argv, argv)
	r.env = append(r.env, env)
	return []byte(r.out), r.err
}

func TestCommandQueryReadsAckHandle(t *testing.T) {
	cmd := &recCmd{out: `{"id":"1","type":"t","ack":{"lane":"L","token":"T"}}
{"id":"2","type":"t"}
{"id":"3","type":"t","ack":{"lane":"L"}}
`}
	q := CommandQuery{Meta: Meta{EmitTypes: []string{"e"}}, Argv: []string{"feed"}, Format: FormatJSONL}
	evts, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if evts[0].Ack == nil || evts[0].Ack.Lane != "L" || evts[0].Ack.Token != "T" {
		t.Fatalf("ack = %+v", evts[0].Ack)
	}
	if evts[1].Ack != nil || evts[2].Ack != nil {
		t.Fatalf("absent or malformed ack must be nil: %+v %+v", evts[1].Ack, evts[2].Ack)
	}
}

func TestRunSettleAppendsLaneAndTokenAndUsesTheQueryEnvironment(t *testing.T) {
	cmd := &recCmd{}
	q := CommandQuery{Argv: []string{"feed"}, Settle: []string{"settle", "--db", "x"}, EnvAllow: []string{"MY_VAR_X"}}
	t.Setenv("MY_VAR_X", "1")
	if err := RunSettle(context.Background(), Env{Cmd: cmd}, q, "lane-1", "tok-9"); err != nil {
		t.Fatal(err)
	}
	want := []string{"settle", "--db", "x", "--lane", "lane-1", "--through", "tok-9"}
	if !slices.Equal(cmd.argv[0], want) {
		t.Fatalf("argv = %v, want %v", cmd.argv[0], want)
	}
	if !slices.Contains(cmd.env[0], "MY_VAR_X=1") {
		t.Fatalf("settle did not run under the query's own environment rules: %v", cmd.env[0])
	}
	if q.Settle[2] != "x" || len(q.Settle) != 3 {
		t.Fatal("RunSettle mutated the declared settle command")
	}
}

func TestRunSettleReportsAFailedRun(t *testing.T) {
	cmd := &recCmd{err: errors.New("exit status 3")}
	q := CommandQuery{Argv: []string{"feed"}, Settle: []string{"settle"}}
	if err := RunSettle(context.Background(), Env{Cmd: cmd}, q, "l", "t"); err == nil {
		t.Fatal("a failing settle must return an error")
	}
	if err := RunSettle(context.Background(), Env{Cmd: cmd}, CommandQuery{Argv: []string{"feed"}}, "l", "t"); err == nil {
		t.Fatal("a query without settle must refuse")
	}
}
