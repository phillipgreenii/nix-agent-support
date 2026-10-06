package main

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// fakeRunner is a minimal internal.Runner: only Sessions is canned.
type fakeRunner struct {
	sessions string
	err      error
}

func (fakeRunner) Status(context.Context) ([]byte, error) { return []byte(`{"sessions":[]}`), nil }
func (fakeRunner) Info(context.Context, string) ([]byte, error) {
	return nil, errors.New("not used by this test")
}

func (fakeRunner) Search(context.Context, string, string, scriptout.TimeRange) ([]byte, error) {
	return nil, errors.New("not used by this test")
}

func (f fakeRunner) Sessions(context.Context, time.Time, time.Time) ([]byte, error) {
	return []byte(f.sessions), f.err
}

const sessionsBody = `{"sessions":[
 {"session_id":"s1","cwd":"/w/proj","branch":"feat","started_at":"2026-09-02T10:00:00Z","ended_at":"2026-09-02T11:00:00Z","user_turns":2,"first_prompt":"hello"},
 {"session_id":"s2","cwd":"/w/proj","branch":"main","started_at":"2026-09-03T10:00:00Z","ended_at":"2026-09-03T10:00:01Z","user_turns":0,"first_prompt":""}]}`

func TestNewDispatchTable_DeclaresActivityCapability(t *testing.T) {
	table := newDispatchTable(internal.New(fakeRunner{}))
	result, err := table[scriptout.OpCapabilities].Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("capabilities Handle: %v", err)
	}
	resp, ok := result.(scriptout.CapabilitiesResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	wantVersions := map[string]int{
		"agentsession": schema.AgentSessionSchemaVersion,
		"attention":    schema.AttentionSchemaVersion,
		"search":       schema.SearchSchemaVersion,
		"activity":     schema.ActivitySchemaVersion,
	}
	if !reflect.DeepEqual(resp.SchemaVersions, wantVersions) {
		t.Errorf("schemaVersions = %v, want %v", resp.SchemaVersions, wantVersions)
	}
	hasOp := false
	for _, op := range resp.Ops {
		hasOp = hasOp || op == "list_activity"
	}
	if !hasOp {
		t.Errorf("capabilities.ops = %v, want list_activity", resp.Ops)
	}
	if !reflect.DeepEqual(resp.Ops, table.Ops()) {
		t.Errorf("ops = %v, want the table's own keys %v", resp.Ops, table.Ops())
	}
	raw, _ := json.Marshal(resp.Vocabulary["activity_kinds"])
	if string(raw) != `["session"]` {
		t.Errorf("vocabulary.activity_kinds = %s, want [\"session\"]", raw)
	}
}

func TestNewDispatchTable_ListActivityConformance(t *testing.T) {
	table := newDispatchTable(internal.New(fakeRunner{sessions: sessionsBody}))
	backend := conformance.TableBackend{Table: table}
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	for _, r := range conformance.RunListActivityCase(context.Background(), backend, since, before) {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}

	res, err := conformance.InvokeListActivity(context.Background(), backend, since.Format(time.RFC3339), before.Format(time.RFC3339))
	if err != nil || res.ErrorCode != "" {
		t.Fatalf("InvokeListActivity: res=%+v err=%v", res, err)
	}
	var got schema.ActivityListResult
	if err := json.Unmarshal(res.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "session:s1" || got.Items[0].Kind != "session" {
		t.Errorf("items = %+v, want one session:s1 item (s2 has zero user turns)", got.Items)
	}
	if got.Truncated {
		t.Error("truncated must be false")
	}
}

func TestNewDispatchTable_ListActivityUnavailableWhenPaMonitorMissing(t *testing.T) {
	table := newDispatchTable(internal.New(fakeRunner{err: exec.ErrNotFound}))
	backend := conformance.TableBackend{Table: table}
	res, err := conformance.InvokeListActivity(context.Background(), backend, "2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z")
	if err != nil {
		t.Fatalf("InvokeListActivity: %v", err)
	}
	if res.ErrorCode != "unavailable" {
		t.Errorf("error code = %q, want unavailable", res.ErrorCode)
	}
}

func TestNewDispatchTable_ListActivityConfigOnTheWire(t *testing.T) {
	table := newDispatchTable(internal.New(fakeRunner{sessions: sessionsBody}))
	backend := conformance.TableBackend{Table: table}
	invoke := func(cfg string) (string, int) {
		req := `{"op":"list_activity","args":{"since":"2026-09-01T00:00:00Z","before":"2026-10-01T00:00:00Z"},"config":` + cfg + `}`
		out, _, err := backend.Invoke(context.Background(), []byte(req))
		if err != nil {
			t.Fatal(err)
		}
		var resp struct {
			Result *schema.ActivityListResult `json:"result"`
			Error  *struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("decode %s: %v", out, err)
		}
		if resp.Error != nil {
			return resp.Error.Code, 0
		}
		return "", len(resp.Result.Items)
	}
	if code, n := invoke(`{"min_user_turns":0}`); code != "" || n != 2 {
		t.Errorf("min_user_turns 0: code=%q items=%d, want 2 items", code, n)
	}
	if code, _ := invoke(`{"min_user_turns":"x"}`); code != "invalid_argument" {
		t.Errorf("malformed min_user_turns: code=%q, want invalid_argument", code)
	}
}
