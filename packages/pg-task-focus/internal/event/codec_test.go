package event_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

func TestValidText(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want error
	}{
		{"no strings", nil, nil},
		{"empty string", []string{""}, nil},
		{"ascii", []string{"hello"}, nil},
		{"multi-byte text", []string{"café 日本語 \U0001f600"}, nil},
		{"newline, tab and U+2028", []string{"a\nb\tc d"}, nil},
		{"the byte 0xff", []string{"ok", "bad\xff"}, event.ErrInvalidUTF8},
		{"a truncated multi-byte sequence", []string{"\xe6\x97"}, event.ErrInvalidUTF8},
		{"an encoded surrogate", []string{"\xed\xa0\x80"}, event.ErrInvalidUTF8},
		{"exactly the event limit", []string{strings.Repeat("a", event.MaxEventBytes)}, nil},
		{"one byte over the event limit", []string{strings.Repeat("a", event.MaxEventBytes+1)}, event.ErrTooLarge},
		{"300 KiB", []string{strings.Repeat("a", 300*1024)}, event.ErrTooLarge},
		{"several strings that together are too long", []string{strings.Repeat("a", event.MaxEventBytes/2+1), strings.Repeat("b", event.MaxEventBytes/2+1)}, event.ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := event.ValidText(c.in...)
			if !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
				t.Errorf("ValidText = %v, want %v", err, c.want)
			}
		})
	}
}

func TestValidReason(t *testing.T) {
	for _, blank := range []string{
		"", "  ", "\t\n", " \t \n ", " ", "　", "  　", " ", "\r\n",
	} {
		if got, err := event.ValidReason(blank); err == nil {
			t.Errorf("ValidReason(%q) = %q, want an error", blank, got)
		}
	}
	for in, want := range map[string]string{
		" late ":              "late",
		"\tlate\n":            "late",
		" late　":              "late",
		"two  words inside":   "two  words inside",
		"no surrounding trim": "no surrounding trim",
	} {
		got, err := event.ValidReason(in)
		if err != nil || got != want {
			t.Errorf("ValidReason(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := event.ValidReason("bad\xff"); !errors.Is(err, event.ErrInvalidUTF8) {
		t.Errorf("ValidReason(0xff) = %v, want ErrInvalidUTF8", err)
	}
	if _, err := event.ValidReason(strings.Repeat("a", event.MaxEventBytes+1)); !errors.Is(err, event.ErrTooLarge) {
		t.Errorf("ValidReason(too long) = %v, want ErrTooLarge", err)
	}
}

// fixtures reads every valid line under testdata/events, by file name
// without its extension.
func fixtures(t testing.TB) map[string][]byte {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "events", "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(raw, []byte("\n")) != 1 || !bytes.HasSuffix(raw, []byte("\n")) {
			t.Fatalf("%s is not exactly one newline-terminated line", f)
		}
		out[strings.TrimSuffix(filepath.Base(f), ".jsonl")] = bytes.TrimSuffix(raw, []byte("\n"))
	}
	return out
}

func TestEveryEventTypeHasAFixture(t *testing.T) {
	have := map[event.Type]bool{}
	for name := range fixtures(t) {
		typ := event.Type(name)
		if i := strings.Index(name, ".retracted"); i >= 0 {
			typ = event.Type(name[:i+len(".retracted")])
		} else if parts := strings.Split(name, "."); len(parts) > 2 {
			typ = event.Type(strings.Join(parts[:2], "."))
		}
		have[typ] = true
	}
	for _, typ := range event.Types() {
		if !have[typ] {
			t.Errorf("no fixture for %s", typ)
		}
	}
	for _, form := range []string{"event.retracted.target", "event.retracted.target_batch"} {
		if _, ok := fixtures(t)[form]; !ok {
			t.Errorf("no fixture for %s", form)
		}
	}
}

func TestEncodeDecodeEveryType(t *testing.T) {
	for name, line := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			e, err := event.Decode(line)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !strings.HasPrefix(name, string(e.Type)) {
				t.Errorf("decoded type %q does not match the fixture name %q", e.Type, name)
			}
			if e.Payload == nil || e.Payload.EventType() != e.Type {
				t.Errorf("payload %T does not match type %q", e.Payload, e.Type)
			}
			if e.V != event.SchemaVersion || e.Line != 0 {
				t.Errorf("V = %d, Line = %d; want %d and 0", e.V, e.Line, event.SchemaVersion)
			}
			if len(e.Data) == 0 || !bytes.Contains(line, e.Data) {
				t.Errorf("Data = %s is not the data of the line", e.Data)
			}
			again, err := event.Encode(e)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(again, line) {
				t.Errorf("encode(decode(line)) differs\n got: %s\nwant: %s", again, line)
			}
			if bytes.ContainsAny(again, "\n\r") || strings.ContainsRune(string(again), ' ') {
				t.Errorf("the encoding holds a raw line break: %q", again)
			}
		})
	}
}

func TestEncodeKeepsSeparatorsOutOfTheLine(t *testing.T) {
	note := "a\nb\tc d e\r\nf"
	e := newEvent(event.CycleAnnotated{CycleID: "cyc", Note: note})
	line, err := event.Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsAny(line, "\n\r\t") || strings.ContainsAny(string(line), "  ") {
		t.Errorf("raw separator in %q", line)
	}
	back, err := event.Decode(line)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Payload.(event.CycleAnnotated).Note; got != note {
		t.Errorf("note = %q, want %q", got, note)
	}
}

// newEvent wraps a payload in a valid envelope.
func newEvent(p event.Payload) event.Event {
	return event.Event{
		Envelope: event.Envelope{
			V:           event.SchemaVersion,
			ID:          "01J9Z3K8M2E000000000000001",
			At:          event.At(time.Date(2026, 10, 7, 13, 30, 4, 120_000_000, time.UTC)),
			EffectiveAt: event.At(time.Date(2026, 10, 7, 13, 28, 0, 0, time.UTC)),
			Type:        p.EventType(),
		},
		Payload: p,
	}
}

func TestEncodeRejectsOversizeAndInvalidUTF8(t *testing.T) {
	t.Run("a 300 KiB note is too large", func(t *testing.T) {
		line, err := event.Encode(newEvent(event.CycleAnnotated{CycleID: "c", Note: strings.Repeat("a", 300*1024)}))
		if !errors.Is(err, event.ErrTooLarge) || line != nil {
			t.Errorf("Encode = %d bytes, %v; want no bytes and ErrTooLarge", len(line), err)
		}
	})
	t.Run("the limit applies to the encoded line", func(t *testing.T) {
		// Each newline escapes to two bytes, so the text is under the limit
		// and the line is over it.
		note := strings.Repeat("\n", event.MaxEventBytes/2)
		if err := event.ValidText(note); err != nil {
			t.Fatalf("ValidText: %v", err)
		}
		if _, err := event.Encode(newEvent(event.CycleAnnotated{CycleID: "c", Note: note})); !errors.Is(err, event.ErrTooLarge) {
			t.Errorf("Encode = %v, want ErrTooLarge", err)
		}
	})
	t.Run("a line of exactly MaxEventBytes passes and one byte more fails", func(t *testing.T) {
		base, err := event.Encode(newEvent(event.CycleAnnotated{CycleID: "c", Note: "a"}))
		if err != nil {
			t.Fatal(err)
		}
		n := event.MaxEventBytes - len(base) + 1
		line, err := event.Encode(newEvent(event.CycleAnnotated{CycleID: "c", Note: strings.Repeat("a", n)}))
		if err != nil || len(line) != event.MaxEventBytes {
			t.Fatalf("Encode at the limit = %d bytes, %v; want %d bytes", len(line), err, event.MaxEventBytes)
		}
		if _, err := event.Decode(line); err != nil {
			t.Errorf("Decode of a line at the limit: %v", err)
		}
		if _, err := event.Encode(newEvent(event.CycleAnnotated{CycleID: "c", Note: strings.Repeat("a", n+1)})); !errors.Is(err, event.ErrTooLarge) {
			t.Errorf("Encode one byte over = %v, want ErrTooLarge", err)
		}
	})
	t.Run("many small pairs that together are too long", func(t *testing.T) {
		kv := make([]event.KV, 0, 5000)
		for i := 0; i < 5000; i++ {
			kv = append(kv, event.KV{Key: "key", Value: strings.Repeat("v", 60)})
		}
		if _, err := event.Encode(newEvent(event.CycleAnnotated{CycleID: "c", KV: kv})); !errors.Is(err, event.ErrTooLarge) {
			t.Errorf("Encode = %v, want ErrTooLarge", err)
		}
	})
	t.Run("ordinary multi-byte text passes", func(t *testing.T) {
		note := "café 日本語 \U0001f600"
		line, err := event.Encode(newEvent(event.CycleAnnotated{CycleID: "c", Note: note}))
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if !utf8.Valid(line) || !bytes.Contains(line, []byte(note)) {
			t.Errorf("the multi-byte text was altered in %q", line)
		}
	})

	// The byte 0xff would be silently rewritten to U+FFFD by json.Marshal, so
	// every string of every payload is scanned itself.
	bad := "bad\xff"
	invalid := map[string]event.Payload{
		"cycle note":        event.CycleAnnotated{CycleID: "c", Note: bad},
		"kv value":          event.CycleAnnotated{CycleID: "c", KV: []event.KV{{Key: "k", Value: bad}}},
		"kv key":            event.CycleAnnotated{CycleID: "c", KV: []event.KV{{Key: bad, Value: "v"}}},
		"skip reason":       event.TaskSkipped{TaskID: "t", Reason: bad},
		"period label":      event.PeriodChanged{Kind: "day", Start: civil.Date{Year: 2026, Month: 10, Day: 7}, TZ: "America/New_York", Label: bad},
		"cycle title":       event.CycleStarted{CycleID: "c", Type: "deep", Title: bad, PlannedMinutes: 5},
		"correction reason": event.EventCorrected{Target: "01J9Z3K8M2E000000000000002", Fields: map[string]json.RawMessage{"minutes": json.RawMessage("5")}, Reason: bad},
		"correction field":  event.EventCorrected{Target: "01J9Z3K8M2E000000000000002", Fields: map[string]json.RawMessage{"note": json.RawMessage("\"" + bad + "\"")}},
		"retraction reason": event.EventRetracted{Target: "01J9Z3K8M2E000000000000002", Reason: bad},
		"task title":        event.TaskMaterialized{TaskID: "t", Definition: "d", Cadence: "daily", Period: civil.Date{Year: 2026, Month: 10, Day: 7}, Title: bad},
		"task id":           event.TaskCompleted{TaskID: event.TaskID(bad)},
	}
	for name, p := range invalid {
		t.Run("invalid UTF-8 in "+name, func(t *testing.T) {
			line, err := event.Encode(newEvent(p))
			if !errors.Is(err, event.ErrInvalidUTF8) || line != nil {
				t.Errorf("Encode = %q, %v; want no bytes and ErrInvalidUTF8", line, err)
			}
		})
	}
}

func TestEncodeRefusesWhatDecodeWouldRefuse(t *testing.T) {
	good := newEvent(event.TaskSkipped{TaskID: "day:2026-10-07:post-plan", Reason: "late"})
	if _, err := event.Encode(good); err != nil {
		t.Fatalf("Encode(good): %v", err)
	}

	blankReason := newEvent(event.TaskSkipped{TaskID: "t", Reason: "  "})
	badID := good
	badID.ID = "not-a-ulid"
	noAt := good
	noAt.At = event.Instant{}
	noEffective := good
	noEffective.EffectiveAt = event.Instant{}
	wrongType := good
	wrongType.Type = "task.completed"
	unknownType := good
	unknownType.Type = "task.exploded"
	noPayload := good
	noPayload.Payload = nil
	noPayload.Data = nil
	badHash := good
	badHash.ReqHash = "ABC"
	badVersion := good
	badVersion.V = 2

	cases := map[string]event.Event{
		"blank skip reason": blankReason,
		"bad id":            badID,
		"no at":             noAt,
		"no effective_at":   noEffective,
		"type mismatch":     wrongType,
		"unknown type":      unknownType,
		"no payload":        noPayload,
		"bad req_hash":      badHash,
		"version 2":         badVersion,
		"retraction with both": newEvent(event.EventRetracted{
			Target: "01J9Z3K8M2E000000000000002", TargetBatch: "01J9Z3K8M2B000000000000002",
		}),
		"retraction with neither": newEvent(event.EventRetracted{}),
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			if line, err := event.Encode(e); err == nil {
				t.Errorf("Encode succeeded with %s", line)
			}
		})
	}
}

func TestEncodeOfRawDataIsCanonical(t *testing.T) {
	e := newEvent(event.TaskCompleted{TaskID: "t"})
	e.Payload = nil
	e.Data = json.RawMessage(`{ "task_id" : "t" }`)
	line, err := event.Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	want, err := event.Encode(newEvent(event.TaskCompleted{TaskID: "t"}))
	if err != nil || !bytes.Equal(line, want) {
		t.Errorf("Encode(Data) = %s, %v; want %s", line, err, want)
	}
}

func TestEncodeDefaultsAnUnsetVersionAndType(t *testing.T) {
	e := newEvent(event.TaskCompleted{TaskID: "t"})
	e.V, e.Type = 0, ""
	line, err := event.Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := event.Decode(line)
	if err != nil || back.V != 1 || back.Type != "task.completed" {
		t.Errorf("Decode = V %d, type %q, %v; want version 1 and task.completed", back.V, back.Type, err)
	}
}

// fixtureLine returns the fixture as a generic object for mutation.
func fixtureObject(t *testing.T, name string) map[string]any {
	t.Helper()
	var o map[string]any
	if err := json.Unmarshal(fixtures(t)[name], &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func data(o map[string]any) map[string]any { return o["data"].(map[string]any) }

func marshal(t *testing.T, o map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeRejects(t *testing.T) {
	type mutation func(o map[string]any)
	cases := []struct {
		name    string
		fixture string
		mutate  mutation
	}{
		{"unknown envelope field", "task.completed", func(o map[string]any) { o["extra"] = 1 }},
		{"unknown data field", "task.completed", func(o map[string]any) { data(o)["carry_over"] = true }},
		{"missing type", "task.completed", func(o map[string]any) { delete(o, "type") }},
		{"unknown type", "task.completed", func(o map[string]any) { o["type"] = "task.exploded" }},
		{"missing id", "task.completed", func(o map[string]any) { delete(o, "id") }},
		{"lower-case id", "task.completed", func(o map[string]any) { o["id"] = strings.ToLower(o["id"].(string)) }},
		{"missing at", "task.completed", func(o map[string]any) { delete(o, "at") }},
		{"missing effective_at", "task.completed", func(o map[string]any) { delete(o, "effective_at") }},
		{"at with a numeric offset", "task.completed", func(o map[string]any) { o["at"] = "2026-10-07T13:30:04.120+00:00" }},
		{"effective_at with another zone", "task.completed", func(o map[string]any) { o["effective_at"] = "2026-10-07T14:28:00.000+01:00" }},
		{"effective_at without a fraction", "task.completed", func(o map[string]any) { o["effective_at"] = "2026-10-07T13:28:00Z" }},
		{"malformed req_hash", "task.completed", func(o map[string]any) { o["req_hash"] = "ABC" }},
		{"upper-case req_hash", "task.completed", func(o map[string]any) { o["req_hash"] = strings.ToUpper(o["req_hash"].(string)) }},
		{"missing data", "task.completed", func(o map[string]any) { delete(o, "data") }},
		{"null data", "task.completed", func(o map[string]any) { o["data"] = nil }},
		{"array data", "task.completed", func(o map[string]any) { o["data"] = []any{} }},
		{"v as a string", "task.completed", func(o map[string]any) { o["v"] = "1" }},
		{"missing v", "task.completed", func(o map[string]any) { delete(o, "v") }},
		{"empty task_id", "task.completed", func(o map[string]any) { data(o)["task_id"] = "" }},
		{"missing task_id", "task.withdrawn", func(o map[string]any) { delete(data(o), "task_id") }},
		{"malformed batch", "task.withdrawn", func(o map[string]any) { data(o)["batch"] = "b1" }},
		{"empty cycle_id", "cycle.stopped", func(o map[string]any) { data(o)["cycle_id"] = "" }},
		{"empty profile", "profile.changed", func(o map[string]any) { data(o)["profile"] = "" }},

		{"retraction with both target and target_batch", "event.retracted.target", func(o map[string]any) { data(o)["target_batch"] = "01J9Z3K8M2B000000000000001" }},
		{"retraction with neither", "event.retracted.target", func(o map[string]any) { delete(data(o), "target") }},
		{"retraction carrying a batch", "event.retracted.target", func(o map[string]any) { data(o)["batch"] = "01J9Z3K8M2B000000000000001" }},
		{"batch retraction carrying a batch", "event.retracted.target_batch", func(o map[string]any) { data(o)["batch"] = "01J9Z3K8M2B000000000000001" }},
		{"retraction of a target that is not an id", "event.retracted.target", func(o map[string]any) { data(o)["target"] = "x" }},
		{"retraction of a batch that is not an id", "event.retracted.target_batch", func(o map[string]any) { data(o)["target_batch"] = "x" }},

		{"skip with an empty reason", "task.skipped", func(o map[string]any) { data(o)["reason"] = "" }},
		{"skip without a reason", "task.skipped", func(o map[string]any) { delete(data(o), "reason") }},
		{"skip with a blank reason", "task.skipped", func(o map[string]any) { data(o)["reason"] = "  " }},
		{"skip with a no-break space reason", "task.skipped", func(o map[string]any) { data(o)["reason"] = " " }},
		{"skip with an ideographic space reason", "task.skipped", func(o map[string]any) { data(o)["reason"] = "　" }},
		{"skip with a tab and newline reason", "task.skipped", func(o map[string]any) { data(o)["reason"] = "\t\n" }},

		{"day period with an end", "period.changed", func(o map[string]any) { data(o)["end"] = "2026-10-07" }},
		{"week period without an end", "period.changed.week", func(o map[string]any) { delete(data(o), "end") }},
		{"unknown period kind", "period.changed", func(o map[string]any) { data(o)["kind"] = "month" }},
		{"period with an abbreviation as the zone", "period.changed", func(o map[string]any) { data(o)["tz"] = "ET" }},
		{"period with an empty zone", "period.changed", func(o map[string]any) { data(o)["tz"] = "" }},
		{"period with no zone", "period.changed", func(o map[string]any) { delete(data(o), "tz") }},
		{"period with a date that does not exist", "period.changed", func(o map[string]any) { data(o)["start"] = "2026-02-30" }},

		{"materialized without due", "task.materialized", func(o map[string]any) { delete(data(o), "due") }},
		{"materialized without due_rule", "task.materialized", func(o map[string]any) { delete(data(o), "due_rule") }},
		{"materialized with an unknown cadence", "task.materialized", func(o map[string]any) { data(o)["cadence"] = "monthly" }},
		{"materialized daily with a weekday rule", "task.materialized", func(o map[string]any) {
			data(o)["due_rule"] = map[string]any{"at": "09:00", "tz": "America/New_York", "weekday": "thu"}
		}},
		{"materialized weekly without a weekday", "task.materialized.weekly", func(o map[string]any) { data(o)["due_rule"] = map[string]any{"at": "15:00", "tz": "America/New_York"} }},
		{"materialized with an unknown rule field", "task.materialized", func(o map[string]any) { data(o)["due_rule"].(map[string]any)["carry_over"] = true }},
		{"materialized without a period", "task.materialized", func(o map[string]any) { delete(data(o), "period") }},
		{"materialized without a definition", "task.materialized", func(o map[string]any) { data(o)["definition"] = "" }},

		{"cycle started with zero minutes", "cycle.started", func(o map[string]any) { data(o)["planned_minutes"] = 0 }},
		{"cycle started with 525601 minutes", "cycle.started", func(o map[string]any) { data(o)["planned_minutes"] = 525601 }},
		{"cycle started with fractional minutes", "cycle.started", func(o map[string]any) { data(o)["planned_minutes"] = 1.5 }},
		{"cycle started without a type", "cycle.started", func(o map[string]any) { data(o)["type"] = "" }},
		{"cycle boosted with zero minutes", "cycle.boosted", func(o map[string]any) { data(o)["minutes"] = 0 }},
		{"cycle boosted with negative minutes", "cycle.boosted", func(o map[string]any) { data(o)["minutes"] = -5 }},
		{"cycle boosted with 525601 minutes", "cycle.boosted", func(o map[string]any) { data(o)["minutes"] = 525601 }},
		{"cycle annotated with a kv entry that has no key", "cycle.annotated", func(o map[string]any) { data(o)["kv"] = []any{map[string]any{"value": "a"}} }},
		{"cycle annotated with an unknown kv field", "cycle.annotated", func(o map[string]any) {
			data(o)["kv"] = []any{map[string]any{"key": "a", "value": "b", "extra": "c"}}
		}},

		{"week period whose end is before its start", "period.changed.week", func(o map[string]any) { data(o)["end"] = "2026-10-04" }},
		{"sprint period whose end is before its start", "period.changed.week", func(o map[string]any) {
			data(o)["kind"] = "sprint"
			data(o)["end"] = "2026-10-04"
		}},

		{"correction without fields", "event.corrected", func(o map[string]any) { delete(data(o), "fields") }},
		{"correction without a target", "event.corrected", func(o map[string]any) { delete(data(o), "target") }},
		{"correction of a target that is not an id", "event.corrected", func(o map[string]any) { data(o)["target"] = "x" }},
		{"batch.committed without a batch", "batch.committed", func(o map[string]any) { delete(data(o), "batch") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := fixtureObject(t, c.fixture)
			c.mutate(o)
			line := marshal(t, o)
			if e, err := event.Decode(line); err == nil {
				t.Errorf("Decode accepted %s as %T", line, e.Payload)
			}
		})
	}

	t.Run("not JSON", func(t *testing.T) {
		for _, in := range []string{"", "   ", "garbage", "{", `{"v":1`, "[]", "null", `"v"`} {
			if _, err := event.Decode([]byte(in)); err == nil {
				t.Errorf("Decode(%q) succeeded", in)
			}
		}
	})
	t.Run("data after the object", func(t *testing.T) {
		line := fixtures(t)["task.completed"]
		for _, tail := range []string{"x", "{}", string(line)} {
			if _, err := event.Decode(append(append([]byte{}, line...), tail...)); err == nil {
				t.Errorf("Decode accepted trailing %q", tail)
			}
		}
	})
	t.Run("invalid UTF-8 inside a string", func(t *testing.T) {
		line := bytes.Replace(fixtures(t)["task.skipped"], []byte("Out sick"), []byte("Out \xff sick"), 1)
		if _, err := event.Decode(line); !errors.Is(err, event.ErrInvalidUTF8) {
			t.Errorf("Decode = %v, want ErrInvalidUTF8 (the byte must not be rewritten to U+FFFD)", err)
		}
	})
	t.Run("a line over the size limit", func(t *testing.T) {
		line := bytes.Replace(fixtures(t)["cycle.annotated"], []byte("Line one"), bytes.Repeat([]byte("a"), event.MaxEventBytes), 1)
		if _, err := event.Decode(line); !errors.Is(err, event.ErrTooLarge) {
			t.Errorf("Decode = %v, want ErrTooLarge", err)
		}
	})
}

func TestDecodeChecksTheVersionFirst(t *testing.T) {
	cases := map[string]string{
		"an otherwise valid line":     string(fixtures(t)["task.completed"]),
		"an unknown type":             `{"v":2,"id":"x","type":"task.exploded","data":{}}`,
		"fields the schema rejects":   `{"v":2,"surprise":true}`,
		"no other envelope field":     `{"v":2}`,
		"data that is not an object":  `{"v":2,"type":"task.completed","data":7}`,
		"a missing instant":           `{"v":2,"id":"01J9Z3K8M2E000000000000001","type":"task.completed","data":{"task_id":"t"}}`,
		"invalid UTF-8 in the string": "{\"v\":2,\"note\":\"\xff\"}",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "an otherwise valid line" {
				in = strings.Replace(in, `"v":1`, `"v":2`, 1)
			}
			_, err := event.Decode([]byte(in))
			var uv *event.UnknownVersionError
			if !errors.As(err, &uv) || uv.V != 2 {
				t.Fatalf("Decode(%s) = %v, want *UnknownVersionError{V: 2}", in, err)
			}
		})
	}
	for v, want := range map[string]int{"0": 0, "3": 3, "-1": -1, "1000": 1000} {
		_, err := event.Decode([]byte(`{"v":` + v + `}`))
		var uv *event.UnknownVersionError
		if !errors.As(err, &uv) || uv.V != want {
			t.Errorf("v=%s: Decode = %v, want *UnknownVersionError{V: %d}", v, err, want)
		}
	}
}

func TestDecodeKeepsTheRawDataAndAnyLongValidLine(t *testing.T) {
	note := strings.Repeat("n", 100*1024)
	line, err := event.Encode(newEvent(event.CycleAnnotated{CycleID: "c", Note: note}))
	if err != nil {
		t.Fatal(err)
	}
	e, err := event.Decode(line)
	if err != nil {
		t.Fatalf("Decode of a 100 KiB line: %v", err)
	}
	if got := e.Payload.(event.CycleAnnotated).Note; got != note {
		t.Error("the 100 KiB note did not round trip")
	}
	var data map[string]any
	if err := json.Unmarshal(e.Data, &data); err != nil || data["cycle_id"] != "c" {
		t.Errorf("Data = %.60s, %v", e.Data, err)
	}
}

func TestKVRepeatedKeysPreserved(t *testing.T) {
	want := []event.KV{{Key: "a", Value: "1"}, {Key: "b", Value: "x"}, {Key: "a", Value: "2"}}
	line, err := event.Encode(newEvent(event.CycleAnnotated{CycleID: "c", KV: want}))
	if err != nil {
		t.Fatal(err)
	}
	back, err := event.Decode(line)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Payload.(event.CycleAnnotated).KV; !reflect.DeepEqual(got, want) {
		t.Errorf("kv = %v, want %v", got, want)
	}
}

func TestCorrectionFieldsKeepEveryValueAndSortTheKeys(t *testing.T) {
	fields := map[string]json.RawMessage{
		"minutes":      json.RawMessage(`15`),
		"effective_at": json.RawMessage(`"2026-10-07T13:20:00.000Z"`),
		"kv":           json.RawMessage(`[{"key":"a","value":"1"}]`),
	}
	line, err := event.Encode(newEvent(event.EventCorrected{Target: "01J9Z3K8M2E000000000000002", Fields: fields}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(line, []byte(`"fields":{"effective_at":"2026-10-07T13:20:00.000Z","kv":[{"key":"a","value":"1"}],"minutes":15}`)) {
		t.Errorf("fields are not sorted by key in %s", line)
	}
	back, err := event.Decode(line)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range fields {
		if got := back.Payload.(event.EventCorrected).Fields[k]; string(got) != string(v) {
			t.Errorf("fields[%q] = %s, want %s", k, got, v)
		}
	}
}

func TestEncodeDecodeProperty(t *testing.T) {
	genID := rapid.Custom(func(t *rapid.T) event.ID {
		first := rapid.SampledFrom([]byte("01234567")).Draw(t, "first")
		rest := rapid.SliceOfN(rapid.SampledFrom([]byte(crockford)), 25, 25).Draw(t, "rest")
		return event.ID(string(first) + string(rest))
	})
	genInstant := rapid.Custom(func(t *rapid.T) event.Instant {
		return event.At(time.UnixMilli(rapid.Int64Range(0, 253402300799999).Draw(t, "ms")))
	})
	genReason := rapid.String().Filter(func(s string) bool {
		_, err := event.ValidReason(s)
		return err == nil
	})
	genPayload := rapid.Custom(func(t *rapid.T) event.Payload {
		switch rapid.IntRange(0, 3).Draw(t, "payload kind") {
		case 0:
			kv := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) event.KV {
				return event.KV{Key: rapid.StringN(1, -1, -1).Draw(t, "key"), Value: rapid.String().Draw(t, "value")}
			}), 0, 6).Draw(t, "kv")
			if len(kv) == 0 {
				kv = nil
			}
			return event.CycleAnnotated{CycleID: event.CycleID(rapid.StringN(1, 30, -1).Draw(t, "cycle")), Note: rapid.String().Draw(t, "note"), KV: kv}
		case 1:
			return event.TaskSkipped{TaskID: event.TaskID(rapid.StringN(1, 30, -1).Draw(t, "task")), Reason: genReason.Draw(t, "reason"), Batch: rapid.OneOf(rapid.Just(event.ID("")), genID).Draw(t, "batch")}
		case 2:
			return event.EventRetracted{TargetBatch: genID.Draw(t, "target batch"), Reason: rapid.String().Draw(t, "reason")}
		default:
			return event.PeriodChanged{Kind: "day", Start: civil.Date{Year: 2026, Month: 10, Day: rapid.IntRange(1, 28).Draw(t, "day")}, TZ: "Pacific/Apia", Label: rapid.String().Draw(t, "label"), Batch: genID.Draw(t, "batch")}
		}
	})

	rapid.Check(t, func(t *rapid.T) {
		p := genPayload.Draw(t, "payload")
		e := newEvent(p)
		e.ID = genID.Draw(t, "id")
		e.At = genInstant.Draw(t, "at")
		e.EffectiveAt = genInstant.Draw(t, "effective_at")

		line, err := event.Encode(e)
		if err != nil {
			t.Fatalf("Encode(%+v): %v", p, err)
		}
		if bytes.ContainsAny(line, "\n\r") || !utf8.Valid(line) {
			t.Fatalf("not a single valid UTF-8 line: %q", line)
		}
		back, err := event.Decode(line)
		if err != nil {
			t.Fatalf("Decode(%s): %v", line, err)
		}
		if back.ID != e.ID || !back.At.Time().Equal(e.At.Time()) || !back.EffectiveAt.Time().Equal(e.EffectiveAt.Time()) {
			t.Fatalf("envelope changed: %+v vs %+v", back.Envelope, e.Envelope)
		}
		if _, isPeriod := p.(event.PeriodChanged); !isPeriod && !reflect.DeepEqual(back.Payload, p) {
			t.Fatalf("payload changed:\n got %#v\nwant %#v", back.Payload, p)
		}
		again, err := event.Encode(back)
		if err != nil || !bytes.Equal(again, line) {
			t.Fatalf("encode(decode(line)) = %s, %v; want %s", again, err, line)
		}
	})
}

// TestPeriodEndMayEqualStartButNotPrecedeIt pins the one boundary the docs
// leave open: a week or sprint that ends the day it starts is allowed, one that
// ends before it starts is not.
func TestPeriodEndMayEqualStartButNotPrecedeIt(t *testing.T) {
	for _, kind := range []string{"week", "sprint"} {
		o := fixtureObject(t, "period.changed.week")
		data(o)["kind"] = kind
		data(o)["end"] = data(o)["start"]
		if _, err := event.Decode(marshal(t, o)); err != nil {
			t.Errorf("%s ending the day it starts: Decode = %v, want it accepted", kind, err)
		}
	}
	d := func(day int) *civil.Date { return &civil.Date{Year: 2026, Month: 10, Day: day} }
	bad := newEvent(event.PeriodChanged{Kind: "week", Start: *d(11), End: d(5), TZ: "America/New_York", Batch: "01J9Z3K8M2B000000000000001"})
	if line, err := event.Encode(bad); err == nil {
		t.Errorf("Encode wrote %s, but Decode would refuse a week that ends before it starts", line)
	}
}

// correctionLine is the event.corrected fixture with fields replaced by the
// given JSON object.
func correctionLine(t *testing.T, fields string) []byte {
	t.Helper()
	o := fixtureObject(t, "event.corrected")
	data(o)["fields"] = json.RawMessage(fields)
	return marshal(t, o)
}

func TestDecodeChecksTheReplacementValuesOfACorrection(t *testing.T) {
	cases := []struct{ name, fields, path string }{
		{"a blank reason", `{"reason":"  "}`, "fields.reason"},
		{"a no-break-space reason", `{"reason":"\u00a0"}`, "fields.reason"},
		{"an ideographic-space reason", `{"reason":"\u3000\t"}`, "fields.reason"},
		{"an instant that does not exist", `{"effective_at":"2026-13-45T25:61:00.000Z"}`, "fields.effective_at"},
		{"a due instant that does not exist", `{"due":"2026-02-30T09:00:00.000Z"}`, "fields.due"},
		{"a start that does not exist", `{"start":"2026-02-30"}`, "fields.start"},
		{"an end that does not exist", `{"end":"2026-02-30"}`, "fields.end"},
		{"a period that does not exist", `{"period":"2026-00-10"}`, "fields.period"},
		{"a zone that is not IANA", `{"tz":"ET"}`, "fields.tz"},
		{"a due rule with an unknown zone", `{"due_rule":{"at":"09:00","tz":"Not/AZone"}}`, "fields.due_rule"},
		{"a due rule with both weekday and day", `{"due_rule":{"at":"09:00","tz":"America/New_York","weekday":"thu","day":2}}`, "fields.due_rule"},
		{"a daily due rule for a weekly cadence", `{"cadence":"weekly","due_rule":{"at":"09:00","tz":"America/New_York"}}`, "fields.due_rule"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := correctionLine(t, c.fields)
			_, err := event.Decode(line)
			if err == nil {
				t.Fatalf("Decode accepted %s", line)
			}
			if !strings.Contains(err.Error(), c.path) {
				t.Errorf("Decode = %v, want it to name %q", err, c.path)
			}
		})
	}

	t.Run("a blank reason is refused by Encode too", func(t *testing.T) {
		e := newEvent(event.EventCorrected{Target: "01J9Z3K8M2E000000000000002", Fields: map[string]json.RawMessage{"reason": json.RawMessage(`"  "`)}})
		if line, err := event.Encode(e); err == nil {
			t.Errorf("Encode wrote %s", line)
		}
	})
	t.Run("a due rule is checked against the cadence being replaced", func(t *testing.T) {
		ok := `{"cadence":"weekly","due_rule":{"at":"09:00","tz":"America/New_York","weekday":"thu"}}`
		if _, err := event.Decode(correctionLine(t, ok)); err != nil {
			t.Errorf("Decode of a weekly rule for a weekly cadence: %v", err)
		}
	})
	t.Run("a correct replacement of every kind is accepted", func(t *testing.T) {
		line := fixtures(t)["event.corrected.replacements"]
		e, err := event.Decode(line)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		again, err := event.Encode(e)
		if err != nil || !bytes.Equal(again, line) {
			t.Errorf("Encode(Decode(line)) = %s, %v; want the line back", again, err)
		}
	})
	t.Run("an empty fields object is left to the rules layer", func(t *testing.T) {
		if _, err := event.Decode(correctionLine(t, `{}`)); err != nil {
			t.Errorf("Decode of fields {}: %v", err)
		}
	})
	t.Run("an empty note, label, group and link are accepted", func(t *testing.T) {
		if _, err := event.Decode(correctionLine(t, `{"note":"","label":"","group":"","link":""}`)); err != nil {
			t.Errorf("Decode: %v", err)
		}
	})
}

func hasRawSeparator(b []byte) bool {
	return bytes.Contains(b, []byte("\u2028")) || bytes.Contains(b, []byte("\u2029"))
}

func TestEncodeNeverWritesARawLineSeparator(t *testing.T) {
	const ls, ps = "\u2028", "\u2029"
	notes := map[string]string{
		"U+2028": "a" + ls + "b", "U+2029": "a" + ps + "b", "both": ls + ps, "only U+2028": ls,
	}
	for name, note := range notes {
		t.Run("cycle.annotated note with "+name, func(t *testing.T) {
			want := newEvent(event.CycleAnnotated{CycleID: "c", Note: note, KV: []event.KV{{Key: "k", Value: note}}})
			line, err := event.Encode(want)
			if err != nil {
				t.Fatal(err)
			}
			if hasRawSeparator(line) {
				t.Errorf("raw separator in %q", line)
			}
			back, err := event.Decode(line)
			if err != nil {
				t.Fatal(err)
			}
			if got := back.Payload.(event.CycleAnnotated); got.Note != note || got.KV[0].Value != note {
				t.Errorf("text changed: %+v", got)
			}
		})
		t.Run("correction fields note with "+name, func(t *testing.T) {
			// A raw separator is valid JSON inside a string, so a line that a
			// person edited, or a RawMessage a caller built, can carry one.
			raw := json.RawMessage(`"` + note + `"`)
			e := newEvent(event.EventCorrected{Target: "01J9Z3K8M2E000000000000002", Fields: map[string]json.RawMessage{"note": raw}})
			line, err := event.Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			if hasRawSeparator(line) {
				t.Errorf("raw separator in %q", line)
			}
			back, err := event.Decode(line)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			if err := json.Unmarshal(back.Payload.(event.EventCorrected).Fields["note"], &got); err != nil || got != note {
				t.Errorf("note = %q, %v; want %q", got, err, note)
			}
			again, err := event.Encode(back)
			if err != nil || !bytes.Equal(again, line) {
				t.Errorf("encode(decode(line)) = %s, %v; want the line back", again, err)
			}
		})
		t.Run("a stored line with a raw separator in correction fields, "+name, func(t *testing.T) {
			line := correctionLine(t, `{"note":"`+note+`"}`)
			e, err := event.Decode(line)
			if err != nil {
				t.Fatal(err)
			}
			out, err := event.Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			if hasRawSeparator(out) {
				t.Errorf("raw separator in %q", out)
			}
		})
	}
	t.Run("an escaped separator in correction fields stays escaped", func(t *testing.T) {
		line := correctionLine(t, `{"note":"a\u2028b"}`)
		e, err := event.Decode(line)
		if err != nil {
			t.Fatal(err)
		}
		out, err := event.Encode(e)
		if err != nil || hasRawSeparator(out) || !bytes.Contains(out, []byte(`a\u2028b`)) {
			t.Errorf("Encode = %q, %v", out, err)
		}
	})

	t.Run("property", func(t *testing.T) {
		text := rapid.StringOfN(rapid.SampledFrom([]rune{'a', 'é', '\u2028', '\u2029', '\n', ' '}), 0, 12, -1)
		rapid.Check(t, func(t *rapid.T) {
			note := text.Draw(t, "note")
			raw := json.RawMessage(`"` + strings.NewReplacer("\n", `\n`).Replace(note) + `"`)
			for name, p := range map[string]event.Payload{
				"annotated":  event.CycleAnnotated{CycleID: "c", Note: note},
				"correction": event.EventCorrected{Target: "01J9Z3K8M2E000000000000002", Fields: map[string]json.RawMessage{"note": raw}},
			} {
				line, err := event.Encode(newEvent(p))
				if err != nil {
					t.Fatalf("%s: Encode: %v", name, err)
				}
				if hasRawSeparator(line) || bytes.ContainsAny(line, "\n\r") {
					t.Fatalf("%s: raw separator or line break in %q", name, line)
				}
				if _, err := event.Decode(line); err != nil {
					t.Fatalf("%s: Decode(%s): %v", name, line, err)
				}
			}
		})
	})
}

func TestDecodeRefusesALoneSurrogateEscape(t *testing.T) {
	annotated := func(note string) []byte {
		return bytes.Replace(fixtures(t)["cycle.annotated"], []byte(`"note":"Line one\nline two\ttabbed, café, <b>&</b>, separator:\u2028."`), []byte(`"note":"`+note+`"`), 1)
	}
	lone := map[string]string{
		"a high surrogate":                    `a\ud800b`,
		"the last high surrogate":             `\udbff`,
		"a low surrogate":                     `x\udc00`,
		"the last low surrogate":              `\udfffy`,
		"a high surrogate at the end":         `abc\ud83d`,
		"two high surrogates":                 `\ud800\ud800`,
		"a low then a high surrogate":         `\ude00\ud83d`,
		"a high surrogate and a plain escape": `\ud83d\u0041`,
		"an upper-case hex escape":            `\uD800`,
	}
	for name, note := range lone {
		t.Run(name, func(t *testing.T) {
			line := annotated(note)
			if !bytes.Contains(line, []byte(note)) {
				t.Fatalf("the test line does not carry %s", note)
			}
			if _, err := event.Decode(line); !errors.Is(err, event.ErrInvalidUTF8) {
				t.Errorf("Decode = %v, want ErrInvalidUTF8", err)
			}
		})
	}

	t.Run("in every kind of string", func(t *testing.T) {
		const esc = `\ud800`
		o := map[string]string{
			"kv value":              strings.Replace(string(fixtures(t)["cycle.annotated"]), `"value":"EX-1"`, `"value":"`+esc+`"`, 1),
			"kv key":                strings.Replace(string(fixtures(t)["cycle.annotated"]), `"key":"ticket"`, `"key":"`+esc+`"`, 1),
			"skip reason":           strings.Replace(string(fixtures(t)["task.skipped"]), "Out sick", esc, 1),
			"correction note":       string(correctionLine(t, `{"note":"`+esc+`"}`)),
			"correction reason":     string(correctionLine(t, `{"reason":"x`+esc+`"}`)),
			"correction kv":         string(correctionLine(t, `{"kv":[{"key":"k","value":"`+esc+`"}]}`)),
			"correction top reason": strings.Replace(string(fixtures(t)["event.corrected"]), "Typed the wrong length", esc, 1),
		}
		for name, line := range o {
			if !strings.Contains(line, esc) {
				t.Fatalf("%s: the test line does not carry the escape", name)
			}
			if _, err := event.Decode([]byte(line)); !errors.Is(err, event.ErrInvalidUTF8) {
				t.Errorf("%s: Decode = %v, want ErrInvalidUTF8", name, err)
			}
		}
	})

	t.Run("a valid surrogate pair decodes", func(t *testing.T) {
		e, err := event.Decode(annotated(`a\ud83d\ude00b \uD83D\uDE00`))
		if err != nil {
			t.Fatal(err)
		}
		if got := e.Payload.(event.CycleAnnotated).Note; got != "a\U0001f600b \U0001f600" {
			t.Errorf("note = %q", got)
		}
	})
	t.Run("a literal U+FFFD decodes and is kept", func(t *testing.T) {
		line := annotated("a\ufffdb")
		e, err := event.Decode(line)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.Payload.(event.CycleAnnotated).Note; got != "a\ufffdb" {
			t.Errorf("note = %q", got)
		}
		out, err := event.Encode(e)
		if err != nil || !bytes.Contains(out, []byte("a\ufffdb")) {
			t.Errorf("Encode = %s, %v", out, err)
		}
	})
	t.Run("an escaped U+FFFD decodes", func(t *testing.T) {
		if _, err := event.Decode(annotated(`a\ufffdb`)); err != nil {
			t.Errorf("Decode: %v", err)
		}
	})
	t.Run("an escaped backslash before u d800 is plain text", func(t *testing.T) {
		e, err := event.Decode(annotated(`a\\ud800b`))
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if got := e.Payload.(event.CycleAnnotated).Note; got != `a\ud800b` {
			t.Errorf("note = %q", got)
		}
	})

	t.Run("Encode refuses the same in raw data and in correction fields", func(t *testing.T) {
		e := newEvent(event.CycleAnnotated{CycleID: "c"})
		e.Payload = nil
		e.Data = json.RawMessage(`{"cycle_id":"c","note":"a\ud800b"}`)
		if line, err := event.Encode(e); !errors.Is(err, event.ErrInvalidUTF8) || line != nil {
			t.Errorf("Encode(Data) = %q, %v; want no bytes and ErrInvalidUTF8", line, err)
		}
		c := newEvent(event.EventCorrected{Target: "01J9Z3K8M2E000000000000002", Fields: map[string]json.RawMessage{"note": json.RawMessage(`"a\ud800b"`)}})
		if line, err := event.Encode(c); !errors.Is(err, event.ErrInvalidUTF8) || line != nil {
			t.Errorf("Encode(fields) = %q, %v; want no bytes and ErrInvalidUTF8", line, err)
		}
	})
}

func TestDecodeReadsOnlyTheNumberOneAsTheVersion(t *testing.T) {
	// A numeric version other than the literal 1 is an unknown version, which
	// stops the store; anything else that is not a version is an ordinary
	// decode failure, which the store may take for a torn tail.
	unknown := map[string]struct {
		v   int
		raw string
	}{
		"2": {2, "2"}, "0": {0, "0"}, "-1": {-1, "-1"}, "-0": {0, "-0"},
		"2.0": {0, "2.0"}, "1.0": {0, "1.0"}, "1e0": {0, "1e0"}, "1E0": {0, "1E0"}, "10e-1": {0, "10e-1"},
		"99999999999999999999":  {0, "99999999999999999999"},
		"-99999999999999999999": {0, "-99999999999999999999"},
		"1.5":                   {0, "1.5"},
	}
	for in, want := range unknown {
		t.Run("v "+in, func(t *testing.T) {
			for _, rest := range []string{`}`, `,"id":"x"}`} {
				_, err := event.Decode([]byte(`{"v":` + in + rest))
				var uv *event.UnknownVersionError
				if !errors.As(err, &uv) {
					t.Fatalf("Decode = %v, want *UnknownVersionError", err)
				}
				if uv.V != want.v || uv.Raw != want.raw {
					t.Errorf("UnknownVersionError = {V: %d, Raw: %q}, want {V: %d, Raw: %q}", uv.V, uv.Raw, want.v, want.raw)
				}
				if !strings.Contains(err.Error(), want.raw) {
					t.Errorf("Error() = %q does not show the version %s", err, want.raw)
				}
			}
		})
	}
	for _, in := range []string{`"2"`, `"1"`, `null`, `true`, `false`, `[1]`, `[]`, `{}`, `{"a":1}`, `""`} {
		t.Run("v "+in, func(t *testing.T) {
			_, err := event.Decode([]byte(`{"v":` + in + `}`))
			if err == nil {
				t.Fatal("Decode succeeded")
			}
			var uv *event.UnknownVersionError
			if errors.As(err, &uv) {
				t.Errorf("Decode = %v: only a numeric version is an unknown version", err)
			}
		})
	}
	t.Run("a missing v", func(t *testing.T) {
		_, err := event.Decode([]byte(`{"id":"x"}`))
		var uv *event.UnknownVersionError
		if err == nil || errors.As(err, &uv) {
			t.Errorf("Decode = %v, want an ordinary error", err)
		}
	})
	t.Run("the literal 1 is the supported version", func(t *testing.T) {
		if _, err := event.Decode(fixtures(t)["task.completed"]); err != nil {
			t.Errorf("Decode: %v", err)
		}
	})
	t.Run("Encode of an unknown version carries the number", func(t *testing.T) {
		e := newEvent(event.TaskCompleted{TaskID: "t"})
		e.V = 3
		_, err := event.Encode(e)
		var uv *event.UnknownVersionError
		if !errors.As(err, &uv) || uv.V != 3 || uv.Raw != "3" {
			t.Errorf("Encode = %v, want *UnknownVersionError{V: 3, Raw: \"3\"}", err)
		}
	})
}
