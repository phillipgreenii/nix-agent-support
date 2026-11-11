// Package consumption reads a session's cumulative resource consumption from
// its transcript: per-model token totals and an estimated cost in integer USD
// cents at API list prices. It is the ccpool-owned consumption meter; policy
// over the reading (ceilings, wind-down, stopping) belongs to the caller.
package consumption

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"

	ct "github.com/phillipgreenii/claude-transcript"
)

// Price is per-MTok USD pricing for one model.
type Price struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheWritePerMTok float64
	CacheReadPerMTok  float64
}

// PriceTable maps a model id prefix to its prices. The key DefaultKey is the
// fallback for a model matching no other key.
type PriceTable map[string]Price

// DefaultKey is the fallback price entry.
const DefaultKey = "_default"

// DefaultPrices is the built-in table (USD per MTok, API list prices, values
// current as of 2026-11; cache write is the 5-minute 1.25x input rate). It is data, not logic.
func DefaultPrices() PriceTable {
	return PriceTable{
		DefaultKey:          {InputPerMTok: 15, OutputPerMTok: 75, CacheWritePerMTok: 18.75, CacheReadPerMTok: 1.5},
		"claude-opus-5-5":   {InputPerMTok: 4, OutputPerMTok: 20, CacheWritePerMTok: 5, CacheReadPerMTok: 0.2},
		"claude-sonnet-5-5": {InputPerMTok: 2, OutputPerMTok: 10, CacheWritePerMTok: 2.5, CacheReadPerMTok: 0.2},
		"claude-opus-4-8":   {InputPerMTok: 15, OutputPerMTok: 75, CacheWritePerMTok: 18.75, CacheReadPerMTok: 1.5},
		"claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15, CacheWritePerMTok: 3.75, CacheReadPerMTok: 0.3},
		"claude-haiku-4-5":  {InputPerMTok: 1, OutputPerMTok: 5, CacheWritePerMTok: 1.25, CacheReadPerMTok: 0.1},
	}
}

// priceFor resolves a model's price by LONGEST table key that is a prefix of
// the model id (transcript ids carry date suffixes such as
// "claude-haiku-4-5-20251001"), falling back to DefaultKey.
func priceFor(model string, t PriceTable) Price {
	best := ""
	for k := range t {
		if k != DefaultKey && strings.HasPrefix(model, k) && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return t[best]
	}
	return t[DefaultKey]
}

// Tokens are cumulative token totals.
type Tokens struct {
	Input         int `json:"input_tokens"`
	Output        int `json:"output_tokens"`
	CacheCreation int `json:"cache_creation_tokens"`
	CacheRead     int `json:"cache_read_tokens"`
}

func (t Tokens) dollars(p Price) float64 {
	return float64(t.Input)/1e6*p.InputPerMTok +
		float64(t.Output)/1e6*p.OutputPerMTok +
		float64(t.CacheCreation)/1e6*p.CacheWritePerMTok +
		float64(t.CacheRead)/1e6*p.CacheReadPerMTok
}

// ModelUsage is one model's share of a session's consumption.
type ModelUsage struct {
	Model string `json:"model"`
	Tokens
	EstimatedCostCents int64 `json:"estimated_cost_cents"`
}

// Reading is a session's consumption at one instant.
type Reading struct {
	// Model is the last model seen ("" when no assistant turn exists).
	Model string `json:"model"`
	Tokens
	// EstimatedCostCents is an estimate at API list prices, summed over the
	// per-model costs and truncated toward zero.
	EstimatedCostCents int64        `json:"estimated_cost_cents"`
	Models             []ModelUsage `json:"models"`
}

// Read sums the assistant usage in the transcript at path. A missing or empty
// path (no transcript yet) reads as zeros, not an error. Each assistant message
// id is counted once (a turn is written as several lines repeating one
// cumulative usage; the largest value per component wins). A nil table means
// DefaultPrices.
func Read(path string, prices PriceTable) (Reading, error) {
	if prices == nil {
		prices = DefaultPrices()
	}
	r := Reading{Models: []ModelUsage{}}
	if path == "" {
		return r, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return r, nil
		}
		return r, err
	}
	defer func() { _ = f.Close() }()

	type msg struct {
		model string
		tok   Tokens
	}
	var order []string // message keys in first-seen order
	byID := map[string]*msg{}
	anon := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var ev ct.Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type != "assistant" {
			continue
		}
		key := ev.Message.ID
		if key == "" { // older transcripts: every line is its own turn
			anon++
			key = "\x00anon" + string(rune(anon))
		}
		m, ok := byID[key]
		if !ok {
			m = &msg{}
			byID[key] = m
			order = append(order, key)
		}
		u := ev.Message.Usage
		m.tok.Input = max(m.tok.Input, u.InputTokens)
		m.tok.Output = max(m.tok.Output, u.OutputTokens)
		m.tok.CacheCreation = max(m.tok.CacheCreation, u.CacheCreationInputTokens)
		m.tok.CacheRead = max(m.tok.CacheRead, u.CacheReadInputTokens)
		if ev.Message.Model != "" {
			m.model = ev.Message.Model
			r.Model = ev.Message.Model
		}
	}
	if err := sc.Err(); err != nil {
		return Reading{Models: []ModelUsage{}}, err
	}

	perModel := map[string]*Tokens{}
	for _, k := range order {
		m := byID[k]
		t := perModel[m.model]
		if t == nil {
			t = &Tokens{}
			perModel[m.model] = t
		}
		t.Input += m.tok.Input
		t.Output += m.tok.Output
		t.CacheCreation += m.tok.CacheCreation
		t.CacheRead += m.tok.CacheRead
	}
	names := make([]string, 0, len(perModel))
	for n := range perModel {
		names = append(names, n)
	}
	sort.Strings(names)
	var totalDollars float64
	for _, n := range names {
		t := *perModel[n]
		d := t.dollars(priceFor(n, prices))
		totalDollars += d
		r.Models = append(r.Models, ModelUsage{Model: n, Tokens: t, EstimatedCostCents: int64(d * 100)})
		r.Input += t.Input
		r.Output += t.Output
		r.CacheCreation += t.CacheCreation
		r.CacheRead += t.CacheRead
	}
	r.EstimatedCostCents = int64(totalDollars * 100)
	return r, nil
}
