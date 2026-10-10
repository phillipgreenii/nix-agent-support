package wire

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
)

// ConfigDoc is the resolved, validated configuration GET /config returns:
// every optional setting filled in, so a client sees what the daemon uses.
type ConfigDoc struct {
	Digest     string          `json:"digest"`
	ListenPort int             `json:"listen_port"`
	PublicURL  string          `json:"public_url,omitempty"`
	Defaults   ConfigDefaults  `json:"defaults"`
	GroupOrder []string        `json:"group_order"`
	Profiles   []ConfigProfile `json:"profiles"`
	Tasks      []ConfigTask    `json:"tasks"`
	Cycles     []ConfigCycle   `json:"cycles"`
}

// ConfigDefaults is the defaults section, resolved.
type ConfigDefaults struct {
	CycleMinutes         int             `json:"cycle_minutes"`
	BoostMinutes         []int           `json:"boost_minutes"`
	Profile              string          `json:"profile"`
	MaxFutureSkewSeconds int             `json:"max_future_skew_seconds"`
	Alert                ConfigAlert     `json:"alert"`
	Attention            ConfigAttention `json:"attention"`
}

// ConfigAlert is a resolved alert setting; a sound is a name or null.
type ConfigAlert struct {
	Sound         *string `json:"sound"`
	ReminderSound *string `json:"reminder_sound"`
	RepeatMinutes int     `json:"repeat_minutes"`
}

// ConfigAttention holds the attention thresholds.
type ConfigAttention struct {
	DueSoonMinutes      int `json:"due_soon_minutes"`
	OvertimeHighMinutes int `json:"overtime_high_minutes"`
	StalePauseMinutes   int `json:"stale_pause_minutes"`
}

// ConfigProfile is one profile.
type ConfigProfile struct {
	Name   string   `json:"name"`
	Daily  []string `json:"daily"`
	Weekly []string `json:"weekly"`
	Sprint []string `json:"sprint"`
	Cycles []string `json:"cycles"`
}

// ConfigTask is one task definition. Due is the rule as the log snapshots it.
type ConfigTask struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Cadence string   `json:"cadence"`
	Group   string   `json:"group,omitempty"`
	Link    string   `json:"link,omitempty"`
	Due     due.Rule `json:"due"`
}

// ConfigCycle is one cycle type, with its alert as resolved against the
// defaults.
type ConfigCycle struct {
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	Minutes int         `json:"minutes"`
	Keys    []string    `json:"keys"`
	Alert   ConfigAlert `json:"alert"`
}

func soundName(s config.Sound) *string {
	if s.None() {
		return nil
	}
	n := s.Name()
	return &n
}

func alertOf(a config.Alert) ConfigAlert {
	return ConfigAlert{Sound: soundName(a.Sound), ReminderSound: soundName(a.ReminderSound), RepeatMinutes: a.RepeatMinutes}
}

func strs(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// FromConfig converts the configuration.
func FromConfig(c *config.Config) ConfigDoc {
	d := c.Defaults()
	out := ConfigDoc{
		Digest: c.Digest(), ListenPort: c.ListenPort(), PublicURL: c.PublicURL(),
		Defaults: ConfigDefaults{
			CycleMinutes: d.CycleMinutes, BoostMinutes: d.BoostMinutes, Profile: d.Profile,
			MaxFutureSkewSeconds: d.MaxFutureSkewSeconds, Alert: alertOf(d.Alert),
			Attention: ConfigAttention{
				DueSoonMinutes: d.Attention.DueSoonMinutes, OvertimeHighMinutes: d.Attention.OvertimeHighMinutes,
				StalePauseMinutes: d.Attention.StalePauseMinutes,
			},
		},
		GroupOrder: strs(c.GroupOrder()), Profiles: []ConfigProfile{}, Tasks: []ConfigTask{}, Cycles: []ConfigCycle{},
	}
	for _, p := range c.Profiles() {
		out.Profiles = append(out.Profiles, ConfigProfile{
			Name: p.Name, Daily: strs(p.Daily), Weekly: strs(p.Weekly), Sprint: strs(p.Sprint), Cycles: strs(p.Cycles),
		})
	}
	for _, t := range c.Tasks() {
		out.Tasks = append(out.Tasks, ConfigTask{
			ID: t.ID, Title: t.Title, Cadence: string(t.Cadence), Group: t.Group, Link: t.Link, Due: t.Due,
		})
	}
	for _, t := range c.CycleTypes() {
		out.Cycles = append(out.Cycles, ConfigCycle{
			ID: t.ID, Title: t.Title, Minutes: c.CycleMinutes(t.ID, nil), Keys: strs(t.Keys), Alert: alertOf(c.Alert(t.ID)),
		})
	}
	return out
}
