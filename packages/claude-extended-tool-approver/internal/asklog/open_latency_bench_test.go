//go:build asklogbench

package asklog

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestOpenInsertLatency measures the hook's per-call log cost: NewStore, one
// RecordPreToolDecision insert, Close. Opt-in (build tag asklogbench) because
// it is a measurement, not a unit test. CETA_BENCH_DB names a database COPY to
// measure (never the live one); CETA_BENCH_N is the sample count.
func TestOpenInsertLatency(t *testing.T) {
	path := os.Getenv("CETA_BENCH_DB")
	if path == "" {
		t.Skip("CETA_BENCH_DB not set")
	}
	n := 30
	if v, err := strconv.Atoi(os.Getenv("CETA_BENCH_N")); err == nil && v > 0 {
		n = v
	}
	defer SetSynchronousForTests(SetSynchronousForTests(""))
	var open, ins, cls []time.Duration
	for i := 0; i < n; i++ {
		t0 := time.Now()
		s, err := NewStore(path)
		if err != nil {
			t.Fatal(err)
		}
		t1 := time.Now()
		in := &hookio.HookInput{SessionID: "bench", CWD: "/tmp", ToolName: "Bash", ToolUseID: fmt.Sprintf("bench-%d-%d", time.Now().UnixNano(), i), ToolInput: json.RawMessage(`{"command":"date"}`)}
		if err := RecordPreToolDecision(s, in, hookio.RuleResult{Decision: hookio.Approve, Reason: "bench"}); err != nil {
			t.Fatal(err)
		}
		t2 := time.Now()
		_ = s.Close()
		t3 := time.Now()
		open = append(open, t1.Sub(t0))
		ins = append(ins, t2.Sub(t1))
		cls = append(cls, t3.Sub(t2))
	}
	rep := func(name string, d []time.Duration) {
		sort.Slice(d, func(a, b int) bool { return d[a] < d[b] })
		t.Logf("%s n=%d p50=%v p90=%v max=%v", name, len(d), d[len(d)/2], d[len(d)*9/10], d[len(d)-1])
	}
	rep("open ", open)
	rep("insert", ins)
	rep("close ", cls)
}

// TestLockedWriteLatency measures a log write while another connection holds
// the write lock: before (plain insert, waits out busy_timeout) and after
// (RunWithin budget, then AppendToSpool).
func TestLockedWriteLatency(t *testing.T) {
	path := os.Getenv("CETA_BENCH_DB")
	if path == "" {
		t.Skip("CETA_BENCH_DB not set")
	}
	defer SetSynchronousForTests(SetSynchronousForTests(""))
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	l, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	tx, err := l.db.Begin() // BEGIN IMMEDIATE (txlock=immediate)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	in := &hookio.HookInput{SessionID: "bench", CWD: "/tmp", ToolName: "Bash", ToolUseID: "bench-lock", ToolInput: json.RawMessage(`{"command":"date"}`)}
	res := hookio.RuleResult{Decision: hookio.Approve}
	t0 := time.Now()
	err = RecordPreToolDecision(s, in, res)
	t.Logf("before: plain insert under lock returned after %v (err=%v)", time.Since(t0), err)
	t0 = time.Now()
	err = RunWithin(1500*time.Millisecond, func() error { return RecordPreToolDecision(s, in, res) })
	if err != nil {
		err = AppendToSpool(path, in, res, nil, "bench")
	}
	t.Logf("after: bounded write + spool returned after %v (spool err=%v)", time.Since(t0), err)
}
