package collect

import (
	"context"
	"log/slog"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/failure"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
	"github.com/phillipgreenii/beads-exporter/internal/stranded"
)

// PassStranded is the stranded-claim pass name, also its pass label value.
const PassStranded = "stranded"

// StrandedEvent is the value of the event attribute on the one log line the
// pass writes per stranded claim.
const StrandedEvent = "stranded_claim"

// StrandedConfig configures the stranded pass.
type StrandedConfig struct {
	// MaxAge is the staleClaimHours age: a claim whose updated_at is older is
	// stranded.
	MaxAge time.Duration
}

// StrandedPass decides, for every claimed not-closed bead, whether its claim
// is stranded, judged by the bead's own updated_at. It spawns its own guarded
// bd list per database (so the stale-JSONL guard and the timeout apply exactly
// as for the main pass and a failing main pass never lets it classify a stale
// list). It reports counts and timestamps as metrics and one log line per
// stranded claim; it never releases or changes a claim.
type StrandedPass struct {
	cfg StrandedConfig
	log *slog.Logger
}

// NewStrandedPass builds the stranded pass.
func NewStrandedPass(cfg StrandedConfig, log *slog.Logger) *StrandedPass {
	return &StrandedPass{cfg: cfg, log: log}
}

// Name implements Pass.
func (*StrandedPass) Name() string { return PassStranded }

// Reasons implements Pass: the bd-driven reasons, nothing else.
func (*StrandedPass) Reasons() []failure.Reason { return baseReasons() }

// Collect implements Pass.
func (p *StrandedPass) Collect(ctx context.Context, env PassEnv) (metrics.Emitter, error) {
	beads, err := env.DB.Adapter.List(ctx, bd.ListOpts{})
	if err != nil {
		return nil, err
	}
	res := stranded.Classify(beads, env.Now, p.cfg.MaxAge)
	for _, c := range res.Claims {
		attrs := []any{
			"event", StrandedEvent,
			"bead", c.BeadID,
			"db", env.DB.Name,
			"assignee", c.Assignee,
			"status", c.Status,
		}
		if !c.ClaimTime.IsZero() {
			attrs = append(attrs, "claim_time", c.ClaimTime.UTC().Format(time.RFC3339))
		}
		if !c.LastUpdate.IsZero() {
			attrs = append(attrs, "last_update", c.LastUpdate.UTC().Format(time.RFC3339))
		}
		p.log.Info("claim not updated within the stale-claim age", attrs...)
	}
	return res, nil
}
