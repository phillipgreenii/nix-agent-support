package main

import (
	"context"
	"reflect"
	"testing"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-alert-pagerduty/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func testTable() scriptout.DispatchTable {
	return newDispatchTable(internal.New(internal.NewHTTPClient(), internal.NewTokenResolver(func(string) string { return "" })))
}

func capabilities(t *testing.T) scriptout.CapabilitiesResponse {
	t.Helper()
	entry, ok := testTable()[scriptout.OpCapabilities]
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

func TestNewDispatchTable_OpsAreAlertPlusAttentionPlusAuthStatus(t *testing.T) {
	want := []string{"auth_status", "capabilities", "list", "list_attention", "list_history", "show"}
	if got := capabilities(t).Ops; !reflect.DeepEqual(got, want) {
		t.Fatalf("Ops = %v, want %v (AuthChecker implemented)", got, want)
	}
}

func TestNewDispatchTable_CapabilitiesDeclaresVersionsAndMatchesTableKeys(t *testing.T) {
	resp := capabilities(t)
	if resp.SchemaVersions["alert"] != schema.AlertSchemaVersion || resp.SchemaVersions["attention"] != schema.AttentionSchemaVersion {
		t.Fatalf("SchemaVersions = %#v", resp.SchemaVersions)
	}
	if resp.Version != Version || resp.Version == "" {
		t.Fatalf("Version = %q", resp.Version)
	}
	if !reflect.DeepEqual(resp.Ops, testTable().Ops()) {
		t.Fatalf("Ops = %v, want table keys %v", resp.Ops, testTable().Ops())
	}
}

// With no key anywhere (the resolver's env is empty and the config disables
// the Keychain), auth_status reports missing rather than ok.
func TestAuthStatus_MissingKeyReportsMissing(t *testing.T) {
	ctx := scriptout.WithConfig(context.Background(), []byte(`{"disable_keychain":true}`))
	result, err := testTable()[scriptout.OpAuthStatus].Handle(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := result.(scriptout.AuthStatus)
	if !ok || st.State != scriptout.AuthMissing {
		t.Fatalf("auth_status = %#v, want state %q", result, scriptout.AuthMissing)
	}
}
