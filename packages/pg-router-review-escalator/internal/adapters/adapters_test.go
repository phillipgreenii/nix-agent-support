package adapters

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-review-escalator/internal/escalate"
)

// call is one recorded Runner invocation.
type call struct {
	Name  string
	Args  []string
	Stdin []byte
	Env   []string
}

// fakeRunner answers every call with the next scripted result and records it.
type fakeRunner struct {
	calls   []call
	results []Result
	err     error
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, stdin []byte, env []string) (Result, error) {
	f.calls = append(f.calls, call{Name: name, Args: args, Stdin: stdin, Env: env})
	if f.err != nil {
		return Result{}, f.err
	}
	if len(f.results) == 0 {
		return Result{}, nil
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r, nil
}

func ok(stdout string) Result { return Result{Stdout: []byte(stdout)} }

func TestListOpenArgsAndDecode(t *testing.T) {
	f := &fakeRunner{results: []Result{ok(`{"entities":[{"id":"bd-1","title":"t","labels":["human"],"metadata":{"review_escalation_key":"pr:x"}}],"present_ids":[],"sources":[]}`)}}
	tr := ConnectorTracker{Runner: f, ListQuery: "my-query", Backend: "my-backend", Binary: "pgc"}
	got, err := tr.ListOpen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"issue", "list", "--query", "my-query", "--backend", "my-backend", "--output", "json"}
	if f.calls[0].Name != "pgc" || !reflect.DeepEqual(f.calls[0].Args, want) {
		t.Errorf("call = %+v", f.calls[0])
	}
	if len(got) != 1 || got[0].ID != "bd-1" || got[0].Metadata[escalate.KeyKey] != "pr:x" || got[0].Labels[0] != "human" {
		t.Errorf("issues = %+v", got)
	}
}

func TestTrackerDefaults(t *testing.T) {
	f := &fakeRunner{results: []Result{ok(`{"entities":[]}`)}}
	if _, err := (ConnectorTracker{Runner: f}).ListOpen(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	if c.Name != DefaultConnectorBinary || !strings.Contains(strings.Join(c.Args, " "), DefaultListQuery) || !strings.Contains(strings.Join(c.Args, " "), DefaultTrackerBackend) {
		t.Errorf("defaults not applied: %+v", c)
	}
}

func TestListOpenFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		f    *fakeRunner
		want string
	}{
		{"degraded exit 2 is NOT accepted", &fakeRunner{results: []Result{{ExitCode: 2, Stdout: []byte(`{"entities":[]}`)}}}, "exit 2"},
		{"exit 1 with stderr", &fakeRunner{results: []Result{{ExitCode: 1, Stderr: []byte("backend down")}}}, "backend down"},
		{"exit 1 with stdout only", &fakeRunner{results: []Result{{ExitCode: 1, Stdout: []byte(`{"error":{}}`)}}}, "exit 1"},
		{"runner error", &fakeRunner{err: errors.New("cannot start")}, "cannot start"},
		{"bad json", &fakeRunner{results: []Result{ok("nope")}}, "decode issue list"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ConnectorTracker{Runner: tc.f}.ListOpen(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestCreateArgs(t *testing.T) {
	f := &fakeRunner{results: []Result{ok(`{"result":{"id":"bd-9","title":"T","labels":["human"],"metadata":{"a":"b"}}}`)}}
	tr := ConnectorTracker{Runner: f}
	got, err := tr.Create(context.Background(), escalate.NewIssue{
		Title: "T", Description: "D", Priority: "P1",
		Labels:   []string{"human", "human-focus-required"},
		Metadata: map[string]string{"z": "1", "a": "x,y=2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "bd-9" {
		t.Errorf("id = %q", got.ID)
	}
	want := []string{
		"issue", "create", "--title", "T", "--description", "D", "--backend", DefaultTrackerBackend, "--output", "json",
		"--priority", "P1", "--labels", "human", "--labels", "human-focus-required",
		"--metadata", `"a=x,y=2"`, "--metadata", "z=1",
	}
	if !reflect.DeepEqual(f.calls[0].Args, want) {
		t.Errorf("args =\n%q\nwant\n%q", f.calls[0].Args, want)
	}
}

func TestCreateWithoutPriorityOmitsFlag(t *testing.T) {
	f := &fakeRunner{results: []Result{ok(`{"result":{"id":"bd-9"}}`)}}
	if _, err := (ConnectorTracker{Runner: f}).Create(context.Background(), escalate.NewIssue{Title: "T"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(f.calls[0].Args, " "), "--priority") {
		t.Errorf("empty priority must not be passed: %q", f.calls[0].Args)
	}
}

func TestCreateFailures(t *testing.T) {
	for name, f := range map[string]*fakeRunner{
		"exit":      {results: []Result{{ExitCode: 1, Stderr: []byte("nope")}}},
		"no id":     {results: []Result{ok(`{"result":{}}`)}},
		"bad json":  {results: []Result{ok(`x`)}},
		"no runner": {err: errors.New("cannot start")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (ConnectorTracker{Runner: f}).Create(context.Background(), escalate.NewIssue{Title: "T"}); err == nil {
				t.Fatal("failure swallowed")
			}
		})
	}
}

func TestCommentSetMetadataCloseArgs(t *testing.T) {
	f := &fakeRunner{}
	tr := ConnectorTracker{Runner: f}
	ctx := context.Background()
	if err := tr.Comment(ctx, "bd-1", "hello `world` $x"); err != nil {
		t.Fatal(err)
	}
	if err := tr.SetMetadata(ctx, "bd-1", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(ctx, "bd-1", "done"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"issue", "comment", "bd-1", "--body", "hello `world` $x", "--backend", DefaultTrackerBackend, "--output", "json"},
		{"issue", "update", "bd-1", "--backend", DefaultTrackerBackend, "--output", "json", "--metadata", "k=v"},
		{"issue", "close", "bd-1", "--reason", "done", "--backend", DefaultTrackerBackend, "--output", "json"},
	}
	for i, w := range want {
		if !reflect.DeepEqual(f.calls[i].Args, w) {
			t.Errorf("call %d args = %q, want %q", i, f.calls[i].Args, w)
		}
	}
}

func TestWritesReportFailure(t *testing.T) {
	for name, fn := range map[string]func(ConnectorTracker) error{
		"comment":  func(tr ConnectorTracker) error { return tr.Comment(context.Background(), "b", "x") },
		"metadata": func(tr ConnectorTracker) error { return tr.SetMetadata(context.Background(), "b", nil) },
		"close":    func(tr ConnectorTracker) error { return tr.Close(context.Background(), "b", "x") },
	} {
		t.Run(name, func(t *testing.T) {
			err := fn(ConnectorTracker{Runner: &fakeRunner{results: []Result{{ExitCode: 4, Stderr: []byte("not found")}}}})
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestCSVField(t *testing.T) {
	for in, want := range map[string]string{
		"k=v":          "k=v",
		"k=a,b":        `"k=a,b"`,
		`k=say "hi"`:   `"k=say ""hi"""`,
		"k=line\nfeed": "\"k=line\nfeed\"",
	} {
		if got := csvField(in); got != want {
			t.Errorf("csvField(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNotifierSubstitutesPlaceholdersAndExportsEnv(t *testing.T) {
	f := &fakeRunner{}
	n := ExecNotifier{Runner: f, Argv: []string{"push-cmd", "--title", "{title}", "--msg={body}", "--click", "{url}", "--tag", "{key}", "static"}}
	err := n.Notify(context.Background(), escalate.Notification{Key: "pr:a", Title: "T; rm -rf /", Body: "B $(x)", URL: "https://x.test/r"})
	if err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	wantArgs := []string{"--title", "T; rm -rf /", "--msg=B $(x)", "--click", "https://x.test/r", "--tag", "pr:a", "static"}
	if c.Name != "push-cmd" || !reflect.DeepEqual(c.Args, wantArgs) {
		t.Errorf("call = %+v", c)
	}
	wantEnv := []string{"ESCALATION_TITLE=T; rm -rf /", "ESCALATION_BODY=B $(x)", "ESCALATION_URL=https://x.test/r", "ESCALATION_KEY=pr:a"}
	if !reflect.DeepEqual(c.Env, wantEnv) {
		t.Errorf("env = %q", c.Env)
	}
}

func TestNotifierFailures(t *testing.T) {
	n := ExecNotifier{Runner: &fakeRunner{}}
	if err := n.Notify(context.Background(), escalate.Notification{}); err == nil || !strings.Contains(err.Error(), "no notify command") {
		t.Errorf("empty argv: err = %v", err)
	}
	n = ExecNotifier{Runner: &fakeRunner{results: []Result{{ExitCode: 7, Stderr: []byte("channel rejected")}}}, Argv: []string{"push"}}
	if err := n.Notify(context.Background(), escalate.Notification{}); err == nil || !strings.Contains(err.Error(), "channel rejected") || !strings.Contains(err.Error(), "7") {
		t.Errorf("non-zero exit: err = %v", err)
	}
	n = ExecNotifier{Runner: &fakeRunner{err: errors.New("no such program")}, Argv: []string{"push"}}
	if err := n.Notify(context.Background(), escalate.Notification{}); err == nil {
		t.Error("start failure swallowed")
	}
}

func TestExecRunner(t *testing.T) {
	r := ExecRunner{Timeout: 10 * time.Second}
	ctx := context.Background()

	res, err := r.Run(ctx, "sh", []string{"-c", `cat; echo err >&2; echo "$ESCALATION_TEST"; exit 3`}, []byte("in\n"), []string{"ESCALATION_TEST=env-ok"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || string(res.Stdout) != "in\nenv-ok\n" || string(res.Stderr) != "err\n" {
		t.Errorf("result = %+v", res)
	}

	if _, err := r.Run(ctx, "definitely-not-a-real-binary-xyz", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "start") {
		t.Errorf("start failure: err = %v", err)
	}

	short := ExecRunner{Timeout: 50 * time.Millisecond}
	if _, err := short.Run(ctx, "sh", []string{"-c", "exec sleep 5"}, nil, nil); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("timeout: err = %v", err)
	}
}
