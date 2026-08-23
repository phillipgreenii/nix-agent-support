package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/cicd"
)

// fakeCICD captures calls and returns canned values.
type fakeCICD struct {
	runs     []api.CIRun
	listErr  error
	logs     []byte
	logsErr  error
	rerunErr error

	rerunCalls   int
	listRunCalls int
	logCalls     int
}

func (f *fakeCICD) ListRuns(context.Context, string, int) ([]api.CIRun, error) {
	f.listRunCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.runs, nil
}

func (f *fakeCICD) GetLogs(context.Context, string) ([]byte, error) {
	f.logCalls++
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	return f.logs, nil
}

func (f *fakeCICD) RerunFailed(context.Context, string, int) error {
	f.rerunCalls++
	return f.rerunErr
}

var _ cicd.Provider = (*fakeCICD)(nil)

// withFakeCICD swaps the provider lookup to return a single fake.
func withFakeCICD(t *testing.T, name string, f cicd.Provider) {
	t.Helper()
	prev := cicdProvidersForRepo
	cicdProvidersForRepo = func(context.Context, string) ([]ciNamedProvider, error) {
		return []ciNamedProvider{{Name: name, Provider: f}}, nil
	}
	t.Cleanup(func() { cicdProvidersForRepo = prev })
}

func TestCIRuns_HumanTable(t *testing.T) {
	resetCIFlags()
	fc := &fakeCICD{
		runs: []api.CIRun{
			{ID: "100", Name: "build", Status: "completed", Conclusion: "success", Provider: "github-actions", URL: "https://x/1"},
			{ID: "101", Name: "lint", Status: "completed", Conclusion: "failure", Provider: "github-actions", URL: "https://x/2"},
		},
	}
	withFakeCICD(t, "github-actions", fc)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"ci", "runs", "5", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if fc.listRunCalls != 1 {
		t.Errorf("ListRuns called %d times; want 1", fc.listRunCalls)
	}
	if !strings.Contains(stdout.String(), "lint") {
		t.Errorf("expected runs table to contain lint; got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "failure") {
		t.Errorf("expected failure conclusion in output; got %q", stdout.String())
	}
}

// TestCIRuns_HumanTable_DescriptionNotRendered guards against an accidental
// new column: api.CIRun grew a Description field (pg2-4dz88.2.2, carrying a
// StatusContext's GraphQL description through) but renderCIRuns is
// deliberately left unchanged for now — parsing/rendering of the field
// itself comes in a later leaf. A run with a non-empty Description must
// still render exactly the same header/columns as before.
func TestCIRuns_HumanTable_DescriptionNotRendered(t *testing.T) {
	resetCIFlags()
	fc := &fakeCICD{
		runs: []api.CIRun{
			{
				ID: "200", Name: "approval-gate", Status: "completed", Conclusion: "failure",
				Provider: "github-status", URL: "https://x/3", Description: "All rules are approved",
			},
		},
	}
	withFakeCICD(t, "github-status", fc)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"ci", "runs", "5", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"PROVIDER", "NAME", "STATUS", "CONCLUSION", "ID", "URL"} {
		if !strings.Contains(out, want) {
			t.Errorf("table header missing %q; got %q", want, out)
		}
	}
	if strings.Contains(out, "DESCRIPTION") {
		t.Errorf("unexpected DESCRIPTION column in rendered table; got %q", out)
	}
	if strings.Contains(out, "All rules are approved") {
		t.Errorf("Description value leaked into rendered table; got %q", out)
	}
}

func TestCIRuns_JSON(t *testing.T) {
	resetCIFlags()
	fc := &fakeCICD{
		runs: []api.CIRun{{ID: "7", Name: "test"}},
	}
	withFakeCICD(t, "github-actions", fc)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"ci", "runs", "5", "--repo", "foo/bar", "--json"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"id": "7"`) {
		t.Errorf("expected JSON id=7; got %q", stdout.String())
	}
}

func TestCIRuns_EmptyHuman(t *testing.T) {
	resetCIFlags()
	fc := &fakeCICD{runs: nil}
	withFakeCICD(t, "github-actions", fc)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"ci", "runs", "5", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no CI runs") {
		t.Errorf("expected empty marker; got %q", stdout.String())
	}
}

func TestCIRuns_PartialProviderFailure(t *testing.T) {
	resetCIFlags()
	good := &fakeCICD{runs: []api.CIRun{{ID: "1", Name: "ok"}}}
	bad := &fakeCICD{listErr: errors.New("nope")}

	prev := cicdProvidersForRepo
	cicdProvidersForRepo = func(context.Context, string) ([]ciNamedProvider, error) {
		return []ciNamedProvider{
			{Name: "good", Provider: good},
			{Name: "bad", Provider: bad},
		}, nil
	}
	t.Cleanup(func() { cicdProvidersForRepo = prev })

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"ci", "runs", "5", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Errorf("expected good-provider runs in output; got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "bad") {
		t.Errorf("expected warning about bad provider on stderr; got %q", stderr.String())
	}
}

func TestCILogs_Success(t *testing.T) {
	resetCIFlags()
	fc := &fakeCICD{logs: []byte("log line 1\nlog line 2\n")}
	withFakeCICD(t, "github-actions", fc)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"ci", "logs", "12345", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "log line 1") {
		t.Errorf("expected log output; got %q", stdout.String())
	}
}

func TestCIRerunFailed_Success(t *testing.T) {
	resetCIFlags()
	fc := &fakeCICD{}
	withFakeCICD(t, "github-actions", fc)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"ci", "rerun-failed", "5", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if fc.rerunCalls != 1 {
		t.Errorf("RerunFailed called %d times; want 1", fc.rerunCalls)
	}
	if !strings.Contains(stdout.String(), "Triggered rerun-failed") {
		t.Errorf("expected success message; got %q", stdout.String())
	}
}

func TestCIRerunFailed_AllFail(t *testing.T) {
	resetCIFlags()
	fc := &fakeCICD{rerunErr: errors.New("nope")}
	withFakeCICD(t, "github-actions", fc)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"ci", "rerun-failed", "5", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestBuildCICDProviders_AcceptsExec(t *testing.T) {
	providers, err := buildCICDProviders([]string{"exec:my-cicd-binary"})
	if err != nil {
		t.Fatalf("expected exec:<binary> to be accepted: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("providers: %+v", providers)
	}
	if providers[0].Name != "exec:my-cicd-binary" {
		t.Fatalf("name: %q", providers[0].Name)
	}
}

func TestBuildCICDProviders_RejectsExecEmptyBinary(t *testing.T) {
	_, err := buildCICDProviders([]string{"exec:"})
	if err == nil {
		t.Fatal("expected error for exec: with empty binary")
	}
	if !strings.Contains(err.Error(), "missing binary") {
		t.Errorf("expected missing-binary error; got %v", err)
	}
}

func TestBuildCICDProviders_RejectsUnknown(t *testing.T) {
	_, err := buildCICDProviders([]string{"some-unknown-provider"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestBuildCICDProviders_AcceptsGHActions(t *testing.T) {
	out, err := buildCICDProviders([]string{"github-actions"})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(out))
	}
}

func TestBuildCICDProviders_Empty(t *testing.T) {
	_, err := buildCICDProviders(nil)
	if err == nil {
		t.Fatal("expected error for empty cicd list")
	}
}
