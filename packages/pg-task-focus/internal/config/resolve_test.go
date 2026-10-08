package config_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
)

// TestGroupRank pins INV-CONF-9: listed groups rank by position; a group that is
// not listed, and the empty group of a task with none, rank after every listed
// one and equal to each other.
func TestGroupRank(t *testing.T) {
	c := mustParseValid(t)
	start, during, end := c.GroupRank("Start of day"), c.GroupRank("During the day"), c.GroupRank("End of day")
	if start != 0 || during != 1 || end != 2 {
		t.Errorf("listed groups rank %d, %d, %d, want 0, 1, 2", start, during, end)
	}
	unlisted, empty := c.GroupRank("Not in the list"), c.GroupRank("")
	if unlisted != empty {
		t.Errorf("an unlisted group ranks %d and the empty group %d, want equal", unlisted, empty)
	}
	if unlisted <= end {
		t.Errorf("an unlisted group ranks %d, want after the last listed group (%d)", unlisted, end)
	}
	if other := c.GroupRank("Another unlisted group"); other != unlisted {
		t.Errorf("two unlisted groups rank %d and %d, want equal", unlisted, other)
	}
	// Case matters: a group name is not a zone, but it is still exact text.
	if got := c.GroupRank("start of day"); got != unlisted {
		t.Errorf("GroupRank(start of day) = %d, want the unlisted rank %d", got, unlisted)
	}
}

// TestGroupRankWithoutGroupOrder pins that a configuration with no group_order
// ranks every group, and none, equal.
func TestGroupRankWithoutGroupOrder(t *testing.T) {
	c := mustParse(t, edited(t, func(c map[string]any) { delete(c, "group_order") }))
	if a, b, e := c.GroupRank("Start of day"), c.GroupRank("anything"), c.GroupRank(""); a != b || b != e {
		t.Errorf("ranks %d, %d, %d, want all equal", a, b, e)
	}
}

// TestGroupRankUsesTheFirstPosition pins that a group listed twice ranks by its
// first position, and the groups after it keep their positions.
func TestGroupRankUsesTheFirstPosition(t *testing.T) {
	c := mustParse(t, edited(t, func(c map[string]any) { c["group_order"] = []any{"b", "a", "b", "c"} }))
	if b, a, cc := c.GroupRank("b"), c.GroupRank("a"), c.GroupRank("c"); b != 0 || a != 1 || cc != 3 {
		t.Errorf("ranks b=%d a=%d c=%d, want 0, 1, 3", b, a, cc)
	}
	if got := c.GroupRank("d"); got <= 3 {
		t.Errorf("an unlisted group ranks %d, want after every listed one", got)
	}
}

// TestAlertFallsBackForARemovedCycleType pins INV-CONF-16: a cycle type that no
// longer exists in the configuration takes defaults.alert, and its minutes fall
// to defaults.cycle_minutes.
func TestAlertFallsBackForARemovedCycleType(t *testing.T) {
	c := mustParse(t, edited(t, func(c map[string]any) {
		delete(at(c, "cycles"), "deep-work")
		at(c, "profiles", "normal")["cycles"] = []any{"review"}
		delete(at(c, "profiles", "on-call"), "cycles")
	}))
	if got, want := c.Alert("deep-work"), (config.Alert{Sound: "Glass", ReminderSound: "Tink", RepeatMinutes: 5}); got != want {
		t.Errorf("Alert(removed type) = %+v, want %+v", got, want)
	}
	if got := c.CycleMinutes("deep-work", nil); got != 25 {
		t.Errorf("CycleMinutes(removed type) = %d, want 25", got)
	}
}

// TestAlertResolvesFieldByField pins INV-CONF-7: each of sound, reminder_sound
// and repeat_minutes resolves per cycle type and then defaults.alert, and the
// reminder falls back to the resolved sound only when neither level sets one.
func TestAlertResolvesFieldByField(t *testing.T) {
	tests := []struct {
		name     string
		defaults map[string]any
		cycle    map[string]any // the alert of cycle type "t"; nil for none
		want     config.Alert
	}{
		{"a type with no alert takes the defaults", map[string]any{"sound": "A", "reminder_sound": "B", "repeat_minutes": 5}, nil, config.Alert{Sound: "A", ReminderSound: "B", RepeatMinutes: 5}},
		{"the type's sound over the default's, reminder from the defaults", map[string]any{"sound": "A", "reminder_sound": "B", "repeat_minutes": 5}, map[string]any{"sound": "C"}, config.Alert{Sound: "C", ReminderSound: "B", RepeatMinutes: 5}},
		{"the type's reminder over the default's", map[string]any{"sound": "A", "reminder_sound": "B", "repeat_minutes": 5}, map[string]any{"reminder_sound": "D"}, config.Alert{Sound: "A", ReminderSound: "D", RepeatMinutes: 5}},
		{"the type's repeat over the default's", map[string]any{"sound": "A", "reminder_sound": "B", "repeat_minutes": 5}, map[string]any{"repeat_minutes": 9}, config.Alert{Sound: "A", ReminderSound: "B", RepeatMinutes: 9}},
		{"no reminder anywhere: the default sound", map[string]any{"sound": "A", "repeat_minutes": 5}, nil, config.Alert{Sound: "A", ReminderSound: "A", RepeatMinutes: 5}},
		{"no reminder anywhere: the type's sound", map[string]any{"sound": "A", "repeat_minutes": 5}, map[string]any{"sound": "C"}, config.Alert{Sound: "C", ReminderSound: "C", RepeatMinutes: 5}},
		{"the default's reminder survives a type's sound", map[string]any{"sound": "A", "reminder_sound": "B", "repeat_minutes": 5}, map[string]any{"sound": "C", "repeat_minutes": 7}, config.Alert{Sound: "C", ReminderSound: "B", RepeatMinutes: 7}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := mustParse(t, edited(t, func(c map[string]any) {
				at(c, "defaults")["alert"] = tc.defaults
				ct := map[string]any{"title": "T"}
				if tc.cycle != nil {
					ct["alert"] = tc.cycle
				}
				at(c, "cycles")["t"] = ct
			}))
			if got := c.Alert("t"); got != tc.want {
				t.Errorf("Alert(t) = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestCycleMinutesPrecedence pins INV-CONF-5 on its own: the override, then the
// type's minutes, then defaults.cycle_minutes, with a type that sets none.
func TestCycleMinutesPrecedence(t *testing.T) {
	c := mustParse(t, edited(t, func(c map[string]any) {
		at(c, "cycles")["no-minutes"] = map[string]any{"title": "No minutes"}
	}))
	ten := 10
	for _, tc := range []struct {
		typ      string
		override *int
		want     int
	}{
		{"no-minutes", nil, 25},
		{"no-minutes", &ten, 10},
		{"deep-work", nil, 50},
		{"deep-work", &ten, 10},
		{"", nil, 25},
	} {
		if got := c.CycleMinutes(tc.typ, tc.override); got != tc.want {
			t.Errorf("CycleMinutes(%q, %v) = %d, want %d", tc.typ, tc.override, got, tc.want)
		}
	}
}
