package origin

import (
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// Bucket is the one declared bucket: Jira requests, metered per second.
const Bucket governor.Bucket = "requests"

// BackgroundShare is the fraction of the request rate the background classes
// may spend; the rest stays for interactive calls.
const BackgroundShare = 0.9

// GovernorConfig is the governor configuration of the daemon: ONE declared
// bucket (unit requests, a one-second window, Cap = request_rate_per_second,
// no reserve) and a background share of 0.9.
func GovernorConfig(cfg *config.Config) governor.Config {
	return governor.Config{
		Declared: []governor.BucketSpec{{
			Bucket: Bucket,
			Unit:   "requests",
			Limits: governor.Limits{
				Cap:     int64(cfg.RequestRatePerSecond),
				Reserve: 0,
				Window:  time.Second,
			},
		}},
		BackgroundShare: BackgroundShare,
	}
}

// NewCLIRunner builds the inner runner of the daemon: it execs pjira ONLY by
// the absolute pjira_path and with ONLY the pjira_env entries as its
// environment. The Env slice is empty, never nil, so the process environment
// is never inherited.
func NewCLIRunner(cfg *config.Config) *internal.CLIRunner {
	env := make([]string, 0, len(cfg.PjiraEnv))
	for k, v := range cfg.PjiraEnv {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return &internal.CLIRunner{BinaryOverride: cfg.PjiraPath, Env: env}
}

// WorkersOverBackgroundCap reports the background cap of the daemon's bucket
// (the requests one window admits to background work, floor(request_rate_per_second
// x BackgroundShare)) and whether workers exceed it. More workers than that
// cannot all run background requests at once; the daemon logs it at start-up.
func WorkersOverBackgroundCap(gov *governor.Governor, workers int) (backgroundCap int64, over bool) {
	bs, ok := gov.State().Bucket(Bucket)
	if !ok {
		return 0, false
	}
	return bs.BackgroundCap, int64(workers) > bs.BackgroundCap
}
