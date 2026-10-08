package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/schemas"
)

// TestRule7Rejections is the table of the spec's rule 7 and the rest of
// INV-CONF-10: one fixture file per rejection, each asserting the Problem's
// path (the JSON pointer of the value at fault, or of the key that is missing
// or not allowed) and that its message names the problem in words an operator
// can act on. The inv field of each row names the INV-CONF invariant it pins
// (INV-CONF-2, 3, 6, 7, 8, 10, 11, 13 and 18).
func TestRule7Rejections(t *testing.T) {
	tests := []struct {
		file string
		inv  string
		path string
		msg  []string // every fragment MUST appear in the message
	}{
		// A profile that does not match the definitions (INV-CONF-2, INV-CONF-10).
		{"profile-unknown-task", "INV-CONF-10", "/profiles/normal/daily/3", []string{`"no-such-task"`, `profile "normal"`, "no task with that id"}},
		{"profile-unknown-cycle", "INV-CONF-10", "/profiles/normal/cycles/3", []string{`"no-such-cycle"`, `profile "normal"`, "no cycle type with that id"}},
		{"task-under-wrong-cadence", "INV-CONF-10", "/profiles/normal/weekly/1", []string{`"plan-day"`, "weekly", "its cadence is daily", "list it under daily"}},
		{"task-listed-twice", "INV-CONF-10", "/profiles/normal/daily/3", []string{`"plan-day"`, "more than once"}},
		{"default-profile-undefined", "INV-CONF-2", "/defaults/profile", []string{`"undefined"`, "not a defined profile", "normal", "on-call"}},
		{"no-profiles", "INV-CONF-2", "/profiles", []string{"at least one profile"}},

		// A due rule that does not fit its cadence (INV-CONF-3).
		{"daily-due-with-weekday", "INV-CONF-3", "/tasks/plan-day/due/weekday", []string{`"plan-day"`, "daily", "takes only at and tz", "remove weekday"}},
		{"daily-due-with-day", "INV-CONF-3", "/tasks/plan-day/due/day", []string{"daily", "takes only at and tz", "remove day"}},
		{"weekly-due-missing-weekday", "INV-CONF-3", "/tasks/weekly-update/due/weekday", []string{`"weekly-update"`, "weekly", "requires weekday", "mon to sun"}},
		{"weekly-due-with-day", "INV-CONF-3", "/tasks/weekly-update/due/day", []string{"weekly", "takes at, tz and weekday", "remove day"}},
		{"sprint-due-missing-day", "INV-CONF-3", "/tasks/capacity-check/due/day", []string{`"capacity-check"`, "sprint", "requires day"}},
		{"sprint-due-with-weekday", "INV-CONF-3", "/tasks/capacity-check/due/weekday", []string{"sprint", "takes at, tz and day", "remove weekday"}},
		{"weekday-thursday", "INV-CONF-3", "/tasks/weekly-update/due/weekday", []string{`"thursday"`, "mon, tue, wed, thu, fri, sat, sun"}},
		{"day-zero", "INV-CONF-3", "/tasks/capacity-check/due/day", []string{"day 0", "at least 1"}},
		{"day-too-large", "INV-CONF-3", "/tasks/capacity-check/due/day", []string{"99999999999999999999", "too large"}},
		{"at-single-digit-hour", "INV-CONF-3", "/tasks/plan-day/due/at", []string{`"9:00"`, "HH:MM", "09:00"}},
		{"at-2400", "INV-CONF-3", "/tasks/plan-day/due/at", []string{`"24:00"`, "HH:MM", "23:59"}},

		// A zone that is not a zone name (INV-CONF-3, INV-TIME-3, D12).
		{"tz-et", "INV-CONF-3", "/tasks/plan-day/due/tz", []string{`"ET"`, "not valid", "standard library", "America/New_York"}},
		{"tz-offset", "INV-CONF-3", "/tasks/plan-day/due/tz", []string{`"-05:00"`, "not valid"}},
		{"tz-local", "INV-CONF-3", "/tasks/plan-day/due/tz", []string{`"Local"`, "not valid", "host"}},
		{"tz-empty", "INV-CONF-3", "/tasks/plan-day/due/tz", []string{"not valid", "MUST be named", "no default zone"}},
		{"tz-wrong-case", "INV-CONF-3", "/tasks/plan-day/due/tz", []string{`"america/new_york"`, "not valid"}},
		{"tz-missing", "INV-CONF-3", "/tasks/plan-day/due/tz", []string{`required key "tz" is missing`}},

		// Durations and intervals (INV-CONF-11).
		{"cycle-minutes-zero", "INV-CONF-11", "/cycles/review/minutes", []string{"1 to 525600", "got 0"}},
		{"cycle-minutes-525601", "INV-CONF-11", "/cycles/review/minutes", []string{"1 to 525600", "got 525601"}},
		{"default-cycle-minutes-zero", "INV-CONF-11", "/defaults/cycle_minutes", []string{"1 to 525600", "got 0"}},
		{"boost-minutes-zero", "INV-CONF-11", "/defaults/boost_minutes/1", []string{"1 to 525600", "got 0"}},
		{"repeat-minutes-negative", "INV-CONF-11", "/defaults/alert/repeat_minutes", []string{"1 to 525600", "got -1"}},
		{"cycle-repeat-minutes-525601", "INV-CONF-11", "/cycles/deep-work/alert/repeat_minutes", []string{"1 to 525600", "got 525601"}},
		{"attention-threshold-zero", "INV-CONF-11", "/defaults/attention/due_soon_minutes", []string{"1 to 525600", "got 0"}},
		{"skew-negative", "INV-CONF-18", "/defaults/max_future_skew_seconds", []string{"0 to 31536000", "got -1"}},
		{"sound-empty", "INV-CONF-7", "/defaults/alert/sound", []string{"must not be empty"}},

		// Keys (INV-CONF-6).
		{"key-has-space", "INV-CONF-6", "/cycles/review/keys/0", []string{`"Has Space"`, "[a-z0-9_-]+"}},
		{"key-cycle-type", "INV-CONF-6", "/cycles/review/keys/1", []string{`"cycle_type"`, "reserved"}},

		// The listener and the URL (INV-CONF-13).
		{"listen-port-missing", "INV-CONF-13", "/listen_port", []string{`required key "listen_port" is missing`}},
		{"listen-port-zero", "INV-CONF-13", "/listen_port", []string{"1 to 65535", "got 0"}},
		{"public-url-ftp", "INV-CONF-13", "/public_url", []string{`"ftp://x"`, "http or https URL"}},
		{"public-url-no-host", "INV-CONF-13", "/public_url", []string{`"https://:8080"`, "http or https URL"}},

		// Unknown keys, and the options the operator ruled out (INV-CONF-8).
		{"unknown-top-level-key", "INV-CONF-8", "/theme", []string{`unknown key "theme"`, "not a configuration option"}},
		{"snooze-minutes-in-alert", "INV-CONF-8", "/defaults/alert/snooze_minutes", []string{`unknown key "snooze_minutes"`, "no snooze, mute, acknowledge or carry-over"}},
		{"mute-in-cycle-alert", "INV-CONF-8", "/cycles/deep-work/alert/mute", []string{`unknown key "mute"`, "no snooze, mute, acknowledge or carry-over"}},
		{"max-repeats-in-defaults", "INV-CONF-8", "/defaults/max_repeats", []string{`unknown key "max_repeats"`, "no snooze, mute, acknowledge or carry-over"}},
		{"carry-over-in-task", "INV-CONF-8", "/tasks/plan-day/carry_over", []string{`unknown key "carry_over"`, "no snooze, mute, acknowledge or carry-over"}},
		{"day-start-top-level", "INV-CONF-8", "/day_start", []string{`unknown key "day_start"`, "no day start"}},
		{"empty-task-id", "INV-CONF-10", "/tasks/", []string{"id", "must not be empty"}},
	}

	// Every fixture in invalid/ is exercised, so none is left unpinned.
	files, err := filepath.Glob(filepath.Join(testdata, "invalid", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, tc := range tests {
		listed[tc.file] = true
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if !listed[name] {
			t.Errorf("fixture %s is not covered by the table", name)
		}
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(testdata, "invalid", tc.file+".json"))
			if err != nil {
				t.Fatal(err)
			}
			c, err := config.Parse(raw)
			if err == nil {
				t.Fatalf("Parse accepted the configuration (%s)", tc.inv)
			}
			if c != nil {
				t.Error("Parse returned a Config alongside the error")
			}
			ve := validationError(t, err)
			p, ok := problemAt(ve, tc.path)
			if !ok {
				t.Fatalf("no Problem at %q (%s); got:\n%v", tc.path, tc.inv, ve)
			}
			for _, frag := range tc.msg {
				if !strings.Contains(p.Message, frag) {
					t.Errorf("message at %q = %q, want it to contain %q", tc.path, p.Message, frag)
				}
			}
			for _, other := range ve.Problems {
				if strings.TrimSpace(other.Message) == "" {
					t.Errorf("a Problem at %q has no message", other.Path)
				}
			}
		})
	}
}

// TestRule7RejectionsAreExact pins that a fixture with one fault yields that
// one Problem and nothing else, so no rejection drags a second, unrelated
// complaint behind it.
func TestRule7RejectionsAreExact(t *testing.T) {
	for file, want := range map[string][]string{
		"profile-unknown-task":       {"/profiles/normal/daily/3"},
		"task-under-wrong-cadence":   {"/profiles/normal/weekly/1"},
		"daily-due-with-weekday":     {"/tasks/plan-day/due/weekday"},
		"weekly-due-missing-weekday": {"/tasks/weekly-update/due/weekday"},
		"tz-et":                      {"/tasks/plan-day/due/tz"},
		"tz-empty":                   {"/tasks/plan-day/due/tz"},
		"at-single-digit-hour":       {"/tasks/plan-day/due/at"},
		"day-zero":                   {"/tasks/capacity-check/due/day"},
		"key-cycle-type":             {"/cycles/review/keys/1"},
		"public-url-ftp":             {"/public_url"},
		"public-url-no-host":         {"/public_url"},
		"default-profile-undefined":  {"/defaults/profile"},
		"unknown-top-level-key":      {"/theme"},
		"listen-port-missing":        {"/listen_port"},
	} {
		raw, err := os.ReadFile(filepath.Join(testdata, "invalid", file+".json"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = config.Parse(raw)
		ve := validationError(t, err)
		if got := paths(ve); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: problem paths = %v, want %v\n%v", file, got, want, ve)
		}
	}
}

// TestAllProblemsReported pins INV-CONF-10: every problem at once, each with
// its path, whether found by the schema or by the semantic checks.
func TestAllProblemsReported(t *testing.T) {
	t.Run("three semantic faults", func(t *testing.T) {
		raw := edited(t, func(c map[string]any) {
			at(c, "profiles", "normal")["daily"] = []any{"plan-day", "no-such-task"}
			at(c, "tasks", "plan-day", "due")["tz"] = "ET"
			at(c, "defaults")["profile"] = "undefined"
		})
		_, err := config.Parse(raw)
		ve := validationError(t, err)
		want := []string{"/defaults/profile", "/profiles/normal/daily/1", "/tasks/plan-day/due/tz"}
		if got := paths(ve); !reflect.DeepEqual(got, want) {
			t.Errorf("problem paths = %v, want %v\n%v", got, want, ve)
		}
	})

	t.Run("schema and semantic faults together", func(t *testing.T) {
		raw := edited(t, func(c map[string]any) {
			delete(c, "listen_port")                                  // schema: required
			at(c, "cycles", "review")["minutes"] = 0                  // schema: range
			at(c, "tasks", "plan-day", "due")["tz"] = "Local"         // semantic: zone
			at(c, "profiles", "normal")["cycles"] = []any{"absent"}   // semantic: unknown cycle
			c["snooze_minutes"] = 5                                   // schema: unknown key
			at(c, "cycles", "review")["keys"] = []any{"cycle_type"}   // semantic: reserved
			at(c, "profiles", "normal")["weekly"] = []any{"plan-day"} // semantic: cadence
		})
		_, err := config.Parse(raw)
		ve := validationError(t, err)
		want := []string{
			"/cycles/review/keys/0",
			"/cycles/review/minutes",
			"/listen_port",
			"/profiles/normal/cycles/0",
			"/profiles/normal/weekly/0",
			"/snooze_minutes",
			"/tasks/plan-day/due/tz",
		}
		if got := paths(ve); !reflect.DeepEqual(got, want) {
			t.Errorf("problem paths = %v, want %v\n%v", got, want, ve)
		}
	})

	t.Run("one missing key reported once per key", func(t *testing.T) {
		raw := edited(t, func(c map[string]any) { delete(c, "defaults"); delete(c, "listen_port"); delete(c, "tasks") })
		_, err := config.Parse(raw)
		ve := validationError(t, err)
		want := []string{"/defaults", "/listen_port", "/tasks"}
		if got := paths(ve); !reflect.DeepEqual(got, want) {
			t.Errorf("problem paths = %v, want %v\n%v", got, want, ve)
		}
	})

	t.Run("the error text lists them", func(t *testing.T) {
		_, err := config.Parse(edited(t, func(c map[string]any) { delete(c, "listen_port"); c["theme"] = 1 }))
		for _, frag := range []string{"2 problems", "/listen_port", "/theme"} {
			if !strings.Contains(err.Error(), frag) {
				t.Errorf("Error() = %q, want it to contain %q", err.Error(), frag)
			}
		}
	})
}

// TestUnknownKeysAreRejectedEverywhere pins INV-CONF-8: no object of the
// configuration takes a key it does not define, and in particular none takes an
// option to mute, snooze, acknowledge or cap the overtime sound, to carry a task
// over, or to start the day at an offset (D17).
func TestUnknownKeysAreRejectedEverywhere(t *testing.T) {
	places := [][]string{
		nil,
		{"defaults"},
		{"defaults", "alert"},
		{"defaults", "attention"},
		{"profiles", "normal"},
		{"tasks", "plan-day"},
		{"tasks", "plan-day", "due"},
		{"cycles", "review"},
		{"cycles", "deep-work", "alert"},
	}
	for _, key := range []string{"snooze_minutes", "mute", "max_repeats", "carry_over", "acknowledge", "day_start", "typo"} {
		for _, place := range places {
			path := "/" + strings.Join(append(append([]string(nil), place...), key), "/")
			t.Run(path, func(t *testing.T) { // pins INV-CONF-8
				raw := edited(t, func(c map[string]any) { at(c, place...)[key] = 3 })
				_, err := config.Parse(raw)
				if err == nil {
					t.Fatalf("Parse accepted %s", key)
				}
				p, ok := problemAt(validationError(t, err), path)
				if !ok || !strings.Contains(p.Message, `unknown key "`+key+`"`) {
					t.Errorf("no Problem at %q naming the key; got %v", path, err)
				}
			})
		}
	}
}

// TestMinutesBounds pins INV-CONF-11: every minutes or interval value is an
// integer from 1 to 525600; the bounds themselves are accepted.
func TestMinutesBounds(t *testing.T) {
	fields := map[string]struct {
		set  func(c map[string]any, v any)
		path string
	}{
		"defaults.cycle_minutes":        {func(c map[string]any, v any) { at(c, "defaults")["cycle_minutes"] = v }, "/defaults/cycle_minutes"},
		"defaults.boost_minutes":        {func(c map[string]any, v any) { at(c, "defaults")["boost_minutes"] = []any{5, v} }, "/defaults/boost_minutes/1"},
		"defaults.alert.repeat_minutes": {func(c map[string]any, v any) { at(c, "defaults", "alert")["repeat_minutes"] = v }, "/defaults/alert/repeat_minutes"},
		"defaults.attention":            {func(c map[string]any, v any) { at(c, "defaults", "attention")["stale_pause_minutes"] = v }, "/defaults/attention/stale_pause_minutes"},
		"cycles.minutes":                {func(c map[string]any, v any) { at(c, "cycles", "review")["minutes"] = v }, "/cycles/review/minutes"},
		"cycles.alert.repeat_minutes":   {func(c map[string]any, v any) { at(c, "cycles", "deep-work", "alert")["repeat_minutes"] = v }, "/cycles/deep-work/alert/repeat_minutes"},
	}
	for name, f := range fields {
		t.Run(name, func(t *testing.T) {
			for _, ok := range []any{1, 525600} {
				mustParse(t, edited(t, func(c map[string]any) { f.set(c, ok) }))
			}
			for _, bad := range []any{0, -1, 525601, 1.5, "25", nil, true} {
				_, err := config.Parse(edited(t, func(c map[string]any) { f.set(c, bad) }))
				if err == nil {
					t.Errorf("Parse accepted %v", bad)
					continue
				}
				p, found := problemAt(validationError(t, err), f.path)
				if !found || !strings.Contains(p.Message, "1 to 525600") {
					t.Errorf("value %v: no Problem at %q naming the range; got %v", bad, f.path, err)
				}
			}
		})
	}
}

// TestParseRejectsWhatIsNotAConfigurationObject pins that a document that is not
// JSON, or not an object, is one problem for the whole document.
func TestParseRejectsWhatIsNotAConfigurationObject(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":         "",
		"whitespace":    "  \n",
		"not JSON":      "defaults:\n  cycle_minutes: 25\n",
		"truncated":     `{"defaults": {`,
		"trailing data": `{"a": 1} {"b": 2}`,
		"an array":      `[]`,
		"null":          `null`,
		"a number":      `7`,
		"a string":      `"config"`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Parse([]byte(raw))
			ve := validationError(t, err)
			if len(ve.Problems) != 1 || ve.Problems[0].Path != "" || ve.Problems[0].Message == "" {
				t.Errorf("problems = %+v, want one problem for the whole document", ve.Problems)
			}
		})
	}
}

// TestConfigSchemaDeclaresDraft2020 pins INV-CONF-14: the configuration has a
// JSON Schema that declares 2020-12, and the loader applies it before the
// semantic checks, so a schema violation is reported alongside the semantic
// ones.
func TestConfigSchemaDeclaresDraft2020(t *testing.T) {
	var s struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(schemas.Config(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Schema != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %q, want the 2020-12 meta-schema", s.Schema)
	}

	_, err := config.Parse(edited(t, func(c map[string]any) {
		at(c, "tasks", "plan-day")["cadence"] = "monthly"
		at(c, "defaults")["profile"] = "undefined"
	}))
	ve := validationError(t, err)
	if _, ok := problemAt(ve, "/tasks/plan-day/cadence"); !ok {
		t.Errorf("the schema violation is missing: %v", ve)
	}
	if _, ok := problemAt(ve, "/defaults/profile"); !ok {
		t.Errorf("the semantic violation is missing: %v", ve)
	}
}
