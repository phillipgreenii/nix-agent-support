package main

import (
	"context"
	"reflect"
	"slices"
	"testing"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-alert-grafana/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func capabilities(t *testing.T) scriptout.CapabilitiesResponse {
	t.Helper()
	table := newDispatchTable(internal.New(internal.NewHTTPClient()))
	entry, ok := table[scriptout.OpCapabilities]
	if !ok {
		t.Fatal("capabilities entry missing")
	}
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp, ok := result.(scriptout.CapabilitiesResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	return resp
}

func TestNewDispatchTable_OpsAreAlertPlusAttentionWithHistoryWithoutAuth(t *testing.T) {
	resp := capabilities(t)
	want := []string{"capabilities", "list", "list_attention", "list_history", "show"}
	if !reflect.DeepEqual(resp.Ops, want) {
		t.Fatalf("Ops = %v, want %v (list_history implemented by pg2-rwuhs; no AuthChecker)", resp.Ops, want)
	}
	if slices.Contains(resp.Ops, scriptout.OpAuthStatus) {
		t.Fatal("must not advertise auth_status")
	}
}

func TestNewDispatchTable_CapabilitiesDeclaresVersionsAndMatchesTableKeys(t *testing.T) {
	table := newDispatchTable(internal.New(internal.NewHTTPClient()))
	resp := capabilities(t)
	if resp.SchemaVersions["alert"] != schema.AlertSchemaVersion || resp.SchemaVersions["attention"] != schema.AttentionSchemaVersion {
		t.Fatalf("SchemaVersions = %#v", resp.SchemaVersions)
	}
	if resp.Version != Version || resp.Version == "" {
		t.Fatalf("Version = %q, want %q", resp.Version, Version)
	}
	if !reflect.DeepEqual(resp.Ops, table.Ops()) {
		t.Fatalf("Ops = %v, want table keys %v", resp.Ops, table.Ops())
	}
}
