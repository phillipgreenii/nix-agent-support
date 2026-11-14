package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
)

// Session-list observer and bounded hang diagnostic.
//
// listObserver wraps a role's ccpool.Runner. Every List call is counted on the
// pass counters carried by the context (calls, wall time, timeouts). When a call
// is still running after the diagnostic threshold, the observer samples the
// process tree below this process (`ps` plus, when available, a stack sample)
// and names which child is stuck, so a hang of `ccpool list` can be traced to
// tmux, git or the ccpool process itself (its own sqlite wait).
//
// The diagnostic is bounded three ways: a per-run sample cap, a total sample
// cap kept in a counter file, and an expiry date after which it does nothing.
// It can also be switched off.

const (
	// defaultListDiagThreshold is how long a list call runs before it is sampled.
	defaultListDiagThreshold = 15 * time.Second
	// listDiagMaxPerRun caps the samples one janitor run takes.
	listDiagMaxPerRun = 3
	// listDiagMaxTotal caps the samples taken over the life of the state dir.
	listDiagMaxTotal = 40
	// listDiagCountFile holds the total sample count, in the handler state dir.
	listDiagCountFile = "list-diag-count"
	// listDiagEvent is the events.jsonl kind of one sample.
	listDiagEvent = "list_hang_sample"
	// listDiagStackMax bounds the stack sample text kept in the event.
	listDiagStackMax = 6000
	// listDiagToolTimeout bounds each sampling command.
	listDiagToolTimeout = 5 * time.Second
)

// defaultListDiagExpiry is when the diagnostic stops by itself.
var defaultListDiagExpiry = time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)

// procRow is one line of `ps`.
type procRow struct {
	PID, PPID int
	State     string
	Elapsed   string
	Command   string
}

// listDiagSample is what one sample found.
type listDiagSample struct {
	// Stuck names the stuck component: tmux, git, ccpool-self (no child, so an
	// in-process wait such as sqlite) or none (no ccpool process was found).
	Stuck string `json:"stuck"`
	// Procs are the descendants of this process at sampling time.
	Procs []string `json:"procs"`
	// Stack is the stack sample of the ccpool process, when one could be taken.
	Stack string `json:"stack,omitempty"`
}

// listDiagger takes one sample; injectable for tests.
type listDiagger func(ctx context.Context, root int) listDiagSample

// listDiagConfig bounds and wires the diagnostic.
type listDiagConfig struct {
	Enabled   bool
	Threshold time.Duration
	Expiry    time.Time
	StateDir  string
	MaxPerRun int
	MaxTotal  int
	Now       func() time.Time
	Sample    listDiagger
	// Record publishes one sample (the events log in production).
	Record func(fields map[string]any)

	mu     sync.Mutex
	perRun int
}

func (c *listDiagConfig) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// admit reserves one sample under the three bounds and reports whether it may run.
func (c *listDiagConfig) admit() bool {
	if c == nil || !c.Enabled || c.now().After(c.Expiry) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.perRun >= c.MaxPerRun {
		return false
	}
	total := 0
	path := filepath.Join(c.StateDir, listDiagCountFile)
	if b, err := os.ReadFile(path); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && v > 0 {
			total = v
		}
	}
	if total >= c.MaxTotal {
		return false
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(total+1)+"\n"), 0o644); err != nil {
		return false // cannot keep the total, so do not sample
	}
	c.perRun++
	return true
}

// newListDiag builds the diagnostic config from the janitor options; its
// samples go to the events log.
func newListDiag(opts janitorOptions, stateDir string, log interface {
	Emit(level, kind, msg string, fields map[string]any) error
},
) *listDiagConfig {
	expiry := defaultListDiagExpiry
	if t, err := time.Parse(time.RFC3339, opts.listDiagUntil); err == nil {
		expiry = t
	}
	return &listDiagConfig{
		Enabled:   opts.listDiag,
		Threshold: opts.listDiagAfter,
		Expiry:    expiry,
		StateDir:  stateDir,
		MaxPerRun: listDiagMaxPerRun,
		MaxTotal:  listDiagMaxTotal,
		Sample:    sampleProcessTree,
		Record: func(fields map[string]any) {
			_ = log.Emit("warn", listDiagEvent, "ccpool list is slow; sampled the process tree", fields)
		},
	}
}

// listObserver is a ccpool.Runner whose List is counted and, when slow, sampled.
type listObserver struct {
	ccpool.Runner
	diag *listDiagConfig
}

func (o *listObserver) List(ctx context.Context) ([]ccpool.Session, error) {
	counts := countsFrom(ctx)
	start := time.Now()
	var wg sync.WaitGroup
	var timer *time.Timer
	if o.diag != nil && o.diag.Enabled && o.diag.Threshold > 0 {
		wg.Add(1)
		timer = time.AfterFunc(o.diag.Threshold, func() {
			defer wg.Done()
			o.sample(ctx, counts, start)
		})
	}
	sessions, err := o.Runner.List(ctx)
	if timer != nil && timer.Stop() {
		wg.Done() // never fired
	}
	wg.Wait() // a fired sample finishes before the call reports
	counts.listCall(time.Since(start), errors.Is(err, ccpool.ErrCallTimeout))
	return sessions, err
}

func (o *listObserver) sample(ctx context.Context, counts *passCounts, start time.Time) {
	d := o.diag
	if !d.admit() {
		return
	}
	s := d.Sample(ctx, os.Getpid())
	fields := map[string]any{
		"stuck":      s.Stuck,
		"waited_ms":  time.Since(start).Milliseconds(),
		"procs":      s.Procs,
		"stack":      s.Stack,
		"expires_at": d.Expiry.Format(time.RFC3339),
	}
	if counts != nil {
		fields["pass"], fields["role"], fields["pool"] = counts.Pass, counts.Role, counts.Pool
	}
	slog.Warn("janitor: ccpool list is slow", "stuck", s.Stuck, "waited_ms", fields["waited_ms"])
	if d.Record != nil {
		d.Record(fields)
	}
}

// parsePS parses `ps -A -o pid=,ppid=,state=,etime=,command=` output.
func parsePS(out string) []procRow {
	var rows []procRow
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		rows = append(rows, procRow{PID: pid, PPID: ppid, State: f[2], Elapsed: f[3], Command: strings.Join(f[4:], " ")})
	}
	return rows
}

// descendants returns the rows below root, parents before children.
func descendants(rows []procRow, root int) []procRow {
	kids := map[int][]procRow{}
	for _, r := range rows {
		kids[r.PPID] = append(kids[r.PPID], r)
	}
	var out []procRow
	queue := []int{root}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, k := range kids[p] {
			if seen[k.PID] {
				continue
			}
			seen[k.PID] = true
			out = append(out, k)
			queue = append(queue, k.PID)
		}
	}
	return out
}

func baseCmd(command string) string {
	first := strings.Fields(command)
	if len(first) == 0 {
		return ""
	}
	return filepath.Base(first[0])
}

// classifyStuck names what a hung `ccpool list` is waiting on from the process
// tree below it: the deepest tmux or git descendant, else the ccpool process
// itself (an in-process wait such as sqlite), else none. ccpoolPID is the pid of
// the ccpool process, 0 when none was found.
func classifyStuck(tree []procRow) (stuck string, ccpoolPID int) {
	for _, r := range tree {
		if baseCmd(r.Command) == "ccpool" {
			ccpoolPID = r.PID
			break
		}
	}
	if ccpoolPID == 0 {
		return "none", 0
	}
	below := descendants(tree, ccpoolPID)
	for i := len(below) - 1; i >= 0; i-- {
		switch b := baseCmd(below[i].Command); b {
		case "tmux", "git":
			return b, ccpoolPID
		}
	}
	return "ccpool-self", ccpoolPID
}

// sampleProcessTree is the production listDiagger: `ps`, classification, and a
// stack sample of the ccpool process (macOS `sample`, else skipped).
func sampleProcessTree(ctx context.Context, root int) listDiagSample {
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listDiagToolTimeout)
	defer cancel()
	out, err := exec.CommandContext(tctx, "ps", "-A", "-o", "pid=,ppid=,state=,etime=,command=").Output()
	if err != nil {
		return listDiagSample{Stuck: "none", Procs: []string{"ps failed: " + err.Error()}}
	}
	tree := descendants(parsePS(string(out)), root)
	stuck, pid := classifyStuck(tree)
	s := listDiagSample{Stuck: stuck}
	for _, r := range tree {
		s.Procs = append(s.Procs, fmt.Sprintf("%d ppid=%d %s %s %s", r.PID, r.PPID, r.State, r.Elapsed, truncate(r.Command, 200)))
	}
	if pid != 0 {
		if bin, err := exec.LookPath("sample"); err == nil {
			sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), listDiagToolTimeout)
			defer scancel()
			if b, err := exec.CommandContext(sctx, bin, strconv.Itoa(pid), "1").Output(); err == nil {
				s.Stack = truncate(string(b), listDiagStackMax)
			}
		}
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
