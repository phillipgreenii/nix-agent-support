package parser

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

func TestCountMismatchesCountsPerSourceTypeField(t *testing.T) {
	p := New(Config{})
	r := source.Record{Type: "assistant", Mismatches: []string{"usage.output_tokens", "usage.output_tokens", "cwd"}}
	p.countMismatches("claude-transcript", r)
	p.countMismatches("claude-transcript", source.Record{Type: "user"})
	reg := p.cfg.Obs.Reg
	lbl := map[string]string{"source": "claude-transcript", "type": "assistant", "field": "usage.output_tokens"}
	if v, ok := reg.Counter(MFieldTypeMismatch, lbl); !ok || v != 2 {
		t.Fatalf("usage.output_tokens counter = %v, %v", v, ok)
	}
	lbl["field"] = "cwd"
	if v, ok := reg.Counter(MFieldTypeMismatch, lbl); !ok || v != 1 {
		t.Fatalf("cwd counter = %v, %v", v, ok)
	}
	if _, ok := reg.Counter(MFieldTypeMismatch, map[string]string{"source": "claude-transcript", "type": "user", "field": ""}); ok {
		t.Fatal("a record without mismatches must add no series")
	}
}
