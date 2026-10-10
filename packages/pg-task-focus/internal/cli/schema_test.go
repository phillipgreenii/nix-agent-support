package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/schemas"
)

// cliOnly are the documents only the command line prints.
const cliOnly = `{
  "CheckOutput": {
    "type": "object",
    "description": "The output of check: whether a daemon would start on the log, what the next start would recover, and why not.",
    "required": ["ok", "path", "size_bytes", "lines", "batches", "recovery"],
    "additionalProperties": false,
    "properties": {
      "ok": { "type": "boolean" },
      "path": { "type": "string" },
      "size_bytes": { "type": "integer", "minimum": 0 },
      "lines": { "type": "integer", "minimum": 0 },
      "batches": { "type": "integer", "minimum": 0 },
      "recovery": {
        "type": "object",
        "required": ["torn_tail", "uncommitted_batches", "truncated_bytes"],
        "additionalProperties": false,
        "properties": {
          "torn_tail": { "type": "boolean" },
          "uncommitted_batches": { "type": "integer", "minimum": 0 },
          "truncated_bytes": { "type": "integer", "minimum": 0 }
        }
      },
      "problem": { "type": "string" },
      "line": { "type": "integer", "minimum": 1 },
      "invalid": {
        "type": "object",
        "required": ["code", "message", "events"],
        "additionalProperties": false,
        "properties": {
          "code": { "type": "string" },
          "message": { "type": "string" },
          "events": { "type": "array", "items": { "type": "string" } }
        }
      }
    }
  },
  "ConfigCheckOutput": {
    "type": "object",
    "description": "The output of config check: every problem with the JSON pointer of the value at fault.",
    "required": ["valid", "problems"],
    "additionalProperties": false,
    "properties": {
      "valid": { "type": "boolean" },
      "digest": { "type": "string" },
      "problems": {
        "type": "array",
        "items": {
          "type": "object",
          "required": ["path", "message"],
          "additionalProperties": false,
          "properties": { "path": { "type": "string" }, "message": { "type": "string" } }
        }
      }
    }
  }
}`

// The outputs that come from the API, by the name of their OpenAPI schema.
var fromAPI = []string{
	"State", "Result", "DryRunResult", "Events", "Problem", "Version", "Store", "Period", "Task", "KV", "AlertSettings",
	"CycleBase", "Cycle", "DimmedCycle", "ResumeOffer", "TaskRef", "NotMaterialized", "CycleRef", "Preview", "EventView",
	"ProblemDetails", "Reason", "Instant", "Date", "ULID", "Health",
}

// derive builds schemas/cli.schema.json from api/openapi.yaml: each schema the
// client prints, with its references rewritten from #/components/schemas/X to
// #/$defs/X, and the event schema (a separate file the API document refers
// to) left as a described object.
func derive(t *testing.T) []byte {
	t.Helper()
	var doc struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(api.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	defs := map[string]any{}
	for _, name := range fromAPI {
		s, ok := doc.Components.Schemas[name]
		if !ok {
			t.Fatalf("api/openapi.yaml has no schema %s", name)
		}
		defs[name] = s
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(cliOnly), &extra); err != nil {
		t.Fatal(err)
	}
	for k, v := range extra {
		defs[k] = v
	}
	raw, err := json.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "#/components/schemas/", "#/$defs/")
	text = strings.ReplaceAll(text, `{"$ref":"../schemas/event.schema.json"}`, `{"type":"object","description":"One event line, as event.schema.json defines it."}`)
	var rewritten map[string]any
	if err := json.Unmarshal([]byte(text), &rewritten); err != nil {
		t.Fatal(err)
	}
	var anyOf []any
	for _, n := range []string{"State", "Result", "DryRunResult", "Events", "Problem", "CheckOutput", "ConfigCheckOutput"} {
		anyOf = append(anyOf, map[string]any{"$ref": "#/$defs/" + n})
	}
	out := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id":     "https://pg-task-focus.invalid/schemas/cli.schema.json",
		"title":   "pg-task-focus command-line output",
		"description": "Every document a client verb prints with --json: the daemon's own JSON (State for status and each line of status --watch, " +
			"Result or DryRunResult for a mutation, Events for events list, Problem for a refusal) and the two the command line makes itself " +
			"(CheckOutput, ConfigCheckOutput). Derived from api/openapi.yaml by a test; do not edit the shared definitions here.",
		"anyOf": anyOf,
		"$defs": rewritten,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The checked-in schema is the one derived from the API document: the shared
// definitions cannot drift. UPDATE_CLI_SCHEMA=1 rewrites the file.
func TestCLISchemaIsDerivedFromTheAPI(t *testing.T) {
	want := derive(t)
	if os.Getenv("UPDATE_CLI_SCHEMA") == "1" {
		if err := os.WriteFile("../../schemas/cli.schema.json", want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Compared as JSON, not as bytes: the formatter owns the file's layout.
	var gotDoc, wantDoc any
	if err := json.Unmarshal(schemas.CLI(), &gotDoc); err != nil {
		t.Fatalf("schemas/cli.schema.json is not JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Fatalf("schemas/cli.schema.json drifted from api/openapi.yaml; run UPDATE_CLI_SCHEMA=1 go test ./internal/cli -run TestCLISchemaIsDerivedFromTheAPI")
	}
}
