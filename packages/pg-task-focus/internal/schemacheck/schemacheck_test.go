package schemacheck_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
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
		{"batch.committed.extra-property.jsonl", "/data", "carry_over"},
		{"cycle.stopped.extra-property.jsonl", "/data", "carry_over"},
		{"task.materialized.due-rule-extra-property.jsonl", "/data/due_rule", "carry_over"},
		{"cycle.annotated.kv-key-bad.jsonl", "/data/kv/0/key", "match"},
		{"cycle.annotated.kv-key-empty.jsonl", "/data/kv/0/key", "minLength"},
		{"event.corrected.fields-unknown-key.jsonl", "/data/fields", "carry_over"},
		{"event.corrected.fields-minutes-zero.jsonl", "/data/fields/minutes", "minimum"},
		{"event.corrected.fields-minutes-525601.jsonl", "/data/fields/minutes", "maximum"},
		{"event.corrected.fields-kind-x.jsonl", "/data/fields/kind", "must be one of"},
		{"event.corrected.fields-kv-bad-key.jsonl", "/data/fields/kv/0/key", "match"},
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
	// A line the schema accepts and only a Go check refuses: the schema checks
	// the shape of a value and the Go check its meaning, so each of these
	// passes Validate and fails Decode with the path of the field at fault.
	goOnly := []struct{ file, message string }{
		{"event.corrected.blank-reason.jsonl", "fields.reason"},
		{"event.corrected.impossible-instant.jsonl", "fields.effective_at"},
		{"event.corrected.impossible-date.jsonl", "fields.end"},
		{"event.corrected.bad-due-rule.jsonl", "fields.due_rule"},
		{"period.changed.week-end-before-start.jsonl", "end"},
		// encoding/json would keep the last of a repeated key, so the schema
		// sees a valid line; Decode refuses it and names the key and its object.
		{"duplicate-type.jsonl", `duplicate key "type" at /`},
		{"duplicate-task-id.jsonl", `duplicate key "task_id" at /data`},
		{"duplicate-due-rule-key.jsonl", `duplicate key "at" at /data/due_rule`},
		{"duplicate-fields-key.jsonl", `duplicate key "minutes" at /data/fields`},
		{"duplicate-v-1-then-1.jsonl", `duplicate key "v" at /`},
		{"duplicate-v-2-then-1.jsonl", "event version 2 is not known"},
	}
	for _, c := range goOnly {
		listed[c.file] = true
		t.Run(c.file, func(t *testing.T) {
			line, ok := lines[c.file]
			if !ok {
				t.Fatalf("no fixture testdata/events/invalid/%s", c.file)
			}
			if err := s.Validate(line); err != nil {
				t.Errorf("Validate: %v, but only a Go check is meant to refuse this line", err)
			}
			_, err := event.Decode(line)
			if err == nil {
				t.Fatal("Decode succeeded, want an error")
			}
			if !strings.Contains(err.Error(), c.message) {
				t.Errorf("Decode = %v, want it to name %q", err, c.message)
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

// INV-LOG-2: a line that writes v more than once is judged by every one of
// its v values first, so "v":2 followed by "v":1 is an unknown version and not
// a version 1 line, however it is ordered.
func TestDecodeJudgesARepeatedVByAllItsValues(t *testing.T) {
	line := fixtureLines(t, "../../testdata/events/invalid/duplicate-v-2-then-1.jsonl")["duplicate-v-2-then-1.jsonl"]
	for name, in := range map[string][]byte{
		"2 then 1":        line,
		"1 then 2":        bytes.Replace(line, []byte(`"v":2,"v":1`), []byte(`"v":1,"v":2`), 1),
		"1 then 1.0":      bytes.Replace(line, []byte(`"v":2,"v":1`), []byte(`"v":1,"v":1.0`), 1),
		"a string then 3": bytes.Replace(line, []byte(`"v":2,"v":1`), []byte(`"v":"x","v":3`), 1),
		"2 then a string": bytes.Replace(line, []byte(`"v":2,"v":1`), []byte(`"v":2,"v":"1"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := event.Decode(in)
			var uv *event.UnknownVersionError
			if !errors.As(err, &uv) {
				t.Fatalf("Decode(%.60s) = %v, want an *UnknownVersionError", in, err)
			}
		})
	}
	for name, v := range map[string]string{"1 then 1": `"v":1,"v":1`, "a string then 1": `"v":"1","v":1`, "null then 1": `"v":null,"v":1`} {
		t.Run(name, func(t *testing.T) {
			in := bytes.Replace(line, []byte(`"v":2,"v":1`), []byte(v), 1)
			_, err := event.Decode(in)
			var uv *event.UnknownVersionError
			if err == nil || errors.As(err, &uv) {
				t.Fatalf("Decode(%.60s) = %v, want an ordinary error, not an unknown version", in, err)
			}
		})
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

func configSchema(t *testing.T) *schemacheck.Schema {
	t.Helper()
	s, err := schemacheck.Compile("config.schema.json", schemas.Config())
	if err != nil {
		t.Fatalf("Compile(config schema): %v", err)
	}
	return s
}

// validConfig is the generic example of the configuration contract: every
// section, every optional key and one task of each cadence.
const validConfig = `{
  "defaults": {
    "cycle_minutes": 25,
    "boost_minutes": [5, 10, 25],
    "profile": "normal",
    "max_future_skew_seconds": 60,
    "alert": {"sound": "Glass", "reminder_sound": "Tink", "repeat_minutes": 5},
    "attention": {"due_soon_minutes": 30, "overtime_high_minutes": 15, "stale_pause_minutes": 45}
  },
  "listen_port": 49210,
  "public_url": "https://focus.example.test",
  "group_order": ["Start of day", "During the day", "End of day"],
  "profiles": {
    "normal": {
      "daily": ["plan-day", "end-of-day-summary"],
      "weekly": ["weekly-update"],
      "sprint": ["capacity-check"],
      "cycles": ["review", "deep-work"]
    }
  },
  "tasks": {
    "plan-day": {"title": "Plan the day", "cadence": "daily", "group": "Start of day", "link": "https://tasks.example.test/plan", "due": {"at": "09:00", "tz": "America/New_York"}},
    "end-of-day-summary": {"title": "Post the summary", "cadence": "daily", "group": "End of day", "due": {"at": "17:30", "tz": "America/New_York"}},
    "weekly-update": {"title": "Write the weekly update", "cadence": "weekly", "due": {"weekday": "thu", "at": "09:00", "tz": "America/New_York"}},
    "capacity-check": {"title": "Do the capacity check", "cadence": "sprint", "due": {"day": 1, "at": "09:00", "tz": "America/New_York"}}
  },
  "cycles": {
    "review": {"title": "Review cycle", "minutes": 25, "keys": ["pr"]},
    "deep-work": {"title": "Deep work cycle", "minutes": 50, "keys": ["ticket", "pr"], "alert": {"sound": "Hero", "repeat_minutes": 10}}
  }
}`

// mutated returns validConfig after f has edited it.
func mutated(t *testing.T, f func(c map[string]any)) []byte {
	t.Helper()
	var c map[string]any
	if err := json.Unmarshal([]byte(validConfig), &c); err != nil {
		t.Fatal(err)
	}
	f(c)
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func obj(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		m = m[p].(map[string]any)
	}
	return m
}

func TestConfigSchemaAcceptsTheGenericExample(t *testing.T) {
	if err := configSchema(t).Validate([]byte(validConfig)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestConfigSchemaAcceptsTheMinimalConfig(t *testing.T) {
	minimal := []byte(`{
	  "defaults": {"cycle_minutes": 25, "profile": "p", "alert": {"sound": "Glass", "repeat_minutes": 5}},
	  "listen_port": 1,
	  "profiles": {"p": {}},
	  "tasks": {},
	  "cycles": {}
	}`)
	if err := configSchema(t).Validate(minimal); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestConfigSchemaRejectsOperatorRuledOptions holds the two rulings at the
// schema level: no option can acknowledge, mute, snooze or cap the overtime
// sound, and none can carry an unfinished task over. Each key is tried in
// every object of the configuration and must fail naming the object and the key.
func TestConfigSchemaRejectsOperatorRuledOptions(t *testing.T) {
	s := configSchema(t)
	places := []struct {
		pointer string
		path    []string
	}{
		{"", nil},
		{"/defaults", []string{"defaults"}},
		{"/defaults/alert", []string{"defaults", "alert"}},
		{"/defaults/attention", []string{"defaults", "attention"}},
		{"/profiles/normal", []string{"profiles", "normal"}},
		{"/tasks/plan-day", []string{"tasks", "plan-day"}},
		{"/tasks/plan-day/due", []string{"tasks", "plan-day", "due"}},
		{"/cycles/review", []string{"cycles", "review"}},
		{"/cycles/deep-work/alert", []string{"cycles", "deep-work", "alert"}},
	}
	for _, key := range []string{"snooze_minutes", "mute", "max_repeats", "carry_over", "acknowledge", "day_start", "switched"} {
		for _, place := range places {
			t.Run(key+" at "+place.pointer, func(t *testing.T) {
				doc := mutated(t, func(c map[string]any) { obj(c, place.path...)[key] = 3 })
				err := s.Validate(doc)
				if err == nil {
					t.Fatalf("Validate accepted %s at %q", key, place.pointer)
				}
				found := false
				for _, v := range violations(t, err) {
					if v.Pointer == place.pointer && strings.Contains(v.Message, key) {
						found = true
					}
				}
				if !found {
					t.Errorf("no violation at %q naming %q in:\n%v", place.pointer, key, err)
				}
			})
		}
	}
}

func TestConfigSchemaRejects(t *testing.T) {
	s := configSchema(t)
	cases := []struct {
		name    string
		edit    func(c map[string]any)
		pointer string
		message string
	}{
		{"a missing listen_port", func(c map[string]any) { delete(c, "listen_port") }, "", "listen_port"},
		{"a missing defaults", func(c map[string]any) { delete(c, "defaults") }, "", "defaults"},
		{"a missing profiles", func(c map[string]any) { delete(c, "profiles") }, "", "profiles"},
		{"a missing defaults.profile", func(c map[string]any) { delete(obj(c, "defaults"), "profile") }, "/defaults", "profile"},
		{"a missing defaults.alert", func(c map[string]any) { delete(obj(c, "defaults"), "alert") }, "/defaults", "alert"},
		{"a missing task due", func(c map[string]any) { delete(obj(c, "tasks", "plan-day"), "due") }, "/tasks/plan-day", "due"},
		{"a missing due tz", func(c map[string]any) { delete(obj(c, "tasks", "plan-day", "due"), "tz") }, "/tasks/plan-day/due", "tz"},
		{"an empty due tz", func(c map[string]any) { obj(c, "tasks", "plan-day", "due")["tz"] = "" }, "/tasks/plan-day/due/tz", "minLength"},
		{"a missing due at", func(c map[string]any) { delete(obj(c, "tasks", "plan-day", "due"), "at") }, "/tasks/plan-day/due", "at"},
		{"a due time of 9:00", func(c map[string]any) { obj(c, "tasks", "plan-day", "due")["at"] = "9:00" }, "/tasks/plan-day/due/at", "match"},
		{"a due time of 24:00", func(c map[string]any) { obj(c, "tasks", "plan-day", "due")["at"] = "24:00" }, "/tasks/plan-day/due/at", "match"},
		{"a due time of 09:60", func(c map[string]any) { obj(c, "tasks", "plan-day", "due")["at"] = "09:60" }, "/tasks/plan-day/due/at", "match"},
		{"a weekday of thursday", func(c map[string]any) { obj(c, "tasks", "weekly-update", "due")["weekday"] = "thursday" }, "/tasks/weekly-update/due/weekday", "must be one of"},
		{"a due day of 0", func(c map[string]any) { obj(c, "tasks", "capacity-check", "due")["day"] = 0 }, "/tasks/capacity-check/due/day", "minimum"},
		{"a cadence of monthly", func(c map[string]any) { obj(c, "tasks", "plan-day")["cadence"] = "monthly" }, "/tasks/plan-day/cadence", "must be one of"},
		{"cycle minutes of 0", func(c map[string]any) { obj(c, "cycles", "review")["minutes"] = 0 }, "/cycles/review/minutes", "minimum"},
		{"cycle minutes of 525601", func(c map[string]any) { obj(c, "cycles", "review")["minutes"] = 525601 }, "/cycles/review/minutes", "maximum"},
		{"fractional cycle minutes", func(c map[string]any) { obj(c, "cycles", "review")["minutes"] = 1.5 }, "/cycles/review/minutes", "integer"},
		{"a default cycle_minutes of 0", func(c map[string]any) { obj(c, "defaults")["cycle_minutes"] = 0 }, "/defaults/cycle_minutes", "minimum"},
		{"a boost of 0", func(c map[string]any) { obj(c, "defaults")["boost_minutes"] = []any{5, 0} }, "/defaults/boost_minutes/1", "minimum"},
		{"a boost of 525601", func(c map[string]any) { obj(c, "defaults")["boost_minutes"] = []any{525601} }, "/defaults/boost_minutes/0", "maximum"},
		{"a repeat_minutes of -1", func(c map[string]any) { obj(c, "defaults", "alert")["repeat_minutes"] = -1 }, "/defaults/alert/repeat_minutes", "minimum"},
		{"a cycle repeat_minutes of 0", func(c map[string]any) { obj(c, "cycles", "deep-work", "alert")["repeat_minutes"] = 0 }, "/cycles/deep-work/alert/repeat_minutes", "minimum"},
		{"an attention threshold of 0", func(c map[string]any) { obj(c, "defaults", "attention")["due_soon_minutes"] = 0 }, "/defaults/attention/due_soon_minutes", "minimum"},
		{"a negative max_future_skew_seconds", func(c map[string]any) { obj(c, "defaults")["max_future_skew_seconds"] = -1 }, "/defaults/max_future_skew_seconds", "minimum"},
		{"a key with a space", func(c map[string]any) { obj(c, "cycles", "review")["keys"] = []any{"Has Space"} }, "/cycles/review/keys/0", "match"},
		{"a key in upper case", func(c map[string]any) { obj(c, "cycles", "review")["keys"] = []any{"PR"} }, "/cycles/review/keys/0", "match"},
		{"a public_url of ftp://x", func(c map[string]any) { c["public_url"] = "ftp://x" }, "/public_url", "match"},
		{"a public_url with no host", func(c map[string]any) { c["public_url"] = "https://" }, "/public_url", "match"},
		{"a listen_port of 0", func(c map[string]any) { c["listen_port"] = 0 }, "/listen_port", "minimum"},
		{"a listen_port of 65536", func(c map[string]any) { c["listen_port"] = 65536 }, "/listen_port", "maximum"},
		{"a listen_port as a string", func(c map[string]any) { c["listen_port"] = "49210" }, "/listen_port", "want integer"},
		{"no profile at all", func(c map[string]any) { c["profiles"] = map[string]any{} }, "/profiles", "minProperties"},
		{"a profile that lists a number", func(c map[string]any) { obj(c, "profiles", "normal")["daily"] = []any{1} }, "/profiles/normal/daily/0", "want string"},
		{"a task title that is empty", func(c map[string]any) { obj(c, "tasks", "plan-day")["title"] = "" }, "/tasks/plan-day/title", "minLength"},
		{"an empty task id", func(c map[string]any) { obj(c, "tasks")[""] = obj(c, "tasks", "plan-day") }, "/tasks", "additional properties"},
		{"an unknown task field", func(c map[string]any) { obj(c, "tasks", "plan-day")["priority"] = 1 }, "/tasks/plan-day", "priority"},
		{"an unknown top-level key", func(c map[string]any) { c["theme"] = "dark" }, "", "theme"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.Validate(mutated(t, c.edit))
			if err == nil {
				t.Fatal("Validate succeeded")
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
		})
	}
}

func TestConfigSchemaLeavesTheSemanticChecksToTheLoader(t *testing.T) {
	s := configSchema(t)
	// Each of these is a rule that needs more than one value, or the zone
	// database; the loader applies them after the schema.
	cases := map[string]func(c map[string]any){
		"a profile naming an unknown task":     func(c map[string]any) { obj(c, "profiles", "normal")["daily"] = []any{"nope"} },
		"a task listed under another cadence":  func(c map[string]any) { obj(c, "profiles", "normal")["weekly"] = []any{"plan-day"} },
		"a daily due rule with a weekday":      func(c map[string]any) { obj(c, "tasks", "plan-day", "due")["weekday"] = "thu" },
		"a weekly due rule without a weekday":  func(c map[string]any) { delete(obj(c, "tasks", "weekly-update", "due"), "weekday") },
		"a tz that is not a zone":              func(c map[string]any) { obj(c, "tasks", "plan-day", "due")["tz"] = "ET" },
		"defaults.profile that is not defined": func(c map[string]any) { obj(c, "defaults")["profile"] = "undefined" },
		"the reserved key cycle_type":          func(c map[string]any) { obj(c, "cycles", "review")["keys"] = []any{"cycle_type"} },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Validate(mutated(t, edit)); err != nil {
				t.Errorf("the schema refused it, but the semantic checks own this rule: %v", err)
			}
		})
	}
}

// TestConfigFreeTextInventory pins that the configuration holds no free text
// (every string in it is an identifier, a name, a title or a setting, never
// text the operator types at run time) and lists every string property, so a
// new one is a deliberate choice.
func TestConfigFreeTextInventory(t *testing.T) {
	s := configSchema(t)
	if got := schemacheck.FreeTextFields(s, ""); len(got) != 0 {
		t.Errorf("the configuration schema annotates free text at %v, want none", got)
	}
	notFree := []string{
		"defaults.alert.sound", "defaults.alert.reminder_sound", "defaults.profile",
		"public_url", "group_order[]",
		"profiles.*.daily[]", "profiles.*.weekly[]", "profiles.*.sprint[]", "profiles.*.cycles[]",
		"tasks.*.title", "tasks.*.cadence", "tasks.*.group", "tasks.*.link",
		"tasks.*.due.at", "tasks.*.due.tz", "tasks.*.due.weekday",
		"$schema", // an editor's hint at the schema; the loader ignores it
		"cycles.*.title", "cycles.*.keys[]", "cycles.*.alert.sound", "cycles.*.alert.reminder_sound",
	}
	sameSet(t, "string properties of the configuration", schemacheck.StringFields(s, ""), notFree)
}

// openObjectAllowList names the object schemas that are deliberately not
// closed, each with the reason. It is empty of anything else: a new object
// schema closes with additionalProperties: false or is added here on purpose.
var openObjectAllowList = map[string]map[string]string{
	"event.schema.json": {
		"/properties/data": "the envelope's data is closed by the per-type definition the type discriminator selects",
	},
}

// TestEveryObjectSchemaIsClosed walks both embedded schemas and requires
// additionalProperties: false of every object definition, so an unknown key is
// refused wherever it appears. A conditional or combining fragment (an if,
// then, else, not, allOf, anyOf or oneOf entry) is not a definition and is
// skipped, but the properties and items inside it are checked.
func TestEveryObjectSchemaIsClosed(t *testing.T) {
	for name, raw := range map[string][]byte{"event.schema.json": schemas.Event(), "config.schema.json": schemas.Config()} {
		t.Run(name, func(t *testing.T) {
			var doc any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			var walk func(node any, pointer string, fragment bool)
			walk = func(node any, pointer string, fragment bool) {
				m, ok := node.(map[string]any)
				if !ok {
					return
				}
				if !fragment && (m["type"] == "object" || m["properties"] != nil) {
					seen[pointer] = true
					if _, allowed := openObjectAllowList[name][pointer]; !allowed && m["additionalProperties"] != false {
						t.Errorf("the object schema at %q does not set additionalProperties: false", pointer)
					}
				}
				for _, kw := range []string{"properties", "patternProperties", "$defs"} {
					subs, _ := m[kw].(map[string]any)
					for key, sub := range subs {
						walk(sub, pointer+"/"+kw+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(key), false)
					}
				}
				walk(m["items"], pointer+"/items", false)
				walk(m["additionalProperties"], pointer+"/additionalProperties", false)
				for _, kw := range []string{"allOf", "anyOf", "oneOf"} {
					subs, _ := m[kw].([]any)
					for i, sub := range subs {
						walk(sub, pointer+"/"+kw+"/"+strconv.Itoa(i), true)
					}
				}
				for _, kw := range []string{"if", "then", "else", "not"} {
					walk(m[kw], pointer+"/"+kw, true)
				}
			}
			walk(doc, "", false)
			if len(seen) < 2 {
				t.Fatalf("found only %d object schemas in %s: the walk is not reaching them", len(seen), name)
			}
			for pointer := range openObjectAllowList[name] {
				if !seen[pointer] {
					t.Errorf("the allow-list names %q, which is not an object schema of %s", pointer, name)
				}
			}
		})
	}
}

// weirdNames are property names that the text of a validator's message cannot
// carry back faithfully: quotes, a backslash, a line break, the characters a
// JSON pointer escapes, and non-ASCII.
var weirdNames = []string{"it's", `a\b`, "line\nbreak", `a"b`, "a/b", "a~b", "café", "a', 'b", "", " "}

// A caller that needs the names of the properties at fault reads them from the
// violation, not from its message, so every name comes back exactly as the
// schema or the document spells it.
func TestViolationsCarryTheNamesOfMissingAndUnexpectedProperties(t *testing.T) {
	required, err := json.Marshal(weirdNames)
	if err != nil {
		t.Fatal(err)
	}
	s, err := schemacheck.Compile("names.schema.json", []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": `+string(required)+`,
  "additionalProperties": false,
  "properties": {"known": {"type": "string"}}
}`))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("missing", func(t *testing.T) {
		got := violations(t, s.Validate([]byte(`{}`)))
		if len(got) != 1 {
			t.Fatalf("%d violations, want one: %v", len(got), got)
		}
		want := slices.Clone(weirdNames)
		have := slices.Clone(got[0].Missing)
		sort.Strings(want)
		sort.Strings(have)
		if !slices.Equal(have, want) || len(got[0].Unexpected) != 0 || got[0].Pointer != "" {
			t.Errorf("violation = %+v, want Missing %q at the root and nothing unexpected", got[0], want)
		}
	})
	t.Run("unexpected", func(t *testing.T) {
		doc := map[string]any{"known": "x"}
		for _, n := range weirdNames {
			doc[n] = 1
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var unexpected []string
		for _, v := range violations(t, s.Validate(raw)) {
			if len(v.Missing) != 0 {
				t.Errorf("violation %+v lists missing names, but every required name is present", v)
			}
			unexpected = append(unexpected, v.Unexpected...)
		}
		want := slices.Clone(weirdNames)
		sort.Strings(want)
		sort.Strings(unexpected)
		if !slices.Equal(unexpected, want) {
			t.Errorf("unexpected names = %q, want %q", unexpected, want)
		}
	})
	t.Run("no names for any other violation", func(t *testing.T) {
		found := false
		for _, v := range violations(t, s.Validate([]byte(`{"known": 1}`))) {
			if v.Pointer != "/known" {
				continue
			}
			found = true
			if len(v.Missing) != 0 || len(v.Unexpected) != 0 {
				t.Errorf("a type violation carries names: %+v", v)
			}
		}
		if !found {
			t.Error("no violation at /known")
		}
	})
}

// INV-CONF-14: the checked-in schema lets an editor validate a configuration,
// and an editor's hint is a $schema key, which the schema allows as a string.
func TestConfigSchemaAllowsADollarSchemaString(t *testing.T) {
	s := configSchema(t)
	if err := s.Validate(mutated(t, func(c map[string]any) { c["$schema"] = "./config.schema.json" })); err != nil {
		t.Errorf("a $schema string was refused: %v", err)
	}
	for name, v := range map[string]any{"a number": 3, "null": nil, "an object": map[string]any{}, "a list": []any{"x"}, "a boolean": true} {
		t.Run(name, func(t *testing.T) {
			err := s.Validate(mutated(t, func(c map[string]any) { c["$schema"] = v }))
			found := false
			for _, viol := range violations(t, err) {
				found = found || viol.Pointer == "/$schema"
			}
			if !found {
				t.Errorf("a non-string $schema (%s) was not refused at /$schema: %v", name, err)
			}
		})
	}
}
