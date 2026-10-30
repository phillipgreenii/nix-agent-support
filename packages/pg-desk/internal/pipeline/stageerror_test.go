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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
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

func TestPipelineRunInterpretOnly_FailureIsStageTagged(t *testing.T) {
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return gather.Facts{}, nil
	}), &out)
	// A never-gathered entity is a quiet no-op now (pg2-4i9ywp); a malformed
	// stored-facts row is the load_facts failure this test pins.
	if err := p.store.UpsertEntity(store.Entity{
		Repo: "acme/widgets", EntityType: "pr", EntityID: "bad-facts",
		Facts: "{not json", ContentHash: "fixture",
	}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	err := p.RunInterpretOnly(context.Background(), "pr", "bad-facts", gather.ChangeChanged)
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
