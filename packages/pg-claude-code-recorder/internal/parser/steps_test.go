package parser_test

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// tickClock is a fake step clock: every read advances it by one millisecond, so
// the span between two consecutive reads is exactly one tick.
type tickClock struct{ n atomic.Int64 }

func (c *tickClock) now() time.Duration { return time.Duration(c.n.Add(1)) * time.Millisecond }

func rebuildKit(t *testing.T, files, repeat int, clock func() time.Duration) (steps store.StepStats, wall time.Duration, slow int, k interface{ Count(string, ...any) int }) {
	t.Helper()
	kit := testkit.New(t)
	for i := 0; i < files; i++ {
		body := strings.ReplaceAll(transcriptFixture, "example-session-1", fmt.Sprintf("example-session-%d", i))
		kit.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", i), strings.Repeat(body, repeat))
	}
	kit.StoreOptions = store.Options{Rebuild: true, BulkRebuild: true, StepClock: clock}
	kit.Reopen()
	t0 := time.Now()
	kit.Parse()
	wall = time.Since(t0)
	return kit.Store.Steps(), wall, len(kit.Parser.SlowestFiles()), kit
}

// A fake clock drives each step: a span of two consecutive reads is one tick,
// so each step's seconds are its tick count.
func TestStepTimersWithFakeClock(t *testing.T) {
	const files = 6
	c := &tickClock{}
	s, _, slow, k := rebuildKit(t, files, 1, c.now)
	ms := int64(time.Millisecond)

	lines := int64(k.Count(`SELECT COUNT(*) FROM records`))
	if lines == 0 || s.Records != lines {
		t.Fatalf("records inserted %d, table holds %d", s.Records, lines)
	}
	// parse: two reads per line, one tick between them.
	if s.StepCalls[store.StepParse] != s.Lines || s.StepNS[store.StepParse] != s.Lines*ms {
		t.Fatalf("parse: %d calls, %d ns, %d lines (want one tick per line)", s.StepCalls[store.StepParse], s.StepNS[store.StepParse], s.Lines)
	}
	// read: one tick per chunk read.
	if r := s.StepCalls[store.StepRead]; r < files || s.StepNS[store.StepRead] != r*ms {
		t.Fatalf("read: %d calls, %d ns", r, s.StepNS[store.StepRead])
	}
	// insert: one tick for the line plus two per sampled statement, so a whole
	// number of ticks and at least one per line.
	if in := s.StepNS[store.StepInsert]; in%ms != 0 || in < s.StepCalls[store.StepInsert]*ms || s.StepCalls[store.StepInsert] == 0 || s.StepCalls[store.StepInsert] > s.Lines {
		t.Fatalf("insert: %d ns over %d lines", in, s.Lines)
	}
	// savepoint: SAVEPOINT and RELEASE are one tick each, per write.
	if sp := s.StepCalls[store.StepSavepoint]; sp == 0 || s.StepNS[store.StepSavepoint] != sp*2*ms {
		t.Fatalf("savepoint: %d calls, %d ns (want two ticks per write)", sp, s.StepNS[store.StepSavepoint])
	}
	if s.StepCalls[store.StepCommit] == 0 {
		t.Fatal("no batch commit was counted")
	}
	if s.StepCalls[store.StepOther] != 1 {
		t.Fatalf("other is added once per ParseAll, got %d", s.StepCalls[store.StepOther])
	}
	// The sampled statements are scaled by 16 and counted for every table.
	var rows int64
	for i := range s.TableRows {
		rows += s.TableRows[i]
	}
	if s.TableRows[store.TableRecords] != lines || s.TableRows[store.TableFields] == 0 || rows == 0 {
		t.Fatalf("table statements %v, records %d", s.TableRows, lines)
	}
	var tbl int64
	for _, n := range s.TableNS {
		if n%(16*ms) != 0 {
			t.Fatalf("table seconds %v are not scaled ticks", s.TableNS)
		}
		tbl += n
	}
	if tbl == 0 {
		t.Fatal("no statement was sampled")
	}
	if slow == 0 || slow > 5 {
		t.Fatalf("slowest files: %d", slow)
	}
}

// On the real clock the six steps partition ParseAll: other is the wall time
// less the five measured steps, and it stays a small share of a fixture rebuild.
func TestStepsPartitionParseAll(t *testing.T) {
	s, wall, _, _ := rebuildKit(t, 4, 150, nil)
	var sum int64
	for i := range s.StepNS {
		sum += s.StepNS[i]
	}
	if sum > wall.Nanoseconds() || float64(sum) < 0.9*float64(wall) {
		t.Fatalf("steps sum to %v, ParseAll took %v", time.Duration(sum), wall)
	}
	other := float64(s.StepNS[store.StepOther]) / float64(sum)
	if other >= 0.15 {
		t.Fatalf("other is %.1f%% of the parse (steps %v)", 100*other, s.StepNS)
	}
}

// The counters reach the registry under the catalogued names.
func TestStepCountersReachRegistry(t *testing.T) {
	kit := bulkCorpus(t, 3)
	kit.StoreOptions = store.Options{Rebuild: true, BulkRebuild: true}
	kit.Reopen()
	kit.Parse()
	for _, st := range obs.Steps {
		if _, ok := kit.Obs.Reg.Counter(obs.MStepSeconds, map[string]string{"step": st}); !ok {
			t.Fatalf("no %s{step=%s}", obs.MStepSeconds, st)
		}
	}
	if v, _ := kit.Obs.Reg.Counter(obs.MStepCalls, map[string]string{"step": "parse"}); v == 0 {
		t.Fatal("step_calls_total{step=parse} is 0")
	}
}
