package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-activity-git/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

func newTestTable() scriptout.DispatchTable {
	return newDispatchTable(internal.New(internal.Options{Stderr: &discard{}}))
}

type discard struct{}

func (*discard) Write(p []byte) (int, error) { return len(p), nil }

// configInjectingBackend wraps a conformance.Backend and adds the host's
// per-backend config block to every request, as the umbrella does for the
// static backends.<name> block: conformance.ListActivityRequest sends none.
type configInjectingBackend struct {
	inner  conformance.Backend
	config string
}

func (c configInjectingBackend) Invoke(ctx context.Context, request []byte) ([]byte, int, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, 0, err
	}
	req["config"] = json.RawMessage(c.config)
	out, err := json.Marshal(req)
	if err != nil {
		return nil, 0, err
	}
	return c.inner.Invoke(ctx, out)
}

func TestNewDispatchTable_DeclaresActivityCapability(t *testing.T) {
	table := newTestTable()
	result, err := table[scriptout.OpCapabilities].Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("capabilities Handle: %v", err)
	}
	resp, ok := result.(scriptout.CapabilitiesResponse)
	if !ok {
		t.Fatalf("result type = %T, want scriptout.CapabilitiesResponse", result)
	}
	// ops is computed from the table itself and always lists the
	// capabilities op; the only capability op is list_activity, and there is
	// no auth_status.
	if want := table.Ops(); !reflect.DeepEqual(resp.Ops, want) {
		t.Errorf("capabilities.ops = %v, want table.Ops() = %v", resp.Ops, want)
	}
	if want := []string{scriptout.OpCapabilities, "list_activity"}; !reflect.DeepEqual(resp.Ops, want) {
		t.Errorf("capabilities.ops = %v, want %v (no auth_status)", resp.Ops, want)
	}
	if got := resp.SchemaVersions["activity"]; got != schema.ActivitySchemaVersion {
		t.Errorf("schemaVersions[activity] = %d, want %d", got, schema.ActivitySchemaVersion)
	}
	raw, _ := json.Marshal(resp.Vocabulary["activity_kinds"])
	if want := `["commit"]`; string(raw) != want {
		t.Errorf("vocabulary.activity_kinds = %s, want %s", raw, want)
	}
}

func TestNewDispatchTable_NoAuthStatus(t *testing.T) {
	if _, ok := newTestTable()[scriptout.OpAuthStatus]; ok {
		t.Error("table must not carry auth_status: Backend is not a provider.AuthChecker")
	}
}

func TestNewDispatchTable_ListActivityConformance(t *testing.T) {
	backend := configInjectingBackend{
		inner:  conformance.TableBackend{Table: newTestTable()},
		config: `{"author_emails":["me@example.test"]}`,
	}
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	for _, s := range []time.Time{since, {}} {
		results := conformance.RunListActivityCase(context.Background(), backend, s, before)
		if len(results) == 0 {
			t.Fatal("conformance returned no sub-cases")
		}
		for _, r := range results {
			if r.Err != nil {
				t.Errorf("%s (since=%v): %v", r.Name, s, r.Err)
			}
		}
	}

	res, err := conformance.InvokeListActivity(context.Background(), backend, since.Format(time.RFC3339), before.Format(time.RFC3339))
	if err != nil || res.ErrorCode != "" {
		t.Fatalf("InvokeListActivity: res=%+v err=%v", res, err)
	}
	if got, want := string(res.Result), `{"items":[],"truncated":false}`; got != want {
		t.Errorf("result = %s, want %s", got, want)
	}
}

func TestNewDispatchTable_ListActivityUnavailableWithoutAuthorEmails(t *testing.T) {
	for name, cfg := range map[string]string{
		"missing key": `{}`,
		"empty list":  `{"author_emails":[]}`,
		"blank entry": `{"author_emails":["  "]}`,
	} {
		t.Run(name, func(t *testing.T) {
			backend := configInjectingBackend{
				inner:  conformance.TableBackend{Table: newTestTable()},
				config: cfg,
			}
			res, err := conformance.InvokeListActivity(context.Background(), backend, "2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z")
			if err != nil {
				t.Fatalf("InvokeListActivity: %v", err)
			}
			if res.ErrorCode != "unavailable" {
				t.Fatalf("error code = %q, want unavailable", res.ErrorCode)
			}
		})
	}

	// No config block at all.
	bare := conformance.TableBackend{Table: newTestTable()}
	res, err := conformance.InvokeListActivity(context.Background(), bare, "2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z")
	if err != nil {
		t.Fatalf("InvokeListActivity (no config): %v", err)
	}
	if res.ErrorCode != "unavailable" {
		t.Errorf("no-config error code = %q, want unavailable", res.ErrorCode)
	}
}
