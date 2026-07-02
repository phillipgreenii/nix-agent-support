package usage

import (
	"sort"
	"time"
)

// blockDuration is ccusage's fixed 5-hour billing-block width.
const blockDuration = 5 * time.Hour

// Record is one priced usage event extracted from a transcript: when it
// happened, which model produced it, and its token counts. It is the input to
// the native block windowing (ADR 0021 §3 native CostPricer).
type Record struct {
	Timestamp time.Time
	Model     string
	Tokens    ModelTokens
}

// block accumulates records that fall within one 5h window.
type block struct {
	start   time.Time
	end     time.Time // last record's timestamp within the window
	tokens  map[string]ModelTokens
	perTok  ModelTokens // aggregate (for the DTO tokenCounts)
	records int
}

// ActiveBlock groups records into 5-hour blocks the way ccusage does — the
// first record floored to the hour anchors a block; records within 5h of that
// anchor join it; a record beyond the anchor+5h starts a new block. It then
// returns the block whose [start, start+5h) window contains now, priced per
// model via prices. Returns nil when there are no records or now is past the
// last block's window (ccusage isActive=false).
func ActiveBlock(records []Record, prices PriceTable, now time.Time) *Block {
	if len(records) == 0 {
		return nil
	}
	// Sort chronologically; windowing depends on order.
	sorted := make([]Record, len(records))
	copy(sorted, records)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Timestamp.Before(sorted[j].Timestamp) })

	var blocks []*block
	var cur *block
	for _, r := range sorted {
		if cur == nil || r.Timestamp.Sub(cur.start) >= blockDuration {
			cur = &block{
				start:  r.Timestamp.Truncate(time.Hour),
				tokens: map[string]ModelTokens{},
			}
			blocks = append(blocks, cur)
		}
		mt := cur.tokens[r.Model]
		mt.Input += r.Tokens.Input
		mt.Output += r.Tokens.Output
		mt.CacheCreation += r.Tokens.CacheCreation
		mt.CacheRead += r.Tokens.CacheRead
		cur.tokens[r.Model] = mt
		cur.perTok.Input += r.Tokens.Input
		cur.perTok.Output += r.Tokens.Output
		cur.perTok.CacheCreation += r.Tokens.CacheCreation
		cur.perTok.CacheRead += r.Tokens.CacheRead
		cur.records++
		cur.end = r.Timestamp
	}

	last := blocks[len(blocks)-1]
	// Active iff now is within the last block's 5h window (ccusage semantics:
	// the block is active until start+5h). now before start can happen with a
	// clock skew; treat only the in-window case as active.
	if now.Before(last.start) || now.Sub(last.start) >= blockDuration {
		return nil
	}

	var cost float64
	for model, tok := range last.tokens {
		c, _ := prices.Cost(model, tok)
		cost += c
	}
	return &Block{
		ID:        last.start.UTC().Format("2006-01-02T15Z"),
		StartTime: last.start,
		EndTime:   last.start.Add(blockDuration),
		IsActive:  true,
		CostUSD:   cost,
	}
}
