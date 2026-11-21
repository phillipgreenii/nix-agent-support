package client

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
)

const fanOutJSON = `{"sources":[{"source":"a","status":"succeeded","count":1}]}`

func newTestClient(f *Fake) Client { return Client{Run: f.Runner()} }

// TestArgvTable pins the argv each method builds (part of the contract).
func TestArgvTable(t *testing.T) {
	ok := cmdrun.Result{Stdout: []byte(`{"result":{},"sources":[],"changes":[],"runs":[],"entities":[]}`)}
	cases := []struct {
		name string
		call func(Client) error
		want []string
	}{
		{"list minimal", func(c Client) error {
			_, err := c.List(context.Background(), ListRequest{Type: "issue", Query: "q"})
			return err
		}, []string{"issue", "list", "--query", "q", "--output", "json"}},
		{"list all flags", func(c Client) error {
			_, err := c.List(context.Background(), ListRequest{Type: "pr", Query: "q", Backend: "b", IDsOnly: true, Fingerprints: true})
			return err
		}, []string{"pr", "list", "--query", "q", "--backend", "b", "--ids-only", "--fingerprints", "--output", "json"}},
		{"get minimal", func(c Client) error {
			_, err := c.Get(context.Background(), GetRequest{Type: "thread", ID: "T1"})
			return err
		}, []string{"thread", "show", "T1"}},
		{"get all flags", func(c Client) error {
			_, err := c.Get(context.Background(), GetRequest{Type: "issue", ID: "X-1", Backend: "b", Fresh: true, Watch: true})
			return err
		}, []string{"issue", "show", "X-1", "--fresh", "--backend", "b", "--watch"}},
		{"changes minimal", func(c Client) error {
			_, err := c.Changes(context.Background(), ChangesRequest{Type: "pr", Consumer: "c"})
			return err
		}, []string{"pr", "changes", "--consumer", "c", "--output", "json"}},
		{"changes all flags", func(c Client) error {
			_, err := c.Changes(context.Background(), ChangesRequest{Type: "issue", Query: "q", Consumer: "c", Backend: "b", Cached: true, Reset: true, NoAck: true})
			return err
		}, []string{"issue", "changes", "--query", "q", "--consumer", "c", "--backend", "b", "--cached", "--reset", "--no-ack", "--output", "json"}},
		{"changes-ack", func(c Client) error {
			_, err := c.ChangesAck(context.Background(), ChangesAckRequest{Type: "issue", Query: "q", Consumer: "c", Backend: "b", NextSeq: 42})
			return err
		}, []string{"issue", "changes-ack", "--query", "q", "--consumer", "c", "--next-seq", "42", "--backend", "b", "--output", "json"}},
		{"changes-ack no query", func(c Client) error {
			_, err := c.ChangesAck(context.Background(), ChangesAckRequest{Type: "ci", Consumer: "c", Backend: "b", NextSeq: 0})
			return err
		}, []string{"ci", "changes-ack", "--consumer", "c", "--next-seq", "0", "--backend", "b", "--output", "json"}},
		{"ci", func(c Client) error {
			_, err := c.Ci(context.Background(), CIRequest{PRID: "7", Fresh: true})
			return err
		}, []string{"ci", "list", "7", "--fresh", "--output", "json"}},
		{"capabilities", func(c Client) error {
			_, err := c.Capabilities(context.Background(), "pr", "b")
			return err
		}, []string{"pr", "capabilities", "--backend", "b", "--output", "json"}},
		{"capabilities no backend", func(c Client) error {
			_, err := c.Capabilities(context.Background(), "pr", "")
			return err
		}, []string{"pr", "capabilities", "--output", "json"}},
		{"health", func(c Client) error {
			_, err := c.Health(context.Background(), "issue", "b")
			return err
		}, []string{"issue", "health", "--backend", "b", "--output", "json"}},
		{"config validate", func(c Client) error {
			_, err := c.ConfigValidate(context.Background())
			return err
		}, []string{"config", "validate"}},
		{"ledger show", func(c Client) error {
			_, err := c.LedgerShow(context.Background())
			return err
		}, []string{"ledger", "show", "--output", "json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFake()
			f.Default = ok
			if err := tc.call(newTestClient(f)); err != nil {
				t.Fatalf("call: %v", err)
			}
			got := f.Argv()
			if len(got) != 1 || !slices.Equal(got[0], tc.want) {
				t.Fatalf("argv = %v, want [%v]", got, tc.want)
			}
			if c := f.Calls()[0]; c.Name != Binary || c.Timeout != DefaultTimeout {
				t.Fatalf("call = %+v, want binary %q with the default timeout", c, Binary)
			}
		})
	}
}

func TestFanOutDegradedIsNotAnError(t *testing.T) {
	f := NewFake()
	f.Script(cmdrun.Result{Code: ExitDegraded, Stdout: []byte(`{"sources":[{"source":"a","status":"succeeded","count":0},{"source":"b","status":"degraded","count":0,"reason":"x"}],"changes":[]}`)}, "pr", "changes")
	out, err := newTestClient(f).Changes(context.Background(), ChangesRequest{Type: "pr", Consumer: "c"})
	if err != nil {
		t.Fatalf("degraded fan-out returned an error: %v", err)
	}
	if len(out.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(out.Sources))
	}
}

func TestTypedErrors(t *testing.T) {
	cases := []struct {
		name     string
		res      cmdrun.Result
		wantCode int
		wantWire string
		reason   string
	}{
		{"not found", cmdrun.Result{Code: ExitNotFound, Stdout: []byte(`{"error":{"code":"not_found","message":"no such id","reason":"gone"}}`)}, ExitNotFound, "not_found", "gone"},
		{"unavailable plain text", cmdrun.Result{Code: ExitUnavailable, Stdout: []byte("down\n")}, ExitUnavailable, "", ""},
		{"start failure", cmdrun.Result{Code: -1, StartErr: errors.New("no such file")}, -1, "", ""},
		{"context ended", cmdrun.Result{Code: -1, CtxErr: context.DeadlineExceeded, TimedOut: true}, -1, "", ""},
		{"signaled", cmdrun.Result{Code: -1, Signal: "killed"}, -1, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFake()
			f.Default = tc.res
			_, err := newTestClient(f).Get(context.Background(), GetRequest{Type: "issue", ID: "X"})
			var ce *Error
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if ce.ExitCode != tc.wantCode || ce.Code != tc.wantWire || ce.Reason != tc.reason {
				t.Fatalf("error = %+v, want exit %d code %q reason %q", ce, tc.wantCode, tc.wantWire, tc.reason)
			}
			if tc.wantCode == ExitNotFound != IsNotFound(err) {
				t.Fatalf("IsNotFound = %v for exit %d", IsNotFound(err), tc.wantCode)
			}
			if ce.Error() == "" {
				t.Fatal("empty Error()")
			}
		})
	}
}

func TestUndecodableStdout(t *testing.T) {
	f := NewFake()
	f.Default = cmdrun.Result{Stdout: []byte("not json")}
	c := newTestClient(f)
	if _, err := c.Get(context.Background(), GetRequest{Type: "issue", ID: "X"}); err == nil {
		t.Fatal("Get accepted undecodable stdout")
	}
	if _, err := c.List(context.Background(), ListRequest{Type: "issue", Query: "q"}); err == nil {
		t.Fatal("List accepted undecodable stdout")
	}
}

func TestGetReturnsResult(t *testing.T) {
	f := NewFake()
	f.Default = cmdrun.Result{Stdout: []byte(`{"protocolVersion":1,"result":{"id":"X"}}`)}
	got, err := newTestClient(f).Get(context.Background(), GetRequest{Type: "issue", ID: "X"})
	if err != nil || string(got) != `{"id":"X"}` {
		t.Fatalf("Get = %s, %v", got, err)
	}
}

func TestClientOverrides(t *testing.T) {
	f := NewFake()
	f.Default = cmdrun.Result{Stdout: []byte(`{"result":null}`)}
	c := Client{Run: f.Runner(), Binary: "/x/pg-connector", Timeout: 5, Env: []string{"A=b"}}
	if _, err := c.Get(context.Background(), GetRequest{Type: "issue", ID: "X"}); err != nil {
		t.Fatal(err)
	}
	call := f.Calls()[0]
	if call.Name != "/x/pg-connector" || call.Timeout != 5 || !slices.Equal(call.Env, []string{"A=b"}) {
		t.Fatalf("call = %+v", call)
	}
}

func TestFakeLongestPrefixWins(t *testing.T) {
	f := NewFake()
	f.Script(cmdrun.Result{Stdout: []byte("short")}, "pr")
	f.Script(cmdrun.Result{Stdout: []byte("long")}, "pr", "show")
	run := f.Runner()
	if got := string(run(context.Background(), cmdrun.Call{Args: []string{"pr", "show", "1"}}).Stdout); got != "long" {
		t.Fatalf("got %q, want long", got)
	}
	if got := string(run(context.Background(), cmdrun.Call{Args: []string{"pr", "list"}}).Stdout); got != "short" {
		t.Fatalf("got %q, want short", got)
	}
	if got := string(run(context.Background(), cmdrun.Call{Args: []string{"issue"}}).Stdout); got != "" {
		t.Fatalf("got %q, want the zero default", got)
	}
}

func TestExitCodesAreSharedCodes(t *testing.T) {
	want := map[string]int{"OK": 0, "Error": 1, "Usage": 2, "Unavailable": 3, "NotFound": 4, "Degraded": 5, "Config": 7, "Denied": 8}
	got := map[string]int{"OK": ExitOK, "Error": ExitError, "Usage": ExitUsage, "Unavailable": ExitUnavailable, "NotFound": ExitNotFound, "Degraded": ExitDegraded, "Config": ExitConfig, "Denied": ExitDenied}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Exit%s = %d, want %d", k, got[k], v)
		}
	}
}

func TestFanOutExitCode(t *testing.T) {
	mk := func(st ...SourceStatus) FanOutOutcome {
		var o FanOutOutcome
		for _, s := range st {
			o.Sources = append(o.Sources, SourceResult{Status: s})
		}
		return o
	}
	for name, tc := range map[string]struct {
		o    FanOutOutcome
		want int
	}{
		"all healthy":           {mk(SourceSucceeded, SourceDisabled), ExitOK},
		"one degraded":          {mk(SourceSucceeded, SourceDegraded), ExitDegraded},
		"all degraded":          {mk(SourceDegraded), ExitUnavailable},
		"none":                  {mk(), ExitUnavailable},
		"disabled only counted": {mk(SourceDisabled), ExitOK},
	} {
		if got := tc.o.ExitCode(); got != tc.want {
			t.Errorf("%s: ExitCode = %d, want %d", name, got, tc.want)
		}
	}
	if got := (ListOutcome[int]{Sources: mk(SourceDegraded).Sources}).ExitCode(); got != ExitUnavailable {
		t.Errorf("ListOutcome.ExitCode = %d", got)
	}
	if got := (CIListOutcome[int]{Sources: mk(SourceSucceeded).Sources}).ExitCode(); got != ExitOK {
		t.Errorf("CIListOutcome.ExitCode = %d", got)
	}
	co := ChangesOutcome{Sources: []ChangesSource{{Status: SourceSucceeded}, {Status: SourceDegraded}}}
	if got := co.ExitCode(); got != ExitDegraded {
		t.Errorf("ChangesOutcome.ExitCode = %d", got)
	}
}

// TestSchemaVersionCompatibility: an absent version reads as 1, a new
// producer's output decodes with the old decoder unchanged (it never rejects
// unknown fields), and an old producer's output decodes with the new one.
func TestSchemaVersionCompatibility(t *testing.T) {
	// Old producer, new consumer.
	var fromOld FanOutOutcome
	if err := json.Unmarshal([]byte(fanOutJSON), &fromOld); err != nil {
		t.Fatal(err)
	}
	if fromOld.SchemaVersion != 0 || fromOld.Version() != 1 {
		t.Fatalf("absent version: field %d, Version() %d, want 0 and 1", fromOld.SchemaVersion, fromOld.Version())
	}
	// New producer, old consumer: today's decoder is a plain json.Unmarshal into
	// the pre-version struct (no DisallowUnknownFields), reproduced here.
	type oldFanOutOutcome struct {
		Sources []SourceResult `json:"sources"`
	}
	b, err := json.Marshal(FanOutOutcome{SchemaVersion: SchemaVersion1, Sources: []SourceResult{{Source: "a", Status: SourceSucceeded}}})
	if err != nil {
		t.Fatal(err)
	}
	var old oldFanOutOutcome
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatalf("old decoder rejected the versioned outcome: %v", err)
	}
	if len(old.Sources) != 1 {
		t.Fatalf("old decoder lost sources: %+v", old)
	}
	// A producer that leaves the field zero writes nothing (additive, optional).
	b, _ = json.Marshal(FanOutOutcome{Sources: []SourceResult{}})
	if string(b) != `{"sources":[]}` {
		t.Fatalf("zero version leaked into the wire: %s", b)
	}
	// An explicit later version is kept.
	var v2 FanOutOutcome
	if err := json.Unmarshal([]byte(`{"schemaVersion":2,"sources":[]}`), &v2); err != nil || v2.Version() != 2 {
		t.Fatalf("v2 = %+v, %v", v2, err)
	}
}

func TestTargetedAndFanOutStdout(t *testing.T) {
	f := NewFake().
		Script(cmdrun.Result{Stdout: []byte(`{"result":{"n":1}}`)}, "pr", "files").
		Script(cmdrun.Result{Code: ExitDegraded, Stdout: []byte(fanOutJSON)}, "issue", "list").
		Script(cmdrun.Result{Code: ExitUnavailable, Stdout: []byte(`{"error":{"code":"unavailable","message":"down"}}`)}, "ci", "list")
	c := newTestClient(f)
	ctx := context.Background()

	res, err := c.Targeted(ctx, "pr", "files", "7")
	if err != nil || string(res) != `{"n":1}` {
		t.Fatalf("Targeted = %s, %v", res, err)
	}
	raw, err := c.FanOutStdout(ctx, "issue", "list", "--query", "q")
	if err != nil || string(raw) != fanOutJSON {
		t.Fatalf("FanOutStdout degraded = %s, %v", raw, err)
	}
	_, err = c.FanOutStdout(ctx, "ci", "list", "7")
	var ce *Error
	if !errors.As(err, &ce) || ce.ExitCode != ExitUnavailable || ce.Code != "unavailable" {
		t.Fatalf("FanOutStdout failure = %v", err)
	}
	want := [][]string{{"pr", "files", "7"}, {"issue", "list", "--query", "q"}, {"ci", "list", "7"}}
	if got := f.Argv(); !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

func TestNewExitErrorReadsEnvelope(t *testing.T) {
	e := NewExitError([]string{"pr", "show"}, ExitNotFound, []byte(`{"error":{"code":"not_found","message":"gone","reason":"r"}}`))
	if e.ExitCode != ExitNotFound || e.Code != "not_found" || e.Message != "gone" || e.Reason != "r" {
		t.Fatalf("NewExitError = %+v", e)
	}
}
