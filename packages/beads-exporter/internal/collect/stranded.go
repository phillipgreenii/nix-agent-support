package collect

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/failure"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
	"github.com/phillipgreenii/beads-exporter/internal/stranded"
)

// PassStranded is the stranded-claim pass name, also its pass label value.
const PassStranded = "stranded"

// StrandedEvent is the value of the event attribute on the one log line the
// pass writes per claim with no live owner.
const StrandedEvent = "stranded_claim"

// StrandedConfig configures the stranded pass.
type StrandedConfig struct {
	// ClaudeDir is the Claude data directory holding the session transcripts.
	ClaudeDir string
	// Window is the staleClaimHours window.
	Window time.Duration
	// OperatorNames are assignees judged by claim age rather than transcript.
	OperatorNames []string
	// Open overrides the transcript opener; nil means os.Open (tests only).
	Open stranded.Opener
}

// StrandedPass decides, for every claimed not-closed bead, whether its claim
// still has a live owner. It spawns its own guarded bd list per database (so
// the stale-JSONL guard and the timeout apply exactly as for the main pass and
// a failing main pass never lets it classify a stale list) and keeps one
// incremental transcript cache per database. It reports counts and timestamps
// as metrics and one log line per claim with no live owner; it never releases
// or changes a claim.
type StrandedPass struct {
	cfg StrandedConfig
	log *slog.Logger

	mu          sync.Mutex
	classifiers map[string]*stranded.Classifier
}

// NewStrandedPass builds the stranded pass.
func NewStrandedPass(cfg StrandedConfig, log *slog.Logger) *StrandedPass {
	return &StrandedPass{cfg: cfg, log: log, classifiers: map[string]*stranded.Classifier{}}
}

// Name implements Pass.
func (*StrandedPass) Name() string { return PassStranded }

// Reasons implements Pass: the bd-driven reasons plus transcript_error.
func (*StrandedPass) Reasons() []failure.Reason {
	return append(baseReasons(), failure.TranscriptError)
}

func (p *StrandedPass) classifier(db string) *stranded.Classifier {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.classifiers[db]
	if c == nil {
		c = stranded.NewClassifier(stranded.Config{
			ClaudeDir:     p.cfg.ClaudeDir,
			Window:        p.cfg.Window,
			OperatorNames: p.cfg.OperatorNames,
		}, p.cfg.Open)
		p.classifiers[db] = c
	}
	return c
}

// Collect implements Pass.
func (p *StrandedPass) Collect(ctx context.Context, env PassEnv) (metrics.Emitter, error) {
	beads, err := env.DB.Adapter.List(ctx, bd.ListOpts{})
	if err != nil {
		return nil, err
	}
	res, err := p.classifier(env.DB.Name).Classify(ctx, beads, env.Now)
	if err != nil {
		return nil, err
	}
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
		p.log.Info("claim has no live owner", attrs...)
	}
	return res, nil
}
