package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/sync"
)

// Tests for the per-run record (bead pg2-dpml1): one structured line per run,
// success or failure, carrying the change kind, duration, whether the gathered
// facts changed and whether the anchor bead was written.

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

// recordPipeline is a pipeline whose gather returns facts (with an as_of that
// advances every call, as a real re-read does) and whose record writer is rec.
func recordPipeline(t *testing.T, rec *bytes.Buffer, pr map[string]any, syncFn syncerFunc) (*Pipeline, *int) {
	t.Helper()
	calls := 0
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		calls++
		f := minimalFacts(t, pr)
		f.AsOf = "2026-09-16T00:00:0" + string(rune('0'+calls)) + "Z"
		return f, nil
	}), &bytes.Buffer{}, WithRunRecordWriter(rec))
	if syncFn != nil {
		p.syncer = syncFn
	}
	return p, &calls
}

func TestRunRecord_SuccessAppendsOneFullRecord(t *testing.T) {
	var rec bytes.Buffer
	p, _ := recordPipeline(t, &rec, map[string]any{"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 42}, nil)

	if err := p.Run(context.Background(), "pr", "42", gather.ChangeChanged); err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := recordLines(t, &rec)
	if len(lines) != 1 {
		t.Fatalf("want exactly one record per run, got %d: %s", len(lines), rec.String())
	}
	l := lines[0]
	if l.EntityType != "pr" || l.EntityID != "42" || l.Repo != "acme/widgets" || l.PR != 42 {
		t.Errorf("entity fields = %+v", l)
	}
	if l.Change != "changed" || l.Path != "full" || l.Outcome != "ok" {
		t.Errorf("change/path/outcome = %q/%q/%q", l.Change, l.Path, l.Outcome)
	}
	if !l.ContentHashChanged {
		t.Errorf("first gather of an entity must count as content_hash_changed")
	}
	if l.AnchorWritten || l.AnchorCause != "" {
		t.Errorf("no anchor write happened, got written=%v cause=%q", l.AnchorWritten, l.AnchorCause)
	}
	if l.Ts != "2026-09-16T12:00:00Z" {
		t.Errorf("ts = %q, want the pipeline clock's time", l.Ts)
	}
	// The raw line must carry every acceptance field even when false/zero.
	for _, key := range []string{`"duration_ms":`, `"content_hash_changed":`, `"anchor_written":`, `"change":`, `"entity_id":`} {
		if !strings.Contains(rec.String(), key) {
			t.Errorf("record missing %s: %s", key, rec.String())
		}
	}
}

// A sweep that finds nothing new is distinguishable from one that finds a
// change, even though gather stamps a different as_of each time.
func TestRunRecord_NoOpSweepIsDistinguishable(t *testing.T) {
	var rec bytes.Buffer
	pr := map[string]any{"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 7}
	p, _ := recordPipeline(t, &rec, pr, nil)

	if err := p.Run(context.Background(), "pr", "7", gather.ChangeAdded); err != nil {
		t.Fatalf("Run added: %v", err)
	}
	if err := p.Run(context.Background(), "pr", "7", gather.ChangeSweep); err != nil {
		t.Fatalf("Run sweep: %v", err)
	}
	pr["title"] = "fix: x, renamed"
	if err := p.Run(context.Background(), "pr", "7", gather.ChangeSweep); err != nil {
		t.Fatalf("Run sweep 2: %v", err)
	}

	lines := recordLines(t, &rec)
	if len(lines) != 3 {
		t.Fatalf("want 3 records, got %d", len(lines))
	}
	if !lines[0].ContentHashChanged {
		t.Errorf("added run: want content_hash_changed=true")
	}
	if lines[1].Change != "sweep" || lines[1].ContentHashChanged {
		t.Errorf("no-op sweep: change=%q content_hash_changed=%v, want sweep/false (as_of must not count)", lines[1].Change, lines[1].ContentHashChanged)
	}
	if lines[2].Change != "sweep" || !lines[2].ContentHashChanged {
		t.Errorf("sweep that caught a change: change=%q content_hash_changed=%v, want sweep/true", lines[2].Change, lines[2].ContentHashChanged)
	}
}

func TestRunRecord_AnchorWriteIsRecordedAndJoinableByPR(t *testing.T) {
	var rec bytes.Buffer
	p, _ := recordPipeline(t, &rec, map[string]any{"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 15}, syncerFunc(
		func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
			sync.RecordAnchorWrite(ctx, "pr-content-change")
			return nil
		},
	))

	// A qualified "<repo>#<n>" id joins the anchor-write log's numeric `pr`
	// field the same way a bare number does.
	if err := p.Run(context.Background(), "pr", "acme/widgets#15", gather.ChangeSweep); err != nil {
		t.Fatalf("Run: %v", err)
	}
	l := recordLines(t, &rec)[0]
	if !l.AnchorWritten || l.AnchorCause != "pr-content-change" {
		t.Errorf("anchor_written=%v anchor_cause=%q, want true/pr-content-change", l.AnchorWritten, l.AnchorCause)
	}
	if l.PR != 15 || l.Repo != "acme/widgets" {
		t.Errorf("join keys repo=%q pr=%d, want acme/widgets / 15", l.Repo, l.PR)
	}
}

func TestRunRecord_FailureAppendsOneRecordAndKeepsAnchorWrite(t *testing.T) {
	t.Run("gather failure", func(t *testing.T) {
		var rec bytes.Buffer
		p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
			return gather.Facts{}, errors.New("connector down")
		}), &bytes.Buffer{}, WithRunRecordWriter(&rec))
		if err := p.Run(context.Background(), "pr", "3", gather.ChangeSweep); err == nil {
			t.Fatal("Run: want error")
		}
		lines := recordLines(t, &rec)
		if len(lines) != 1 {
			t.Fatalf("want exactly one record, got %d", len(lines))
		}
		l := lines[0]
		if l.Outcome != "error" || l.Stage != StageGather || l.Change != "sweep" || l.PR != 3 {
			t.Errorf("failure record = %+v", l)
		}
		if l.ContentHashChanged || l.AnchorWritten {
			t.Errorf("a run that never gathered must report false/false, got %v/%v", l.ContentHashChanged, l.AnchorWritten)
		}
	})

	t.Run("sync failure after an anchor write", func(t *testing.T) {
		var rec bytes.Buffer
		p, _ := recordPipeline(t, &rec, map[string]any{"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 4}, syncerFunc(
			func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
				sync.RecordAnchorWrite(ctx, "created")
				return errors.New("review bead create failed")
			},
		))
		if err := p.Run(context.Background(), "pr", "4", gather.ChangeChanged); err == nil {
			t.Fatal("Run: want error")
		}
		lines := recordLines(t, &rec)
		if len(lines) != 1 {
			t.Fatalf("want exactly one record, got %d", len(lines))
		}
		l := lines[0]
		if l.Outcome != "error" || l.Stage != StageSync || !l.AnchorWritten || l.AnchorCause != "created" || !l.ContentHashChanged {
			t.Errorf("sync-failure record = %+v", l)
		}
	})
}

func TestRunRecord_InterpretOnlyRecordsPathAndNoChange(t *testing.T) {
	var rec bytes.Buffer
	p, _ := recordPipeline(t, &rec, map[string]any{"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 9}, nil)
	if err := p.Run(context.Background(), "pr", "9", gather.ChangeAdded); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rec.Reset()

	if err := p.RunInterpretOnly(context.Background(), "pr", "9", gather.ChangeChanged); err != nil {
		t.Fatalf("RunInterpretOnly: %v", err)
	}
	lines := recordLines(t, &rec)
	if len(lines) != 1 {
		t.Fatalf("want exactly one record, got %d", len(lines))
	}
	l := lines[0]
	if l.Path != "interpret_only" || l.Change != "changed" || l.ContentHashChanged || l.AnchorWritten || l.PR != 9 {
		t.Errorf("interpret-only record = %+v", l)
	}
}

func TestRunRecord_NoRecordWriterStillLogsToLogWriter(t *testing.T) {
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return minimalFacts(t, map[string]any{"author": "me", "title": "x", "repo": "acme/widgets", "number": 1}), nil
	}), &out)
	if err := p.Run(context.Background(), "pr", "1", gather.ChangeAdded); err != nil {
		t.Fatalf("Run: %v", err)
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

func TestContentSignature_IgnoresNestedAsOf(t *testing.T) {
	a := gather.Facts{PRShow: []byte(`{"title":"t","as_of":"2026-01-01T00:00:00Z"}`), AsOf: "2026-01-01T00:00:00Z", HeadSHA: "h"}
	b := gather.Facts{PRShow: []byte(`{"as_of":"2026-02-02T00:00:00Z","title":"t"}`), AsOf: "2026-02-02T00:00:00Z", HeadSHA: "h"}
	c := gather.Facts{PRShow: []byte(`{"title":"u","as_of":"2026-01-01T00:00:00Z"}`), AsOf: "2026-01-01T00:00:00Z", HeadSHA: "h"}
	if contentSignature(a) != contentSignature(b) {
		t.Error("signature must ignore as_of and key order")
	}
	if contentSignature(a) == contentSignature(c) {
		t.Error("signature must change when real content changes")
	}
}
