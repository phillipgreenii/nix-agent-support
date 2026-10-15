package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Canned wire answers. Fixtures are generic: backend names are placeholders
// and every path is under an invented /state directory.
const (
	statusUnknownOp = `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`
	authOKAnswer    = `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`
	authDownAnswer  = `{"protocolVersion":1,"error":{"code":"unavailable","message":"daemon not running"}}`
	statusFailed    = `{"protocolVersion":1,"error":{"code":"unavailable","message":"status exploded"}}`

	statusHealthyDoc = `{"protocolVersion":1,"schemaVersion":1,"result":{"health":"healthy","source":"daemon","version":"1","build_id":"b1","socket":"/state/d.sock","store":{"path":"/state/s.db","present":true,"size_bytes":10,"age_seconds":5},"supervisor":{"kind":"launchd","label":"l","state":"running"}}}`

	statusDegradedDoc = `{"protocolVersion":1,"schemaVersion":1,"result":{"health":"degraded","reasons":["budget bucket core is paused","query team last_error set"],"source":"daemon","socket":"/state/d.sock","store":{"present":true,"age_seconds":5},"supervisor":{"state":"running"}}}`

	statusDownLocalDoc = `{"protocolVersion":1,"schemaVersion":1,"result":{"health":"down","reasons":["the daemon did not answer"],"source":"local","socket":"/state/d.sock","store":{"path":"/state/s.db","present":true,"size_bytes":10,"age_seconds":7200},"supervisor":{"kind":"launchd","label":"l","state":"not_registered"}}}`
)

func capsAnswer() string {
	return fmt.Sprintf(`{"protocolVersion":1,"schemaVersions":{"pr":%d},"ops":["show","auth_status","capabilities"]}`, schema.PRSchemaVersion)
}

// writeStatusBackend registers a fake backend answering auth_status,
// capabilities and status as given; every other op is unknown_op.
func writeStatusBackend(t *testing.T, name, auth, status string) {
	t.Helper()
	byOp := map[string]string{"auth_status": auth, "capabilities": capsAnswer()}
	if status != "" {
		byOp["status"] = status
	}
	writeOpAwareFakeBackend(t, name, byOp, statusUnknownOp)
}

type verb struct {
	name string
	run  func(backends []string) FanOutOutcome
}

var bothVerbs = []verb{
	{"auth status", func(b []string) FanOutOutcome { return FanOutAuthStatus(context.Background(), nil, b) }},
	{"config validate", func(b []string) FanOutOutcome { return FanOutConfigValidate(context.Background(), nil, b) }},
}

func TestHealthRows_HealthyStatusAddsSucceededRowAndExit0(t *testing.T) {
	for _, v := range bothVerbs {
		t.Run(v.name, func(t *testing.T) {
			writeStatusBackend(t, "daemon-a", authOKAnswer, statusHealthyDoc)
			out := v.run([]string{"daemon-a"})
			want := []SourceResult{
				{Source: "daemon-a", Status: SourceSucceeded, Count: wantOwnCount(v.name)},
				{Source: "daemon-a/health", Status: SourceSucceeded},
			}
			if !reflect.DeepEqual(out.Sources, want) {
				t.Fatalf("sources = %+v, want %+v", out.Sources, want)
			}
			if got := out.ExitCode(); got != 0 {
				t.Fatalf("ExitCode = %d, want 0", got)
			}
		})
	}
}

// wantOwnCount is the Count of a fully healthy backend's own row: 0 for auth
// status, 2 (auth_status + capabilities) for config validate.
func wantOwnCount(verb string) int {
	if verb == "config validate" {
		return 2
	}
	return 0
}

func TestHealthRows_DegradedStatusIsDegradedRowWithReasonsAndExit2(t *testing.T) {
	for _, v := range bothVerbs {
		t.Run(v.name, func(t *testing.T) {
			writeStatusBackend(t, "daemon-a", authOKAnswer, statusDegradedDoc)
			writeStatusBackend(t, "plain-b", authOKAnswer, "")
			out := v.run([]string{"daemon-a", "plain-b"})
			if len(out.Sources) != 3 {
				t.Fatalf("sources = %+v, want 3 rows", out.Sources)
			}
			h := out.Sources[1]
			if h.Source != "daemon-a/health" || h.Status != SourceDegraded {
				t.Fatalf("health row = %+v", h)
			}
			if want := "degraded: budget bucket core is paused; query team last_error set"; h.Reason != want {
				t.Fatalf("reason = %q, want %q", h.Reason, want)
			}
			if out.Sources[0].Status != SourceSucceeded || out.Sources[2].Source != "plain-b" || out.Sources[2].Status != SourceSucceeded {
				t.Fatalf("other rows changed: %+v", out.Sources)
			}
			if got := out.ExitCode(); got != 2 {
				t.Fatalf("ExitCode = %d, want 2 (another backend is healthy)", got)
			}
		})
	}
}

func TestHealthRows_DownLocalReportShowsSupervisorSocketAndStoreAge(t *testing.T) {
	for _, v := range bothVerbs {
		t.Run(v.name, func(t *testing.T) {
			writeStatusBackend(t, "daemon-a", authDownAnswer, statusDownLocalDoc)
			out := v.run([]string{"daemon-a"})
			if len(out.Sources) != 2 {
				t.Fatalf("sources = %+v, want auth row and health row", out.Sources)
			}
			if out.Sources[0].Source != "daemon-a" || out.Sources[0].Status != SourceDegraded {
				t.Fatalf("auth row = %+v, want degraded", out.Sources[0])
			}
			h := out.Sources[1]
			if h.Source != "daemon-a/health" || h.Status != SourceDegraded {
				t.Fatalf("health row = %+v", h)
			}
			want := "down: the daemon did not answer; supervisor: not_registered; socket: /state/d.sock; store: age 2h0m0s"
			if h.Reason != want {
				t.Fatalf("reason = %q, want %q", h.Reason, want)
			}
			if got := out.ExitCode(); got != 3 {
				t.Fatalf("ExitCode = %d, want 3 (no healthy row)", got)
			}

			// With another backend healthy the same outcome exits 2.
			writeStatusBackend(t, "plain-b", authOKAnswer, "")
			out = v.run([]string{"daemon-a", "plain-b"})
			if got := out.ExitCode(); got != 2 {
				t.Fatalf("ExitCode = %d, want 2 (another backend is healthy)", got)
			}
		})
	}
}

func TestHealthRows_DownLocalReportWithoutStoreSaysAbsent(t *testing.T) {
	doc := backendStatusDoc{Health: "down", Source: "local", Socket: "/state/d.sock"}
	doc.Supervisor.State = "none"
	got := healthReason(doc)
	want := "down; supervisor: none; socket: /state/d.sock; store: absent"
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestHealthRows_UnknownOpStatusAddsNoRowAndOutputIsUnchanged(t *testing.T) {
	for _, v := range bothVerbs {
		t.Run(v.name, func(t *testing.T) {
			writeStatusBackend(t, "plain-a", authOKAnswer, "")
			out := v.run([]string{"plain-a"})
			want := []SourceResult{{Source: "plain-a", Status: SourceSucceeded, Count: wantOwnCount(v.name)}}
			if !reflect.DeepEqual(out.Sources, want) {
				t.Fatalf("sources = %+v, want %+v", out.Sources, want)
			}
			if got := out.ExitCode(); got != 0 {
				t.Fatalf("ExitCode = %d, want 0", got)
			}
		})
	}
}

func TestHealthRows_FailedStatusCallAddsNoRowAndChangesNothing(t *testing.T) {
	for _, v := range bothVerbs {
		t.Run(v.name, func(t *testing.T) {
			writeStatusBackend(t, "flaky-a", authOKAnswer, statusFailed)
			out := v.run([]string{"flaky-a"})
			want := []SourceResult{{Source: "flaky-a", Status: SourceSucceeded, Count: wantOwnCount(v.name)}}
			if !reflect.DeepEqual(out.Sources, want) {
				t.Fatalf("sources = %+v, want %+v", out.Sources, want)
			}
			if got := out.ExitCode(); got != 0 {
				t.Fatalf("ExitCode = %d, want 0", got)
			}
		})
	}
}

func TestHealthRows_NonDocumentAnswerIsNotDaemonBacked(t *testing.T) {
	// The one canned reply every op gets from a generic fake backend, and a
	// document whose health is none of the three words, are not a status
	// document.
	for _, answer := range []string{
		`{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`,
		`{"protocolVersion":1,"schemaVersion":1,"result":{"health":"great"}}`,
		`{"protocolVersion":1,"schemaVersion":1,"result":[1,2]}`,
		`{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	} {
		for _, v := range bothVerbs {
			t.Run(v.name+"/"+answer, func(t *testing.T) {
				writeStatusBackend(t, "odd-a", authOKAnswer, answer)
				out := v.run([]string{"odd-a"})
				if len(out.Sources) != 1 || out.Sources[0].Source != "odd-a" {
					t.Fatalf("sources = %+v, want the backend's own row only", out.Sources)
				}
			})
		}
	}
}

func TestHealthRows_SitInTheSamePlaceInBothVerbs(t *testing.T) {
	writeStatusBackend(t, "daemon-a", authOKAnswer, statusDegradedDoc)
	writeStatusBackend(t, "plain-b", authOKAnswer, "")
	writeStatusBackend(t, "daemon-c", authDownAnswer, statusDownLocalDoc)
	backends := []string{"daemon-a", "plain-b", "daemon-c"}
	wantOrder := []string{"daemon-a", "daemon-a/health", "plain-b", "daemon-c", "daemon-c/health"}
	byVerb := map[string]map[string]SourceResult{}
	for _, v := range bothVerbs {
		var got []string
		byVerb[v.name] = map[string]SourceResult{}
		for _, s := range v.run(backends).Sources {
			got = append(got, s.Source)
			byVerb[v.name][s.Source] = s
		}
		if !reflect.DeepEqual(got, wantOrder) {
			t.Fatalf("%s: row order = %v, want %v", v.name, got, wantOrder)
		}
	}
	for _, name := range []string{"daemon-a/health", "daemon-c/health"} {
		if a, c := byVerb["auth status"][name], byVerb["config validate"][name]; !reflect.DeepEqual(a, c) {
			t.Fatalf("%s differs between verbs: %+v vs %+v", name, a, c)
		}
	}
}

func TestHealthRows_HealthyReportWithReasonsStaysSucceeded(t *testing.T) {
	writeStatusBackend(t, "daemon-a", authOKAnswer,
		`{"protocolVersion":1,"schemaVersion":1,"result":{"health":"healthy","reasons":["change source unavailable"],"source":"daemon"}}`)
	out := FanOutAuthStatus(context.Background(), nil, []string{"daemon-a"})
	h := out.Sources[1]
	if h.Status != SourceSucceeded || !strings.Contains(h.Reason, "change source unavailable") {
		t.Fatalf("health row = %+v", h)
	}
	if out.ExitCode() != 0 {
		t.Fatalf("ExitCode = %d, want 0", out.ExitCode())
	}
}

func TestHealthRows_StatusRequestCarriesRegisteredConfigAndNoArgs(t *testing.T) {
	// The fake answers a healthy document only when the request names the
	// status op, carries its registered config block and has no args.
	dir := t.TempDir()
	script := "#!/bin/sh\nreq=$(cat)\n" +
		"if echo \"$req\" | grep -q '\"op\":\"status\"' && echo \"$req\" | grep -q '\"k\":\"v\"' && ! echo \"$req\" | grep -q '\"args\"'; then\n" +
		"cat <<'EOF_FAKE'\n" + statusHealthyDoc + "\nEOF_FAKE\nelse\ncat <<'EOF_FAKE'\n" + statusUnknownOp + "\nEOF_FAKE\nfi\n"
	if err := os.WriteFile(filepath.Join(dir, "cfg-a"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - cfg-a\nbackends:\n  cfg-a:\n    k: v\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	row, ok := backendStatusRow(context.Background(), reg, "cfg-a")
	if !ok || row.Status != SourceSucceeded {
		t.Fatalf("row = %+v ok=%v, want a succeeded health row", row, ok)
	}
}
