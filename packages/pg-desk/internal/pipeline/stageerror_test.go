package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/sync"
)

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"canceled", fmt.Errorf("gather: %w", context.Canceled), ClassCanceled},
		{"deadline", fmt.Errorf("gather: %w", context.DeadlineExceeded), ClassDeadline},
		{"signal killed text", errors.New("exec pg-connector [pr show]: signal: killed"), ClassKilled},
		{"exit -1 from a killed child", errors.New("pg-connector [pr show 1]: exit -1: "), ClassKilled},
		{"sqlite locked", errors.New("upsert entity: database is locked"), ClassStoreBusy},
		{"sqlite busy code", errors.New("SQLITE_BUSY (5)"), ClassStoreBusy},
		{"connector error", fmt.Errorf("sync: %w", &sync.ConnectorError{Code: "unavailable"}), ClassConnector},
		{"plain", errors.New("boom"), ClassError},
		{"exit 1 is not a kill", errors.New("pg-connector [pr show 1]: exit 1: boom"), ClassError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyError(tc.err); got != tc.want {
				t.Fatalf("ClassifyError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestTagStage_KeepsMessageAndFirstStage(t *testing.T) {
	base := errors.New("boom")
	tagged := TagStage(StageGather, base)
	if tagged.Error() != "boom" {
		t.Fatalf("tagged message = %q, want the wrapped message unchanged", tagged.Error())
	}
	if !errors.Is(tagged, base) {
		t.Fatal("tagged error must unwrap to the original")
	}
	if got := TagStage(StageStore, tagged); got != tagged {
		t.Fatal("re-tagging an already-tagged error must keep the first (innermost) tag")
	}
	if stage, class := StageOf(fmt.Errorf("outer: %w", tagged)); stage != StageGather || class != ClassError {
		t.Fatalf("StageOf = (%q, %q), want (%q, %q)", stage, class, StageGather, ClassError)
	}
	if TagStage(StageGather, nil) != nil {
		t.Fatal("TagStage(nil) must stay nil")
	}
	if stage, class := StageOf(base); stage != "" || class != "" {
		t.Fatalf("StageOf(untagged) = (%q, %q), want empty", stage, class)
	}
}

// failureLine decodes the single "error" outcome line from a run's log
// output.
func failureLine(t *testing.T, out *bytes.Buffer) runLogLine {
	t.Helper()
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var line runLogLine
		if err := json.Unmarshal([]byte(l), &line); err != nil {
			t.Fatalf("log line %q is not JSON: %v", l, err)
		}
		if line.Outcome == "error" {
			return line
		}
	}
	t.Fatalf("no error-outcome log line in: %s", out.String())
	return runLogLine{}
}

// TestPipelineRun_FailureIsStageTagged proves each failing stage of a PR run
// reports its stage and failure class on the structured log line AND in the
// returned error chain, while the exit-code contract (non-nil error) and the
// message text are unchanged.
func TestPipelineRun_FailureIsStageTagged(t *testing.T) {
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "x", "repo": "acme/widgets", "number": 21})

	t.Run("gather killed", func(t *testing.T) {
		var out bytes.Buffer
		p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
			return gather.Facts{}, errors.New("gather: fetch triggering PR 21: pg-connector [pr show 21]: exit -1: ")
		}), &out)
		err := p.Run(context.Background(), "pr", "21", gather.ChangeChanged)
		if err == nil {
			t.Fatal("Run: expected an error")
		}
		if stage, class := StageOf(err); stage != StageGather || class != ClassKilled {
			t.Fatalf("StageOf(Run err) = (%q, %q), want (%q, %q)", stage, class, StageGather, ClassKilled)
		}
		line := failureLine(t, &out)
		if line.Stage != StageGather || line.ErrorClass != ClassKilled {
			t.Fatalf("log stage/class = (%q, %q), want (%q, %q)", line.Stage, line.ErrorClass, StageGather, ClassKilled)
		}
		if !strings.Contains(line.Error, "exit -1") {
			t.Fatalf("log error = %q, want the original message", line.Error)
		}
	})

	t.Run("gather canceled", func(t *testing.T) {
		var out bytes.Buffer
		p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
			return gather.Facts{}, fmt.Errorf("exec pg-connector: %w", context.Canceled)
		}), &out)
		err := p.Run(context.Background(), "pr", "21", gather.ChangeChanged)
		if stage, class := StageOf(err); stage != StageGather || class != ClassCanceled {
			t.Fatalf("StageOf = (%q, %q), want (%q, %q)", stage, class, StageGather, ClassCanceled)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatal("the tag must not hide the wrapped context error")
		}
	})

	t.Run("store", func(t *testing.T) {
		var out bytes.Buffer
		p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
			return facts, nil
		}), &out)
		if err := p.store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		err := p.Run(context.Background(), "pr", "21", gather.ChangeAdded)
		if err == nil {
			t.Fatal("Run: expected an error")
		}
		if stage, _ := StageOf(err); stage != StageStore {
			t.Fatalf("stage = %q, want %q", stage, StageStore)
		}
		if line := failureLine(t, &out); line.Stage != StageStore || line.ErrorClass == "" {
			t.Fatalf("log stage/class = (%q, %q), want stage %q and a class", line.Stage, line.ErrorClass, StageStore)
		}
	})

	t.Run("sync", func(t *testing.T) {
		var out bytes.Buffer
		p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
			return facts, nil
		}), &out)
		syncErr := errors.New("sync: pg-connector issue create: signal: killed")
		p.syncer = syncerFunc(func(ctx context.Context, repo, entityID string, change gather.ChangeKind, f gather.Facts, interp interpret.Interpretation) error {
			return syncErr
		})
		err := p.Run(context.Background(), "pr", "21", gather.ChangeAdded)
		if !errors.Is(err, syncErr) {
			t.Fatalf("Run error should still wrap the sync error, got %v", err)
		}
		if stage, class := StageOf(err); stage != StageSync || class != ClassKilled {
			t.Fatalf("StageOf = (%q, %q), want (%q, %q)", stage, class, StageSync, ClassKilled)
		}
		if line := failureLine(t, &out); line.Stage != StageSync || line.ErrorClass != ClassKilled {
			t.Fatalf("log stage/class = (%q, %q), want (%q, %q)", line.Stage, line.ErrorClass, StageSync, ClassKilled)
		}
	})
}

// TestPipelineRun_SuccessLineCarriesNoStage proves the new fields are
// failure-only: a clean run's log line keeps its prior shape.
func TestPipelineRun_SuccessLineCarriesNoStage(t *testing.T) {
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "x", "repo": "acme/widgets", "number": 22})
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return facts, nil
	}), &out)
	if err := p.Run(context.Background(), "pr", "22", gather.ChangeAdded); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out.String(), `"stage"`) || strings.Contains(out.String(), `"error_class"`) {
		t.Fatalf("a successful run must not log stage/error_class, got: %s", out.String())
	}
}

func TestPipelineRunInterpretOnly_FailureIsStageTagged(t *testing.T) {
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return gather.Facts{}, nil
	}), &out)
	err := p.RunInterpretOnly(context.Background(), "pr", "never-gathered", gather.ChangeChanged)
	if err == nil {
		t.Fatal("expected an error")
	}
	if stage, _ := StageOf(err); stage != StageLoadFacts {
		t.Fatalf("stage = %q, want %q", stage, StageLoadFacts)
	}
	if line := failureLine(t, &out); line.Stage != StageLoadFacts {
		t.Fatalf("log stage = %q, want %q", line.Stage, StageLoadFacts)
	}
}
