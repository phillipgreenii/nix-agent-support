package config

// Defaults of the optional `defaults` fields, applied when a configuration
// leaves them out. The values are those the design spec gives:
//
//   - maxFutureSkewSeconds: the event table's `effective_at` row, "max_future_skew_seconds
//     (default 60)" (INV-CONF-18).
//   - staleMinutes: the work-cycles rule on stale pauses, "stale_pause_minutes (default 45)".
//   - dueSoonMinutes, overtimeHighMinutes and boostMinutes: the spec states no
//     default in words; they are the values of its example configuration
//     (`due_soon_minutes: 30`, `overtime_high_minutes: 15`,
//     `boost_minutes: [5, 10, 25]`), which the spec says is itself valid.
const (
	defaultMaxFutureSkewSeconds = 60
	defaultStalePauseMinutes    = 45
	defaultDueSoonMinutes       = 30
	defaultOvertimeHighMinutes  = 15
)

func defaultBoostMinutes() []int { return []int{5, 10, 25} }

// CycleMinutes returns the planned duration of a cycle of the given type by
// the precedence of INV-CONF-5: the start-time override when there is one,
// then the type's own minutes, then defaults.cycle_minutes. A type the
// configuration does not define (a removed type, say) has no minutes of its
// own. The override is returned as given: its range is the request's check.
func (c *Config) CycleMinutes(cycleType string, override *int) int {
	if override != nil {
		return *override
	}
	if t, ok := c.cycles[cycleType]; ok && t.Minutes > 0 {
		return t.Minutes
	}
	return c.defaults.CycleMinutes
}

// Alert returns the alert settings of a cycle type, resolved per setting
// (INV-CONF-7). The sound is the type's, else defaults.alert's. The reminder
// sound is the first that is set of: the type's reminder_sound, the type's
// sound, defaults.alert's reminder_sound, defaults.alert's sound. So a type
// that sets only its sound reminds with it, and a type that sets neither
// reminds as the defaults do. "No sound" is a setting like any other: it ends
// the search at the level that wrote it. repeat_minutes is the type's, else
// defaults.alert's. A type the configuration does not define takes
// defaults.alert whole (INV-CONF-16).
func (c *Config) Alert(cycleType string) Alert {
	d := c.alert
	out := Alert{RepeatMinutes: d.RepeatMinutes}
	// defaults.alert.sound is required, so it is always set.
	out.Sound = *d.Sound
	out.ReminderSound = out.Sound
	if d.ReminderSound != nil {
		out.ReminderSound = *d.ReminderSound
	}
	if t, ok := c.cycles[cycleType]; ok {
		if t.Alert.Sound != nil {
			out.Sound = *t.Alert.Sound
			out.ReminderSound = *t.Alert.Sound
		}
		if t.Alert.ReminderSound != nil {
			out.ReminderSound = *t.Alert.ReminderSound
		}
		if t.Alert.RepeatMinutes != 0 {
			out.RepeatMinutes = t.Alert.RepeatMinutes
		}
	}
	return out
}

// GroupRank returns the sort rank of a task group (INV-CONF-9): the position of
// the group in group_order, counted from 0, and for every group not listed,
// and for the empty group of a task with none, one more than the last position,
// so those sort after every listed group and equal to each other. A group
// listed twice ranks by its first position.
func (c *Config) GroupRank(group string) int {
	if r, ok := c.groupRank[group]; ok {
		return r
	}
	return c.groups
}
