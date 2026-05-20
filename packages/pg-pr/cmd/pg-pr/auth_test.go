package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/auth"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
)

// withAuthStubs installs deterministic loader+checker functions for the
// duration of the test and restores the originals afterward.
func withAuthStubs(t *testing.T, cfg *config.Config, statuses []auth.Status, checkErr error) {
	t.Helper()
	origLoader := newAuthLoader
	origChecker := newAuthChecker
	t.Cleanup(func() {
		newAuthLoader = origLoader
		newAuthChecker = origChecker
	})
	newAuthLoader = func(context.Context) (*config.Config, error) { return cfg, nil }
	newAuthChecker = func(context.Context, *config.Config) ([]auth.Status, error) {
		return statuses, checkErr
	}
}

func TestAuthStatus_HumanAllOK(t *testing.T) {
	withAuthStubs(t, &config.Config{}, []auth.Status{
		{Provider: "github", State: "OK", Detail: "Token scopes: 'repo'"},
		{Provider: "jira", State: "OK"},
	}, nil)
	auFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"auth", "status"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"PROVIDER", "github", "OK", "jira", "Token scopes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestAuthStatus_JSON(t *testing.T) {
	withAuthStubs(t, &config.Config{}, []auth.Status{
		{Provider: "github", State: "OK"},
		{Provider: "jira", State: "MISSING", Detail: "missing env: JIRA_API_TOKEN"},
	}, nil)
	auFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"auth", "status", "--json"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected non-zero exit when a provider failed")
	}
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("error = %v", err)
	}
	var parsed []auth.Status
	if jerr := json.Unmarshal(stdout.Bytes(), &parsed); jerr != nil {
		t.Fatalf("decode: %v\n%s", jerr, stdout.String())
	}
	if len(parsed) != 2 {
		t.Fatalf("len = %d", len(parsed))
	}
	if parsed[1].State != "MISSING" {
		t.Fatalf("parsed[1] = %+v", parsed[1])
	}
}

func TestAuthStatus_EnvJSON(t *testing.T) {
	withAuthStubs(t, &config.Config{}, []auth.Status{
		{Provider: "github", State: "OK"},
	}, nil)
	t.Setenv("PGPR_OUTPUT", "json")
	auFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"auth", "status"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	var parsed []auth.Status
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("expected JSON from PGPR_OUTPUT=json: %v\n%s", err, stdout.String())
	}
}

func TestAuthStatus_NonZeroExitOnFailure(t *testing.T) {
	withAuthStubs(t, &config.Config{}, []auth.Status{
		{Provider: "github", State: "EXPIRED", Detail: "token expired"},
	}, nil)
	auFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"auth", "status"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected error when provider EXPIRED")
	}
	if !strings.Contains(err.Error(), "failed auth check") {
		t.Fatalf("error = %v", err)
	}
}
