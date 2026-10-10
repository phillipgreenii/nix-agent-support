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
	if got, want := c.Alert("deep-work"), (config.Alert{Sound: config.Named("Glass"), ReminderSound: config.Named("Tink"), RepeatMinutes: 5}); got != want {
		t.Errorf("Alert(removed type) = %+v, want %+v", got, want)
	}
	if got := c.CycleMinutes("deep-work", nil); got != 25 {
		t.Errorf("CycleMinutes(removed type) = %d, want 25", got)
	}
}

// absent marks a defaults.alert reminder_sound that is left out of the document.
var absent = struct{}{}

// TestAlertResolution pins INV-CONF-7. The sound is the type's, then the
// default's. The reminder sound is the first set of the type's reminder_sound,
// the type's sound, the default's reminder_sound, the default's sound. An
// explicit null is a setting (no sound), not an absent key. repeat_minutes is
// the type's, then the default's.
func TestAlertResolution(t *testing.T) {
	// sd is defaults.alert: nil is JSON null (no sound), absent leaves the key out.
	sd := func(sound, reminder any) map[string]any {
		m := map[string]any{"sound": sound, "repeat_minutes": 5}
		if reminder != absent {
			m["reminder_sound"] = reminder
		}
		return m
	}
	al := func(sound, reminder config.Sound, repeat int) config.Alert {
		return config.Alert{Sound: sound, ReminderSound: reminder, RepeatMinutes: repeat}
	}
	A, B, C, D := config.Named("A"), config.Named("B"), config.Named("C"), config.Named("D")
	none := config.NoSound
	tests := []struct {
		name     string
		defaults map[string]any
		cycle    map[string]any // the alert of cycle type "t"; nil for none
		want     config.Alert
	}{
		{"a type with no alert takes the defaults", sd("A", "B"), nil, al(A, B, 5)},
		{"the type's sound brings its own reminder, not the default's", sd("A", "B"), map[string]any{"sound": "C"}, al(C, C, 5)},
		{"the type's reminder over the default's", sd("A", "B"), map[string]any{"reminder_sound": "D"}, al(A, D, 5)},
		{"the type's reminder over its own sound", sd("A", "B"), map[string]any{"sound": "C", "reminder_sound": "D"}, al(C, D, 5)},
		{"the type's repeat over the default's", sd("A", "B"), map[string]any{"repeat_minutes": 9}, al(A, B, 9)},
		{"a type's repeat leaves the sounds alone", sd("A", "B"), map[string]any{"sound": "C", "repeat_minutes": 7}, al(C, C, 7)},
		{"no reminder anywhere: the default sound", sd("A", absent), nil, al(A, A, 5)},
		{"no reminder anywhere: the type's sound", sd("A", absent), map[string]any{"sound": "C"}, al(C, C, 5)},

		// An explicit no sound (null) is a setting, distinct from an absent key.
		{"the default's sound is none, the reminder follows it", sd(nil, absent), nil, al(none, none, 5)},
		{"the default's reminder is none, the sound plays", sd("A", nil), nil, al(A, none, 5)},
		{"the default's sound is none, its reminder plays", sd(nil, "B"), nil, al(none, B, 5)},
		{"the type's sound is none, silent reminders too", sd("A", "B"), map[string]any{"sound": nil}, al(none, none, 5)},
		{"the type's sound is none, its own reminder plays", sd("A", "B"), map[string]any{"sound": nil, "reminder_sound": "D"}, al(none, D, 5)},
		{"the type's reminder is none, its sound plays", sd("A", "B"), map[string]any{"reminder_sound": nil}, al(A, none, 5)},
		{"the type's sound plays and its reminder is none", sd("A", "B"), map[string]any{"sound": "C", "reminder_sound": nil}, al(C, none, 5)},
		{"a type turns sound back on over a none default", sd(nil, nil), map[string]any{"sound": "C"}, al(C, C, 5)},
		{"a type turns only the reminder on over a none default", sd(nil, nil), map[string]any{"reminder_sound": "D"}, al(none, D, 5)},
		{"an absent key does not inherit none from the type's sibling", sd("A", nil), map[string]any{"repeat_minutes": 8}, al(A, none, 8)},
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
