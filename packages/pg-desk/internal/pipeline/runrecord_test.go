package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// Tests for the per-run record (bead pg2-dpml1): one structured line per run,
// success or failure, carrying the change kind and duration.

// recordLines decodes every JSON line in buf.
func recordLines(t *testing.T, buf *bytes.Buffer) []runLogLine {
	t.Helper()
	var lines []runLogLine
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var l runLogLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("record line %q is not JSON: %v", raw, err)
		}
		lines = append(lines, l)
	}
	return lines
}

func TestRunRecord_InterpretOnlyRecordsPathAndChange(t *testing.T) {
	var rec bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		t.Fatal("gather was invoked by RunInterpretOnly")
		return gather.Facts{}, nil
	}), &bytes.Buffer{}, WithRunRecordWriter(&rec))
	seedInterpretOnlyEntity(t, p, "9", minimalFacts(t, map[string]any{"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 9}))

	if err := p.RunInterpretOnly(context.Background(), "pr", "9", gather.ChangeChanged); err != nil {
		t.Fatalf("RunInterpretOnly: %v", err)
	}
	lines := recordLines(t, &rec)
	if len(lines) != 1 {
		t.Fatalf("want exactly one record, got %d", len(lines))
	}
	l := lines[0]
	if l.Path != "interpret_only" || l.Change != "changed" || l.Outcome != "ok" || l.PR != 9 || l.Repo != "acme/widgets" {
		t.Errorf("interpret-only record = %+v", l)
	}
}

func TestRunRecord_NoRecordWriterStillLogsToLogWriter(t *testing.T) {
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return gather.Facts{}, nil
	}), &out)
	seedInterpretOnlyEntity(t, p, "1", minimalFacts(t, map[string]any{"author": "me", "title": "x", "repo": "acme/widgets", "number": 1}))
	if err := p.RunInterpretOnly(context.Background(), "pr", "1", gather.ChangeAdded); err != nil {
		t.Fatalf("RunInterpretOnly: %v", err)
	}
	if got := len(recordLines(t, &out)); got != 1 {
		t.Fatalf("log writer got %d lines, want 1", got)
	}
}

func TestPRNumberOf(t *testing.T) {
	for in, want := range map[string]int{"42": 42, "acme/widgets#15": 15, "": 0, "abc": 0, "x#": 0} {
		if got := prNumberOf(in); got != want {
			t.Errorf("prNumberOf(%q) = %d, want %d", in, got, want)
		}
	}
}
