package schemacheck_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/schemacheck"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/schemas"
)

func eventSchema(t *testing.T) *schemacheck.Schema {
	t.Helper()
	s, err := schemacheck.Compile("event.schema.json", schemas.Event())
	if err != nil {
		t.Fatalf("Compile(event schema): %v", err)
	}
	return s
}

// fixtureLines reads every file of dir matching pattern and returns its single
// line, without the newline, keyed by the file's base name.
func fixtureLines(t *testing.T, pattern string) map[string][]byte {
	t.Helper()
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(raw), "\n") != 1 || !strings.HasSuffix(string(raw), "\n") {
			t.Fatalf("%s MUST hold exactly one line ending in a newline", f)
		}
		out[filepath.Base(f)] = []byte(strings.TrimSuffix(string(raw), "\n"))
	}
	return out
}

func violations(t *testing.T, err error) []schemacheck.Violation {
	t.Helper()
	var verr *schemacheck.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error %v is not a *schemacheck.ValidationError", err)
	}
	return verr.Violations
}

func TestValidLinesPass(t *testing.T) {
	s := eventSchema(t)
	lines := fixtureLines(t, "../../testdata/events/*.jsonl")
	if len(lines) < len(event.Types()) {
		t.Fatalf("found %d valid fixtures, want at least one per event type (%d)", len(lines), len(event.Types()))
	}
	for name, line := range lines {
		t.Run(name, func(t *testing.T) {
			if err := s.Validate(line); err != nil {
				t.Errorf("Validate: %v", err)
			}
			if _, err := event.Decode(line); err != nil {
				t.Errorf("Decode: %v", err)
			}
		})
	}
}

func TestInvalidLinesFail(t *testing.T) {
	s := eventSchema(t)
	// pointer is the JSON pointer of one of the violations and message a part
	// of that violation's message, so a line fails for the reason it is there.
	table := []struct{ file, pointer, message string }{
		{"wrong-version.jsonl", "/v", "must be 1"},
		{"task.skipped.no-reason.jsonl", "/data", "reason"},
		{"task.skipped.empty-reason.jsonl", "/data/reason", "minLength"},
		{"period.changed.week-without-end.jsonl", "/data", "end"},
		{"period.changed.day-with-end.jsonl", "/data/end", "false"},
		{"event.retracted.both-target-and-target-batch.jsonl", "/data", "oneOf"},
		{"event.retracted.neither.jsonl", "/data", "target"},
		{"event.retracted.with-batch.jsonl", "/data", "batch"},
		{"cycle.started.zero-minutes.jsonl", "/data/planned_minutes", "minimum"},
		{"cycle.started.minutes-525601.jsonl", "/data/planned_minutes", "maximum"},
		{"cycle.boosted.minutes-525601.jsonl", "/data/minutes", "maximum"},
		{"task.materialized.without-due.jsonl", "/data", "due"},
		{"task.completed.carry-over.jsonl", "/data", "carry_over"},
		{"period.changed.without-batch.jsonl", "/data", "batch"},
		{"profile.changed.without-batch.jsonl", "/data", "batch"},
		{"task.materialized.without-batch.jsonl", "/data", "batch"},
		{"task.missed.without-batch.jsonl", "/data", "batch"},
		{"task.withdrawn.without-batch.jsonl", "/data", "batch"},
		{"task.reinstated.without-batch.jsonl", "/data", "batch"},
		{"batch.committed.without-batch.jsonl", "/data", "batch"},
	}
	lines := fixtureLines(t, "../../testdata/events/invalid/*.jsonl")
	listed := map[string]bool{}
	for _, c := range table {
		listed[c.file] = true
		t.Run(c.file, func(t *testing.T) {
			line, ok := lines[c.file]
			if !ok {
				t.Fatalf("no fixture testdata/events/invalid/%s", c.file)
			}
			err := s.Validate(line)
			if err == nil {
				t.Fatal("Validate succeeded, want a violation")
			}
			found := false
			for _, v := range violations(t, err) {
				if v.Pointer == c.pointer && strings.Contains(strings.ToLower(v.Message), strings.ToLower(c.message)) {
					found = true
				}
			}
			if !found {
				t.Errorf("no violation at %q mentioning %q in:\n%v", c.pointer, c.message, err)
			}
			if _, err := event.Decode(line); err == nil {
				t.Error("Decode succeeded, want an error")
			}
		})
	}
	for file := range lines {
		if !listed[file] {
			t.Errorf("fixture testdata/events/invalid/%s is not in the table", file)
		}
	}
}

func TestDecodeReportsAnUnknownVersionNotASchemaFailure(t *testing.T) {
	line := fixtureLines(t, "../../testdata/events/invalid/wrong-version.jsonl")["wrong-version.jsonl"]
	_, err := event.Decode(line)
	var uv *event.UnknownVersionError
	if !errors.As(err, &uv) || uv.V != 2 {
		t.Fatalf("Decode = %v, want *UnknownVersionError{V: 2}", err)
	}
}

func TestSchemaLeavesBlankReasonToTheGoCheck(t *testing.T) {
	s := eventSchema(t)
	for _, reason := range []string{" ", "  ", "\\t\\n", "\\u00a0", "\\u3000"} {
		line := []byte(`{"v":1,"id":"01J9Z3K8M2E000000000000008","at":"2026-10-07T13:30:04.120Z","effective_at":"2026-10-07T13:28:00.000Z","type":"task.skipped","data":{"task_id":"day:2026-10-07:x","reason":"` + reason + `"}}`)
		if err := s.Validate(line); err != nil {
			t.Errorf("reason %q: the schema refused it, but blank is the Go check's to decide: %v", reason, err)
		}
		if _, err := event.Decode(line); err == nil {
			t.Errorf("reason %q: Decode accepted a blank reason", reason)
		}
	}
}

func TestValidateReportsEveryViolation(t *testing.T) {
	s := eventSchema(t)
	// Three independent faults: an id that is not a ULID, a minutes value out
	// of range and an unexpected field.
	line := []byte(`{"v":1,"id":"x","at":"2026-10-07T13:30:04.120Z","effective_at":"2026-10-07T13:28:00.000Z","type":"cycle.boosted","data":{"cycle_id":"c","minutes":0,"carry_over":true}}`)
	got := violations(t, s.Validate(line))
	var pointers []string
	for _, v := range got {
		pointers = append(pointers, v.Pointer)
	}
	for _, want := range []string{"/id", "/data/minutes", "/data"} {
		if !slices.Contains(pointers, want) {
			t.Errorf("no violation at %q; got pointers %v", want, pointers)
		}
	}
	if len(got) != 3 {
		t.Errorf("%d violations, want 3: %v", len(got), got)
	}
	msg := s.Validate(line).Error()
	for _, want := range []string{`"/id"`, `"/data/minutes"`, "carry_over"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q lacks %s", msg, want)
		}
	}
}

func TestValidateRejectsAnythingThatIsNotOneJSONValue(t *testing.T) {
	s := eventSchema(t)
	for _, in := range []string{``, `{`, `{} {}`, `nope`} {
		err := s.Validate([]byte(in))
		if err == nil {
			t.Errorf("Validate(%q) succeeded", in)
			continue
		}
		var verr *schemacheck.ValidationError
		if errors.As(err, &verr) {
			t.Errorf("Validate(%q) = a schema violation, want a plain JSON error", in)
		}
	}
}

func TestCompileRejectsBadSchemas(t *testing.T) {
	cases := map[string]string{
		"not json":                 `{`,
		"not an object":            `[]`,
		"an unknown type":          `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"strng"}`,
		"a reference to a file":    `{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"other.json"}`,
		"a reference to a url":     `{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"https://example.test/x.json"}`,
		"a reference that is gone": `{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"#/$defs/missing"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := schemacheck.Compile("bad.schema.json", []byte(raw)); err == nil {
				t.Error("Compile succeeded")
			}
		})
	}
}

// TestFreeTextInventory pins which properties of the event schema are free
// text. A property is either in the free-text set or listed here as not free
// text, so a new string property cannot appear without a deliberate choice.
func TestFreeTextInventory(t *testing.T) {
	s := eventSchema(t)

	wantFree := []string{
		"period.changed.label",
		"task.skipped.reason",
		"event.corrected.reason",
		"event.retracted.reason",
		"cycle.annotated.note",
		"cycle.annotated.kv[].value",
		"event.corrected.fields.reason",
		"event.corrected.fields.note",
		"event.corrected.fields.label",
		"event.corrected.fields.kv[].value",
	}
	notFree := []string{
		// the envelope: ids, instants, the hash and the discriminator
		"envelope.id", "envelope.at", "envelope.effective_at", "envelope.req_hash", "envelope.type",

		"period.changed.kind", "period.changed.start", "period.changed.end", "period.changed.tz", "period.changed.batch",
		"profile.changed.profile", "profile.changed.batch",
		"task.materialized.task_id", "task.materialized.definition", "task.materialized.cadence", "task.materialized.period",
		"task.materialized.title", "task.materialized.group", "task.materialized.link", "task.materialized.due",
		"task.materialized.batch", "task.materialized.due_rule.at", "task.materialized.due_rule.tz", "task.materialized.due_rule.weekday",
		"task.completed.task_id",
		"task.skipped.task_id", "task.skipped.batch",
		"task.missed.task_id", "task.missed.batch",
		"task.withdrawn.task_id", "task.withdrawn.batch",
		"task.reinstated.task_id", "task.reinstated.batch",
		"cycle.started.cycle_id", "cycle.started.type", "cycle.started.title", "cycle.started.interrupts",
		"cycle.paused.cycle_id", "cycle.paused.batch",
		"cycle.resumed.cycle_id", "cycle.resumed.batch",
		"cycle.boosted.cycle_id",
		"cycle.stopped.cycle_id",
		"cycle.annotated.cycle_id", "cycle.annotated.kv[].key",
		"event.corrected.target",
		"event.corrected.fields.effective_at", "event.corrected.fields.kind", "event.corrected.fields.start",
		"event.corrected.fields.end", "event.corrected.fields.tz", "event.corrected.fields.batch",
		"event.corrected.fields.profile", "event.corrected.fields.task_id", "event.corrected.fields.definition",
		"event.corrected.fields.cadence", "event.corrected.fields.period", "event.corrected.fields.title",
		"event.corrected.fields.group", "event.corrected.fields.link", "event.corrected.fields.due",
		"event.corrected.fields.due_rule.at", "event.corrected.fields.due_rule.tz", "event.corrected.fields.due_rule.weekday",
		"event.corrected.fields.cycle_id", "event.corrected.fields.type", "event.corrected.fields.interrupts",
		"event.corrected.fields.target", "event.corrected.fields.target_batch", "event.corrected.fields.kv[].key",
		"event.retracted.target", "event.retracted.target_batch",
		"batch.committed.batch",
	}

	var gotFree, gotStrings []string
	for _, def := range append([]string{"envelope"}, typeNames()...) {
		lookup := def
		if def == "envelope" {
			lookup = ""
		}
		for _, p := range schemacheck.FreeTextFields(s, lookup) {
			gotFree = append(gotFree, def+"."+p)
		}
		for _, p := range schemacheck.StringFields(s, lookup) {
			gotStrings = append(gotStrings, def+"."+p)
		}
	}
	sameSet(t, "free-text properties", gotFree, wantFree)
	sameSet(t, "string properties that are not free text", subtract(gotStrings, gotFree), notFree)
}

func typeNames() []string {
	var out []string
	for _, ty := range event.Types() {
		out = append(out, string(ty))
	}
	return out
}

func sameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	got, want = sortedUnique(got), sortedUnique(want)
	for _, g := range got {
		if !slices.Contains(want, g) {
			t.Errorf("%s: %q is in the schema but not in the test's list", what, g)
		}
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("%s: %q is in the test's list but not in the schema", what, w)
		}
	}
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}

func subtract(all, remove []string) []string {
	var out []string
	for _, s := range all {
		if !slices.Contains(remove, s) {
			out = append(out, s)
		}
	}
	return out
}

func TestFreeTextFieldsOfAnUnknownDefinitionIsEmpty(t *testing.T) {
	s := eventSchema(t)
	if got := schemacheck.FreeTextFields(s, "task.exploded"); len(got) != 0 {
		t.Errorf("FreeTextFields(unknown) = %v, want none", got)
	}
}
