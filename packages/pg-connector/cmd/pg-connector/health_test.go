package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// This file tests the caller-facing health verb (INTF-WIRE-HEALTH-1) through
// the real cobra command, in process, against shell-script fake backends: one
// that offers the status op (with a per-query baseline age), one that does not
// offer it and one whose status call fails. Helpers: capabilities_test.go,
// changes_forward_test.go.

const (
	// healthDocOpenBaseline is a status document whose query has no stored
	// baseline yet, so only baseline_open_age_seconds is present.
	healthDocOpenBaseline = `{"health":"degraded","reasons":["query team baseline not stored"],"source":"daemon","version":"1","queries":[{"name":"team","baseline_open_age_seconds":930}],"extra":{"nested":[1,2,3]}}`
	// healthDocStoredBaseline carries baseline_at once the baseline is stored.
	healthDocStoredBaseline = `{"health":"healthy","source":"daemon","queries":[{"name":"mine","baseline_at":"2026-01-02T03:04:05Z"}]}`
	// healthDocDown is what a daemon-backed client answers locally while its
	// daemon is down.
	healthDocDown = `{"health":"down","reasons":["the daemon did not answer"],"source":"local","queries":[]}`
)

func healthEnvelope(doc string) string {
	return `{"protocolVersion":1,"schemaVersion":1,"result":` + doc + `}`
}

// healthStatusBackend writes a recording fake backend answering status with doc.
func healthStatusBackend(t *testing.T, name, doc string) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), name+".log")
	writeRecordingBackend(t, name, logPath, map[string]string{"status": healthEnvelope(doc)})
	return logPath
}

// healthNoStatusBackend writes a fake backend that does not offer status: it
// answers the op with unknown_op.
func healthNoStatusBackend(t *testing.T, name string) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), name+".log")
	writeRecordingBackend(t, name, logPath, map[string]string{"status": fwdErrorEnvelope("unknown_op", "no such op")})
	return logPath
}

// healthFailingBackend writes a fake backend whose status op exits non-zero.
func healthFailingBackend(t *testing.T, name string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), name+".log")
	script := fmt.Sprintf("#!/bin/sh\nreq=$(cat)\nprintf '%%s\\n' \"$req\" >> '%s'\necho 'status exploded' >&2\nexit 7\n", logPath)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

type healthWire struct {
	Sources []struct {
		Source string `json:"source"`
		Status string `json:"status"`
		Count  int    `json:"count"`
		Reason string `json:"reason"`
	} `json:"sources"`
	Backends []struct {
		Backend string          `json:"backend"`
		Status  json.RawMessage `json:"status"`
	} `json:"backends"`
}

func decodeHealthWire(t *testing.T, stdout string) healthWire {
	t.Helper()
	var w healthWire
	if err := json.Unmarshal([]byte(stdout), &w); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	return w
}

func healthDecodeValue(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return v
}

func TestHealth_OfferingBackends_ReturnTheirOwnDocumentUnmodified(t *testing.T) {
	stateDir := capsConfig(t, "issue", "", "backend-health-a", "backend-health-b")
	logA := healthStatusBackend(t, "backend-health-a", healthDocOpenBaseline)
	logB := healthStatusBackend(t, "backend-health-b", healthDocStoredBaseline)

	stdout, stderr, code := runCapsArgs(t, []string{"issue", "health", "--output", "json"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s stderr=%s", code, stdout, stderr)
	}
	w := decodeHealthWire(t, stdout)
	if len(w.Sources) != 2 || len(w.Backends) != 2 {
		t.Fatalf("want 2 sources and 2 backends: %s", stdout)
	}
	for i, name := range []string{"backend-health-a", "backend-health-b"} {
		s := w.Sources[i]
		if s.Source != name || s.Status != "succeeded" || s.Count != 1 || s.Reason != "" {
			t.Fatalf("sources[%d] = %+v", i, s)
		}
		if w.Backends[i].Backend != name {
			t.Fatalf("backends[%d].backend = %q, want %q", i, w.Backends[i].Backend, name)
		}
	}
	if got, want := healthDecodeValue(t, string(w.Backends[0].Status)), healthDecodeValue(t, healthDocOpenBaseline); !reflect.DeepEqual(got, want) {
		t.Fatalf("backend a status = %v, want the backend's own document %v", got, want)
	}
	if got, want := healthDecodeValue(t, string(w.Backends[1].Status)), healthDecodeValue(t, healthDocStoredBaseline); !reflect.DeepEqual(got, want) {
		t.Fatalf("backend b status = %v, want %v", got, want)
	}
	// The per-query baseline age rides through to the caller.
	var a struct {
		Queries []struct {
			Name string `json:"name"`
			Age  *int64 `json:"baseline_open_age_seconds"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(w.Backends[0].Status, &a); err != nil || len(a.Queries) != 1 || a.Queries[0].Age == nil || *a.Queries[0].Age != 930 {
		t.Fatalf("baseline_open_age_seconds not carried: %v %+v", err, a)
	}

	// Exactly one status request per backend and nothing else; no ledger or cache.
	for name, logPath := range map[string]string{"a": logA, "b": logB} {
		if ops := opsOf(readRequestLog(t, logPath)); !reflect.DeepEqual(ops, []string{"status"}) {
			t.Fatalf("backend %s requests = %v, want exactly one status", name, ops)
		}
	}
	assertEmptyStateDirs(t, stateDir)
}

func TestHealth_DaemonDownReport_IsTheDocumentNotAFailedRead(t *testing.T) {
	capsConfig(t, "issue", "", "backend-health-down")
	healthStatusBackend(t, "backend-health-down", healthDocDown)

	stdout, _, code := runCapsArgs(t, []string{"issue", "health"})
	if code != 0 {
		t.Fatalf("a down daemon's local report is a successful read, exit code = %d; stdout=%s", code, stdout)
	}
	w := decodeHealthWire(t, stdout)
	if len(w.Backends) != 1 || w.Sources[0].Status != "succeeded" {
		t.Fatalf("answer = %s", stdout)
	}
	var doc struct {
		Health string `json:"health"`
	}
	if err := json.Unmarshal(w.Backends[0].Status, &doc); err != nil || doc.Health != "down" {
		t.Fatalf("health = %q err=%v, want down", doc.Health, err)
	}
}

func TestHealth_BackendWithoutStatusOp_IsDisabledRowAndHealthy(t *testing.T) {
	stateDir := capsConfig(t, "issue", "", "backend-health-ok", "backend-health-none")
	healthStatusBackend(t, "backend-health-ok", healthDocStoredBaseline)
	healthNoStatusBackend(t, "backend-health-none")

	stdout, _, code := runCapsArgs(t, []string{"issue", "health"})
	if code != 0 {
		t.Fatalf("a disabled row counts as healthy, exit code = %d; stdout=%s", code, stdout)
	}
	w := decodeHealthWire(t, stdout)
	if len(w.Sources) != 2 {
		t.Fatalf("sources = %+v", w.Sources)
	}
	s := w.Sources[1]
	if s.Source != "backend-health-none" || s.Status != "disabled" || s.Count != 0 || s.Reason != "status op not offered" {
		t.Fatalf("sources[1] = %+v", s)
	}
	if len(w.Backends) != 1 || w.Backends[0].Backend != "backend-health-ok" {
		t.Fatalf("a backend without status must have no backends entry: %s", stdout)
	}
	assertEmptyStateDirs(t, stateDir)
}

func TestHealth_FailedBackend_IsDegradedRowWithoutBackendsEntry_ExitCodeFollowsFanOutScheme(t *testing.T) {
	stateDir := capsConfig(t, "issue", "", "backend-health-fine", "backend-health-bad")
	healthStatusBackend(t, "backend-health-fine", healthDocStoredBaseline)
	healthFailingBackend(t, "backend-health-bad")

	stdout, _, code := runCapsArgs(t, []string{"issue", "health"})
	if code != 2 {
		t.Fatalf("partial failure exit code = %d, want 2; stdout=%s", code, stdout)
	}
	w := decodeHealthWire(t, stdout)
	if len(w.Sources) != 2 || w.Sources[0].Status != "succeeded" || w.Sources[1].Source != "backend-health-bad" ||
		w.Sources[1].Status != "degraded" || w.Sources[1].Count != 0 || w.Sources[1].Reason == "" {
		t.Fatalf("sources = %+v", w.Sources)
	}
	if len(w.Backends) != 1 || w.Backends[0].Backend != "backend-health-fine" {
		t.Fatalf("a failed backend must have no backends entry: %s", stdout)
	}

	// Pinned to the failing backend alone, every backend failed: exit 3 with
	// the document still printed.
	stdout, _, code = runCapsArgs(t, []string{"issue", "health", "--backend", "backend-health-bad"})
	if code != 3 {
		t.Fatalf("total failure exit code = %d, want 3; stdout=%s", code, stdout)
	}
	w = decodeHealthWire(t, stdout)
	if len(w.Sources) != 1 || w.Sources[0].Status != "degraded" || len(w.Backends) != 0 {
		t.Fatalf("total failure answer = %s", stdout)
	}
	if !strings.Contains(stdout, `"backends":[]`) {
		t.Fatalf("backends must be an empty array, not null: %s", stdout)
	}
	assertEmptyStateDirs(t, stateDir)
}

func TestHealth_BackendFlag_AsksOnlyThatBackend(t *testing.T) {
	stateDir := capsConfig(t, "issue", "", "backend-health-p1", "backend-health-p2")
	log1 := healthStatusBackend(t, "backend-health-p1", healthDocOpenBaseline)
	log2 := healthStatusBackend(t, "backend-health-p2", healthDocStoredBaseline)

	stdout, _, code := runCapsArgs(t, []string{"issue", "health", "--backend", "backend-health-p2"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	w := decodeHealthWire(t, stdout)
	if len(w.Sources) != 1 || w.Sources[0].Source != "backend-health-p2" || len(w.Backends) != 1 || w.Backends[0].Backend != "backend-health-p2" {
		t.Fatalf("answer = %s", stdout)
	}
	if n := len(readRequestLog(t, log1)); n != 0 {
		t.Fatalf("the unpinned backend was asked %d times", n)
	}
	if ops := opsOf(readRequestLog(t, log2)); !reflect.DeepEqual(ops, []string{"status"}) {
		t.Fatalf("pinned backend requests = %v", ops)
	}
	assertEmptyStateDirs(t, stateDir)
}

func TestHealth_UnknownBackend_FailsInvalidArgument_NoBackendCall(t *testing.T) {
	stateDir := capsConfig(t, "issue", "", "backend-health-u1")
	log1 := healthStatusBackend(t, "backend-health-u1", healthDocStoredBaseline)

	stdout, _, code := runCapsArgs(t, []string{"issue", "health", "--backend", "no-such-backend"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "invalid_argument") {
		t.Fatalf("want an invalid_argument error: %s", stdout)
	}
	if n := len(readRequestLog(t, log1)); n != 0 {
		t.Fatalf("a backend was called %d times for an unknown --backend", n)
	}
	assertEmptyStateDirs(t, stateDir)
}

func TestHealth_HumanOutput_ShowsBaselineStateOfEachQuery(t *testing.T) {
	capsConfig(t, "issue", "", "backend-health-h")
	healthStatusBackend(t, "backend-health-h", healthDocOpenBaseline)

	stdout, _, code := runCapsArgs(t, []string{"issue", "health", "--output", "human"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "backend-health-h: health degraded") || !strings.Contains(stdout, "query team: baseline open for 930s") {
		t.Fatalf("human output = %q", stdout)
	}
}
