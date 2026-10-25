package changes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Config configures an Engine.
type Config struct {
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// SettleTimeout is how long a pending row waits for its dependents; zero
	// means config.DefaultSettleTimeout.
	SettleTimeout time.Duration
	// BodyCap is the byte cap of a text value recorded in a field; zero means
	// config.DefaultChangeBodyCap.
	BodyCap int
}

// Engine is the Jira binding of the shared change engine: the shared engine
// (whose methods it exposes) over the Jira registration and classifier, plus the
// one method whose arguments are Jira types. It is safe for concurrent use.
type Engine struct {
	*engine.Engine
}

// New builds an Engine from cfg over the daemon's registration (Spec), applying
// the defaults.
func New(cfg Config) (*Engine, error) {
	if cfg.SettleTimeout < 0 {
		return nil, fmt.Errorf("changes: settle timeout %s is negative", cfg.SettleTimeout)
	}
	if cfg.BodyCap < 0 {
		return nil, fmt.Errorf("changes: body cap %d is negative", cfg.BodyCap)
	}
	if cfg.SettleTimeout == 0 {
		cfg.SettleTimeout = config.DefaultSettleTimeout
	}
	if cfg.BodyCap == 0 {
		cfg.BodyCap = config.DefaultChangeBodyCap
	}
	e, err := engine.New(engine.Config{
		Spec: Spec(), Classifier: classifier{}, Clock: cfg.Clock,
		SettleTimeout: cfg.SettleTimeout, BodyCap: cfg.BodyCap,
	})
	if err != nil {
		return nil, err
	}
	return &Engine{Engine: e}, nil
}

// DetectSummary is the summary seam's body: it runs inside the transaction that
// stored next, after the rows are written. A nil prev, a first sighting and a
// gone summary are the baseline and add nothing. Both contents reach the engine
// in the groups.Stored form (normalised, Updated kept), so the classifier sees
// the revalidation token.
func (e *Engine) DetectSummary(ctx context.Context, tx *store.Tx, prev *groups.SummaryContent, next groups.SummaryContent, first bool, info engine.RefreshInfo) error {
	c := engine.PrimaryChange{ID: next.Issue.ID, Info: info}
	if prev != nil && !first && !next.Gone {
		var err error
		if c.Prev, err = json.Marshal(groups.Stored(*prev)); err != nil {
			return fmt.Errorf("changes: encode previous summary of %s: %w", c.ID, err)
		}
		if c.Next, err = json.Marshal(groups.Stored(next)); err != nil {
			return fmt.Errorf("changes: encode summary of %s: %w", c.ID, err)
		}
	}
	return e.Engine.DetectPrimary(ctx, tx, c)
}
