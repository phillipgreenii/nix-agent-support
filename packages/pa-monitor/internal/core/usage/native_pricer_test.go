package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNativePricerActiveBlockFromTranscripts proves the native CostPricer walks
// ~/.claude/projects/**/*.jsonl, extracts priced usage records, windows them,
// and returns the active block priced per model — the production ActiveBlock
// path of the CostPricer port (ADR 0021 §3). It excludes *.status.jsonl sibling
// files (Phase 3) so they never inflate cost.
func TestNativePricerActiveBlockFromTranscripts(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-tmp-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 4, 23, 19, 0, 0, 0, time.UTC)
	ts := base.Format(time.RFC3339)
	// One transcript, two records, one model.
	transcript := `{"type":"assistant","timestamp":"` + ts + `","message":{"model":"claude-opus-4-7","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1000000}}}
{"type":"assistant","timestamp":"` + ts + `","message":{"model":"claude-opus-4-7","usage":{"output_tokens":1000000}}}
`
	if err := os.WriteFile(filepath.Join(projDir, "sess.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	// A sibling status file that must be ignored (would otherwise fail to parse
	// or, worse, be counted).
	if err := os.WriteFile(filepath.Join(projDir, "sess.status.jsonl"), []byte(`{"ts":1,"five_hour_pct":42}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &NativePricer{ClaudeHome: home, Prices: stdPrices, Now: func() time.Time { return base.Add(time.Hour) }}
	block, err := p.ActiveBlock(context.Background())
	if err != nil {
		t.Fatalf("ActiveBlock: %v", err)
	}
	if block == nil {
		t.Fatal("ActiveBlock = nil, want active block from transcript")
	}
	// 2_000_000 output @ $25/MTok = $50.00
	if diff := block.CostUSD - 50.0; diff > epsilon || diff < -epsilon {
		t.Errorf("CostUSD = %.4f, want 50.00 (two 1M-output opus records)", block.CostUSD)
	}
	if probed, err := p.Probed(); !probed || err != nil {
		t.Errorf("Probed() = (%v,%v), want (true,nil) after a successful scan", probed, err)
	}
}

// TestNativePricerNoTranscripts proves an empty claude-home yields no active
// block and a clean probe (no error).
func TestNativePricerNoTranscripts(t *testing.T) {
	home := t.TempDir()
	p := &NativePricer{ClaudeHome: home, Prices: stdPrices, Now: time.Now}
	block, err := p.ActiveBlock(context.Background())
	if err != nil {
		t.Fatalf("ActiveBlock: %v", err)
	}
	if block != nil {
		t.Errorf("ActiveBlock = %+v, want nil (no transcripts)", block)
	}
}
