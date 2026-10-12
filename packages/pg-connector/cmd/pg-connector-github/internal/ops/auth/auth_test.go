package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/identity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// countingGH counts origin calls and answers a scripted viewer or error.
type countingGH struct {
	calls atomic.Int32
	login string
	err   error
}

func (c *countingGH) Run(context.Context, ...string) ([]byte, error) {
	c.calls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return []byte(fmt.Sprintf(`{"data":{"viewer":{"login":%q}}}`, c.login)), nil
}

type staticClock struct{}

func (staticClock) Now() time.Time                       { return time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC) }
func (staticClock) After(time.Duration) <-chan time.Time { return nil }

func tracker(gh identity.Runner) *identity.Tracker {
	return identity.New(identity.Options{GH: gh, Clock: staticClock{}})
}

func call(t *testing.T, r *router.Router, instance string) scriptout.Response {
	t.Helper()
	return r.Dispatch(context.Background(), router.Call{Instance: instance, Request: scriptout.Request{Op: scriptout.OpAuthStatus}})
}

func decode(t *testing.T, resp scriptout.Response) scriptout.AuthStatus {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("auth_status answered an error: %+v", resp.Error)
	}
	var a scriptout.AuthStatus
	if err := json.Unmarshal(resp.Result, &a); err != nil {
		t.Fatalf("decode %s: %v", resp.Result, err)
	}
	return a
}

func TestAuthenticatedAnswersOKWithNoOriginCall(t *testing.T) {
	gh := &countingGH{login: "octo-one"}
	tr := tracker(gh)
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := gh.calls.Load()

	r := router.New(router.Options{})
	Register(r, tr)
	for _, inst := range []string{"pr", "ci"} {
		resp := call(t, r, inst)
		if got := decode(t, resp); got.State != scriptout.AuthOK || got.Detail != "" {
			t.Errorf("%s: %+v, want OK with no detail", inst, got)
		}
		// The result is exactly the old backends' wire shape.
		if string(resp.Result) != `{"state":"OK"}` {
			t.Errorf("%s: result %s", inst, resp.Result)
		}
	}
	if gh.calls.Load() != before {
		t.Errorf("auth_status made %d origin calls, want 0", gh.calls.Load()-before)
	}
}

func TestSchemaVersionPerInstance(t *testing.T) {
	r := router.New(router.Options{})
	Register(r, tracker(&countingGH{}))
	if got := call(t, r, "pr").SchemaVersion; got != schema.PRSchemaVersion {
		t.Errorf("pr schemaVersion = %d, want %d", got, schema.PRSchemaVersion)
	}
	if got := call(t, r, "ci").SchemaVersion; got != schema.CISchemaVersion {
		t.Errorf("ci schemaVersion = %d, want %d", got, schema.CISchemaVersion)
	}
}

func TestUnauthenticatedAnswersMissingWithTheReason(t *testing.T) {
	gh := &countingGH{login: "octo-one"}
	tr := tracker(gh)
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr.MarkUnauthenticated(fmt.Errorf("Bad credentials (HTTP 401): %w", vcs.ErrAuthInvalid))

	r := router.New(router.Options{})
	Register(r, tr)
	got := decode(t, call(t, r, "pr"))
	if got.State != scriptout.AuthMissing || got.Detail == "" {
		t.Fatalf("%+v, want MISSING with a detail", got)
	}
	if !strings.Contains(got.Detail, "Bad credentials") {
		t.Errorf("detail %q does not carry the reason", got.Detail)
	}

	tr.Clear()
	if got := decode(t, call(t, r, "pr")); got.State != scriptout.AuthOK {
		t.Errorf("after Clear: %+v", got)
	}
}

func TestErrorCases(t *testing.T) {
	ctx := context.Background()

	// Never resolved, last refresh failed: MISSING carrying the error.
	failing := tracker(&countingGH{err: errors.New("dial tcp: no route")})
	_ = failing.Refresh(ctx)
	if got := Status(failing.Current()); got.State != scriptout.AuthMissing || !strings.Contains(got.Detail, "no route") {
		t.Errorf("never resolved: %+v", got)
	}

	// Not tried yet.
	if got := Status(tracker(&countingGH{}).Current()); got.State != scriptout.AuthMissing || got.Detail == "" {
		t.Errorf("not resolved yet: %+v", got)
	}

	// A cached identity whose latest refresh failed transiently: still OK, with a note.
	gh := &countingGH{login: "octo-one"}
	tr := tracker(gh)
	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	gh.err = errors.New("timeout")
	_ = tr.Refresh(ctx)
	if got := Status(tr.Current()); got.State != scriptout.AuthOK || !strings.Contains(got.Detail, "timeout") {
		t.Errorf("transient refresh failure: %+v", got)
	}
}
