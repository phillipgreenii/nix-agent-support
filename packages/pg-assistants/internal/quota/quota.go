// Package quota turns an assistant's consumption over a rolling window into a
// meter for its ladder. The consumption comes from a Source (the recorder in
// production): per-model tokens of every transcript record whose cwd is under
// the assistant's directory. Tokens are summed over the four usage counters;
// dollars price each model's tokens with the built-in price table.
package quota

import (
	"context"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/ladder"
)

// Tokens are the four usage counters.
type Tokens struct{ Input, Output, CacheCreation, CacheRead int64 }

// Sum is all four counters.
func (t Tokens) Sum() int64 { return t.Input + t.Output + t.CacheCreation + t.CacheRead }

// Source reads per-model token totals for the records under dir at or after since.
type Source interface {
	Totals(ctx context.Context, dir string, since time.Time) (map[string]Tokens, error)
}

// Used returns the quota's consumption now: tokens or dollars, as the quota
// is configured.
func Used(totals map[string]Tokens, q config.Quota) float64 {
	if q.Tokens > 0 {
		var n int64
		for _, t := range totals {
			n += t.Sum()
		}
		return float64(n)
	}
	var usd float64
	for model, t := range totals {
		usd += Price(model).Cost(t)
	}
	return usd
}

// Reading is one evaluation of a quota.
type Reading struct {
	Used, Limit float64
	Meter       ladder.Meter
}

// Read queries src for the window ending at now and returns the meter.
func Read(ctx context.Context, src Source, dir string, q config.Quota, now time.Time) (Reading, error) {
	totals, err := src.Totals(ctx, dir, now.Add(-q.Window))
	if err != nil {
		return Reading{}, err
	}
	used := Used(totals, q)
	limit, detail := float64(q.Tokens), ""
	if q.Tokens > 0 {
		detail = fmt.Sprintf("%d of %d tokens in %s", int64(used), q.Tokens, q.Window)
	} else {
		limit = q.Dollars
		detail = fmt.Sprintf("$%.2f of $%.2f in %s", used, q.Dollars, q.Window)
	}
	return Reading{Used: used, Limit: limit, Meter: ladder.Meter{
		Name: "quota", Fraction: used / limit, Points: q.Points, Detail: detail,
	}}, nil
}
