package config

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/query"
)

func timerCfg(querySuffix string) string {
	return `
[[query]]
name = "weekly"
type = "timer"
emits = ["scan.due"]
` + querySuffix + `

[[role]]
name = "r"
handler = "/bin/h"
binds = ["scan.due"]
`
}

func loadErr(t *testing.T, body string) string {
	t.Helper()
	absentGlobalConfig(t)
	writeCfg(t, body)
	_, err := Load()
	if err == nil {
		t.Fatalf("Load succeeded, want an error for:\n%s", body)
	}
	return err.Error()
}

func TestLoad_timerMetadataReachesTheTickPayload(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, timerCfg(`metadata = { lens = "security", scope = "repo" }`))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	evts, err := c.Queries[0].Query.Run(context.Background(), query.Env{})
	if err != nil || len(evts) != 1 {
		t.Fatalf("Run = (%v, %v)", evts, err)
	}
	raw, err := json.Marshal(discover.ToQueueEvent(evts[0]).Payload)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Metadata["lens"] != "security" || payload.Metadata["scope"] != "repo" {
		t.Errorf("payload metadata = %s", raw)
	}
}

func TestLoad_timerMetadataEmptyEqualsAbsent(t *testing.T) {
	var payloads []string
	for _, suffix := range []string{``, `metadata = {}`} {
		absentGlobalConfig(t)
		writeCfg(t, timerCfg(suffix))
		c, err := Load()
		if err != nil {
			t.Fatalf("Load(%q): %v", suffix, err)
		}
		evts, _ := c.Queries[0].Query.Run(context.Background(), query.Env{})
		p := discover.ToQueueEvent(evts[0]).Payload
		raw, _ := json.Marshal(p)
		// Normalize the per-tick id; everything else must be byte-identical.
		p["id"] = ""
		norm, _ := json.Marshal(p)
		if strings.Contains(string(raw), `"metadata":{`) {
			t.Errorf("payload %s must not carry a metadata object", raw)
		}
		payloads = append(payloads, string(norm))
	}
	if payloads[0] != payloads[1] {
		t.Errorf("absent %s != empty %s", payloads[0], payloads[1])
	}
	if want := `{"id":"","metadata":null,"title":"timer tick","type":"timer"}`; payloads[0] != want {
		t.Errorf("payload = %s, want today's %s", payloads[0], want)
	}
}

func TestLoad_timerMetadataDoesNotAliasConfig(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, timerCfg(`metadata = { lens = "security" }`))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	run := func() map[string]any {
		evts, _ := c.Queries[0].Query.Run(context.Background(), query.Env{})
		return evts[0].Item.Metadata
	}
	m := run()
	m["lens"] = "tampered"
	if got := run()["lens"]; got != "security" {
		t.Errorf("next tick lens = %v, want security", got)
	}
}

func TestLoad_timerMetadataRejections(t *testing.T) {
	keys17 := make([]string, 17)
	for i := range keys17 {
		keys17[i] = fmt.Sprintf("k%d = \"v\"", i)
	}
	cases := []struct{ name, body, want string }{
		{"integer", timerCfg(`metadata = { lens = 1 }`), `metadata.lens must be a string, got integer`},
		{"bool", timerCfg(`metadata = { lens = true }`), `metadata.lens must be a string, got bool`},
		{"array", timerCfg(`metadata = { lens = ["a"] }`), `metadata.lens must be a string, got array`},
		{"reserved scheduled_for", timerCfg(`metadata = { scheduled_for = "x" }`), `metadata key "scheduled_for" is reserved (reserved: scheduled_for, on_missed)`},
		{"reserved on_missed", timerCfg(`metadata = { on_missed = "x" }`), `metadata key "on_missed" is reserved`},
		{"17 keys", timerCfg("metadata = { " + strings.Join(keys17, ", ") + " }"), `metadata has 17 keys; at most 16 are allowed`},
		{"non-timer", `
[[query]]
name = "c"
type = "command"
emits = ["e"]
metadata = { lens = "x" }
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
handler = "/bin/h"
binds = ["e"]
`, `metadata is only supported on type = "timer" queries`},
		{"trigger typo", timerCfg("[query.trigger]\nkind = \"period\"\nevry = \"5m\""), `unknown key(s) evry`},
		{"calendar key on period", timerCfg("[query.trigger]\nkind = \"period\"\nat = \"03:30\""), `only valid for kind = "calendar"`},
		{"calendar key on default kind", timerCfg("[query.trigger]\ndays = [\"sun\"]"), `only valid for kind = "calendar"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := loadErr(t, tc.body); !strings.Contains(got, tc.want) || !strings.Contains(got, `query[0] "`) {
				t.Errorf("error = %q, want it to contain %q with the query prefix", got, tc.want)
			}
		})
	}
}

func TestLoad_timerMetadataOnNonTimerEmptyIsAccepted(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "c"
type = "command"
emits = ["e"]
metadata = {}
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
handler = "/bin/h"
binds = ["e"]
`)
	if _, err := Load(); err != nil {
		t.Fatalf("metadata = {} on a non-timer query must be treated as absent: %v", err)
	}
}

// With every set alongside a calendar key, this packet stays silent: the
// calendar packet's mutual-exclusion message covers it.
func TestLoad_calendarKeyWithEveryEmitsNoCalendarOnlyMessage(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, timerCfg("[query.trigger]\nkind = \"period\"\nevery = \"5m\"\nat = \"03:30\""))
	_, err := Load()
	if err != nil && strings.Contains(err.Error(), `only valid for kind = "calendar"`) {
		t.Errorf("error = %v, must not carry the calendar-only message when every is set", err)
	}
}

func TestLoad_periodEveryDecodesAsBefore(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, timerCfg("[query.trigger]\nkind = \"period\"\nevery = \"5m\""))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	pt, ok := c.Queries[0].Query.Trigger().(query.PeriodTrigger)
	if !ok || pt.Every.Minutes() != 5 {
		t.Errorf("trigger = %#v, want PeriodTrigger{5m}", c.Queries[0].Query.Trigger())
	}
}
