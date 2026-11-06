package cli

import (
	"github.com/spf13/cobra"
)

func init() { registerCommand(groupJob, newJobRunCmd) }

// RunOptions holds the values of the job run flags. The run-lifecycle code
// reads it; this file only registers the flags and stores their values.
type RunOptions struct {
	Lens         string
	Scope        string
	From         string
	To           string
	Duration     string
	At           string
	Profile      string
	Analyst      string
	EvidenceOnly bool
	RunKey       string
	ScheduleID   string
}

// runJob is the seam the run-lifecycle code replaces.
var runJob = func(*cobra.Command, *RunOptions) error { return notImplemented() }

func newJobRunCmd() *cobra.Command {
	opts := &RunOptions{}
	cmd := &cobra.Command{
		Use:   "run <lens>",
		Short: "Run one review of a lens over a scope and window",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Lens = args[0]
			if opts.Scope == "" {
				return usageErrorf("required flag --scope not set")
			}
			return runJob(cmd, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.Scope, "scope", "", "scope name to review")
	f.StringVar(&opts.From, "from", "", "window start (RFC 3339)")
	f.StringVar(&opts.To, "to", "", "window end (RFC 3339)")
	f.StringVar(&opts.Duration, "duration", "", "window length ending at --at (or now)")
	f.StringVar(&opts.At, "at", "", "window end for --duration (RFC 3339; default now)")
	f.StringVar(&opts.Profile, "profile", "", "analyst profile")
	f.StringVar(&opts.Analyst, "analyst", "", "analyst backend: ccpool, interactive or none (overrides the profile)")
	f.BoolVar(&opts.EvidenceOnly, "evidence-only", false, "collect evidence only; no analyst")
	f.StringVar(&opts.RunKey, "run-key", "", "run key <lens>/<scope>/<window>/<window end> (set by the router adapter)")
	f.StringVar(&opts.ScheduleID, "schedule-id", "", "schedule id (set by the router adapter)")
	return cmd
}
