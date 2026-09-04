package main

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestDispatch_ResolvesSingleRegisteredBackend(t *testing.T) {
	writeFakeBackend(t, "pg-connector-pr-github", `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`)
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - pg-connector-pr-github\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}

	resp, err := Dispatch(context.Background(), reg, "pr", scriptout.OpAuthStatus, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var status scriptout.AuthStatus
	if err := scriptout.Decode(resp.Result, &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.State != scriptout.AuthOK {
		t.Fatalf("state = %q", status.State)
	}
}

func TestDispatch_NoBackendRegistered(t *testing.T) {
	reg, err := parseRegistry([]byte("connector: {}\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := Dispatch(context.Background(), reg, "pr", scriptout.OpAuthStatus, nil); err == nil {
		t.Fatal("expected error for no registered backend")
	}
}

func TestDispatch_AmbiguousMultipleBackends(t *testing.T) {
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - a\n    - b\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := Dispatch(context.Background(), reg, "pr", scriptout.OpAuthStatus, nil); err == nil {
		t.Fatal("expected error for ambiguous multi-backend targeted op")
	}
}
