package view_test

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
)

func TestEndedPeriodBannerStrings(t *testing.T) {
	tests := []struct {
		kind projection.Kind
		want string
	}{
		{projection.Day, "New day: roll over"},
		{projection.Week, "New week: roll over"},
		{projection.Sprint, "New sprint: roll over"},
		{projection.Kind("fortnight"), ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := view.RolloverBanner(tt.kind); got != tt.want {
				t.Errorf("RolloverBanner(%q) = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}

	t.Run("the state carries the banner of each ended period and none for a current one", func(t *testing.T) {
		weekEnd := civil.Date{Year: 2026, Month: time.October, Day: 11}
		sprintEnd := civil.Date{Year: 2026, Month: time.October, Day: 18}
		b := newLog(t)
		b.batch(t0, profileOf("normal"), dayIn(day1, newYork),
			periodOf("week", civil.Date{Year: 2026, Month: time.October, Day: 5}, &weekEnd, newYork),
			periodOf("sprint", civil.Date{Year: 2026, Month: time.October, Day: 5}, &sprintEnd, newYork))
		m, cfg := b.model(), testutil.LoadConfig(t, nil)

		// 2026-10-12 is the day after the week ended and inside the sprint.
		st := view.Build(m, cfg, time.Date(2026, time.October, 12, 15, 0, 0, 0, time.UTC))
		want := map[projection.Kind]string{
			projection.Day: "New day: roll over", projection.Week: "New week: roll over", projection.Sprint: "",
		}
		for _, p := range st.Periods {
			if p.Banner != want[p.Kind] {
				t.Errorf("%s banner = %q, want %q", p.Kind, p.Banner, want[p.Kind])
			}
		}

		st = view.Build(m, cfg, time.Date(2026, time.October, 19, 15, 0, 0, 0, time.UTC))
		if len(st.Periods) != 3 || st.Periods[2].Banner != "New sprint: roll over" {
			t.Errorf("Periods = %+v, want the sprint's banner to read New sprint: roll over", st.Periods)
		}
	})
}
