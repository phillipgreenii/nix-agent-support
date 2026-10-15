package router

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestOpsListsOnlyTheOpsOfTheInstanceSorted(t *testing.T) {
	r := New(Options{})
	r.Register("pr", "show", echo(1))
	r.Register("ci", "runs", echo(1))
	r.Register("pr", "list", echo(1))
	r.Register("zz", "other", echo(1))
	if got := strings.Join(r.Ops("pr"), ","); got != "list,show" {
		t.Errorf("Ops(pr) = %q, want list,show", got)
	}
	if got := strings.Join(r.Ops("ci"), ","); got != "runs" {
		t.Errorf("Ops(ci) = %q, want runs", got)
	}
	if got := r.Ops("absent"); len(got) != 0 {
		t.Errorf("Ops of an instance with nothing registered = %v", got)
	}
}

func TestMigratingGateMessages(t *testing.T) {
	r := New(Options{})
	r.Register("pr", "list", echo(1))
	r.SetMigrating(true, "")
	e := wantError(t, r.Dispatch(context.Background(), call("pr", "list")), "unavailable")
	if want := "migrating: the store is being migrated; retry shortly"; e.Message != want {
		t.Errorf("default message = %q, want %q", e.Message, want)
	}
	r.SetMigrating(true, "store migration to schema 3 in progress")
	e = wantError(t, r.Dispatch(context.Background(), call("pr", "list")), "unavailable")
	if want := "migrating: store migration to schema 3 in progress"; e.Message != want {
		t.Errorf("detailed message = %q, want %q", e.Message, want)
	}
}

// stepClock returns a clock that advances by step on every reading.
func stepClock(start time.Time, step time.Duration) func() time.Time {
	now := start
	return func() time.Time {
		t := now
		now = now.Add(step)
		return t
	}
}

func TestACallWithoutAReceivedTimeIsStampedWhenTheRouterSeesIt(t *testing.T) {
	start := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	r := New(Options{Now: stepClock(start, 40*time.Millisecond)})
	r.Register("pr", "list", echo(1))
	var got Annotations
	r.Use(func(next Func) Func {
		return func(ctx context.Context, c Call) scriptout.Response {
			resp := next(ctx, c)
			got = AnnotationsFrom(ctx)
			return resp
		}
	})
	if resp := r.Dispatch(context.Background(), call("pr", "list")); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	// Received is read on arrival, the handler starts one reading later.
	if got.Wait != 40*time.Millisecond {
		t.Errorf("wait = %v, want 40ms (the time between the router stamping the call and starting the handler)", got.Wait)
	}
}

func TestAWaitSetBeforeTheHandlerRunsIsNotReplacedByTheRoutersMeasurement(t *testing.T) {
	start := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	r := New(Options{Now: stepClock(start, 40*time.Millisecond)})
	r.Register("pr", "list", echo(1))
	var got Annotations
	r.Use(func(next Func) Func {
		return func(ctx context.Context, c Call) scriptout.Response {
			// A middleware that already knows how long the call waited.
			Annotate(ctx, "queue", false, 0, 7*time.Second)
			resp := next(ctx, c)
			got = AnnotationsFrom(ctx)
			return resp
		}
	})
	r.Dispatch(context.Background(), call("pr", "list"))
	if got.Wait != 7*time.Second {
		t.Errorf("wait = %v, want the 7s that was set first", got.Wait)
	}
}

func TestAnnotateStoresANegativeWaitAndZeroKeepsTheMeasurement(t *testing.T) {
	ctx := withCarrier(context.Background())
	Annotate(ctx, "store", false, 1, -time.Second)
	if got := AnnotationsFrom(ctx).Wait; got != -time.Second {
		t.Errorf("a non-zero wait is stored whatever its sign: %v", got)
	}
	Annotate(ctx, "origin", true, 2, 0)
	got := AnnotationsFrom(ctx)
	if got.Wait != -time.Second || got.ServedFrom != "origin" || !got.Stale || got.AgeSeconds != 2 {
		t.Errorf("annotations = %+v: the last call wins for every field except a zero wait", got)
	}
	// A wait that was set by Annotate is not replaced by the router's measurement.
	c := carrierOf(ctx)
	c.setWaitIfUnset(time.Hour)
	if got := AnnotationsFrom(ctx).Wait; got != -time.Second {
		t.Errorf("setWaitIfUnset replaced a wait that was set: %v", got)
	}
}

func TestADeadlineThatIsNotPositiveIsNotPutOnTheHandlerContext(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		r := New(Options{})
		var sawDeadline bool
		r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			_, sawDeadline = ctx.Deadline()
			return "ok", nil
		}})
		c := call("pr", "list")
		c.Deadline = d
		if resp := r.Dispatch(context.Background(), c); resp.Error != nil || string(resp.Result) != `"ok"` {
			t.Fatalf("deadline %v: %+v", d, resp)
		}
		if sawDeadline {
			t.Errorf("a call with deadline %v got a deadline on the handler context", d)
		}
	}
}

func TestOnlyACallWithADeadlineHasItsFailureReportedAsAMissedDeadline(t *testing.T) {
	// The caller's own context is already past its deadline, so ctx.Err() of the
	// handler's context reports DeadlineExceeded even though the router put no
	// deadline on it.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	for _, d := range []time.Duration{0, -time.Second} {
		r := New(Options{})
		r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 2, Handle: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("boom")
		}})
		c := call("pr", "list")
		c.Deadline = d
		resp := r.Dispatch(expired, c)
		if resp.Error == nil || resp.Error.Message != "boom" {
			t.Errorf("deadline %v: error = %+v, want the handler's own error untouched", d, resp.Error)
		}
		if resp.SchemaVersion != 2 {
			t.Errorf("deadline %v: schema version = %d", d, resp.SchemaVersion)
		}
	}
}

func TestAHandlerThatFailsForAnotherReasonBeforeTheDeadlineKeepsItsError(t *testing.T) {
	r := New(Options{})
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 1, Handle: func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("boom")
	}})
	c := call("pr", "list")
	c.Deadline = time.Hour
	resp := r.Dispatch(context.Background(), c)
	if resp.Error == nil || resp.Error.Message != "boom" {
		t.Fatalf("error = %+v, want the handler's own error: the deadline was not exceeded", resp.Error)
	}
}

func TestAResultThatCannotBeEncodedIsAnErrorWithTheSchemaVersion(t *testing.T) {
	r := New(Options{})
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 6, Handle: func(context.Context, json.RawMessage) (any, error) {
		return make(chan int), nil
	}})
	resp := r.Dispatch(context.Background(), call("pr", "list"))
	if resp.Error == nil || !strings.Contains(resp.Error.Message, `marshal result for op "list"`) {
		t.Fatalf("response = %+v, want a marshal error naming the op", resp)
	}
	if resp.SchemaVersion != 6 {
		t.Errorf("schema version = %d, want the handler's 6", resp.SchemaVersion)
	}
	if len(resp.Result) != 0 {
		t.Errorf("an error response carried a result: %s", resp.Result)
	}
}
