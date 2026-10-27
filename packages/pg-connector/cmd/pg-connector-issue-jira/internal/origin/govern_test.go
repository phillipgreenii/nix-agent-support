package origin

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func TestWorkersOverBackgroundCap(t *testing.T) {
	cases := []struct {
		name     string
		rate     int
		workers  int
		wantCap  int64
		wantOver bool
	}{
		// The defaults: floor(2 x 0.9) = 1 background request per second, 2 workers.
		{"the defaults are over", config.DefaultRequestRatePerSecond, config.DefaultConcurrency, 1, true},
		{"workers at the cap are not over", 5, 4, 4, false},
		{"one worker over the cap", 5, 5, 4, true},
		{"a rate of 1 leaves no background room", 1, 1, 0, true}, // floor(1 x 0.9) = 0
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.RequestRatePerSecond = c.rate
			g, err := governor.New(GovernorConfig(cfg), governor.Options{})
			if err != nil {
				t.Fatal(err)
			}
			got, over := WorkersOverBackgroundCap(g, c.workers)
			if got != c.wantCap || over != c.wantOver {
				t.Errorf("cap, over = %d, %v; want %d, %v", got, over, c.wantCap, c.wantOver)
			}
		})
	}
}
