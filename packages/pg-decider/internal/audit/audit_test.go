package audit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/item"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

var fixedNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func str(s string) *string { return &s }

// fake stands in for pg-connector and pg-desk. It records every exec in order
// and answers from Go; the child process is a tiny shell script that prints
// the canned stdout and exit code and reports the beads-dir variable it saw.
type fake struct {
	t        *testing.T
	dir      string
	execs    []string // "<binary> <args...>"
	beadsDir []string // per exec: "unset" or "set:<value>"
	respond  func(line string) (stdout string, exit int)
}

func newFake(t *testing.T, respond func(line string) (string, int)) *fake {
	t.Helper()
	return &fake{t: t, dir: t.TempDir(), respond: respond}
}

func (f *fake) factory() apply.CmdFactory {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := name + " " + strings.Join(args, " ")
		f.execs = append(f.execs, line)
		seen := filepath.Join(f.dir, fmt.Sprintf("env.%d", len(f.execs)))
		stdout, exit := f.respond(line)
		script := `if [ "${PG_CONNECTOR_ISSUE_BEADS_DIR+x}" = x ]; then printf 'set:%s' "$PG_CONNECTOR_ISSUE_BEADS_DIR" > "$SEEN"; else printf unset > "$SEEN"; fi; printf '%s' "$OUT"; exit "$CODE"`
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = append(os.Environ(), "SEEN="+seen, "OUT="+stdout, fmt.Sprintf("CODE=%d", exit))
		return cmd
	}
}

// comments returns the recorded pg-connector issue comment execs.
func (f *fake) comments() []string {
	var out []string
	for _, l := range f.execs {
		if strings.HasPrefix(l, "pg-connector issue comment ") {
			out = append(out, l)
		}
	}
	return out
}

// beadsDirFor returns the beads-dir observation of the first exec whose line
// has the prefix.
func (f *fake) beadsDirFor(prefix string) string {
	f.t.Helper()
	for i, l := range f.execs {
		if strings.HasPrefix(l, prefix) {
			b, err := os.ReadFile(filepath.Join(f.dir, fmt.Sprintf("env.%d", i+1)))
			if err != nil {
				f.t.Fatal(err)
			}
			return string(b)
		}
	}
	f.t.Fatalf("no exec with prefix %q in %q", prefix, f.execs)
	return ""
}

func envFor(f *fake, cfg *config.Config) apply.Env {
	if cfg == nil {
		cfg = &config.Config{}
	}
	var stderr bytes.Buffer
	return apply.Env{Command: f.factory(), Config: cfg, Clock: func() time.Time { return fixedNow }, Stderr: &stderr}
}

func TestBodyMatchesTheGoldenFile(t *testing.T) {
	ev := apply.Event{
		Action: action.Action{
			Op: action.OpCreate, Rule: "review.head-advanced",
			Facts: map[string]any{
				"unresolved": []string{"c1", "c2"}, "round": 2, "pr": "acme/widgets#42", "head_sha": "abc123",
			},
		},
		Outcome: apply.OutcomeApplied, WorkItemID: "wb-1", Seq: 42, HasSeq: true,
	}
	want, err := os.ReadFile(filepath.Join("testdata", "audit_comment.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got := Body(ev, fixedNow); got != string(want) {
		t.Fatalf("body:\n got %q\nwant %q", got, want)
	}
}

func TestBodyWithoutAFromItemStatesSeqNone(t *testing.T) {
	ev := apply.Event{Action: action.Action{Op: action.OpClose, Rule: "r.close"}, Outcome: apply.OutcomeApplied, WorkItemID: "wb-1"}
	got := Body(ev, fixedNow)
	if !strings.Contains(got, "\nseq: none\n") || !strings.Contains(got, "\nfacts: {}\n") {
		t.Fatalf("body %q", got)
	}
}

func TestBodyFactsAreRenderedWithSortedKeysAndNoHTMLEscaping(t *testing.T) {
	ev := apply.Event{Action: action.Action{Op: action.OpUpdate, Rule: "r", Facts: map[string]any{"b": "<x&y>", "a": 1}}, Outcome: apply.OutcomeApplied, WorkItemID: "wb-1"}
	if got := Body(ev, fixedNow); !strings.Contains(got, `facts: {"a":1,"b":"<x&y>"}`+"\n") {
		t.Fatalf("body %q", got)
	}
}

func TestBodyTimestampIsRFC3339UTCEvenForAZonedClock(t *testing.T) {
	zoned := time.Date(2026, 10, 5, 8, 0, 0, 0, time.FixedZone("EDT", -4*3600))
	ev := apply.Event{Action: action.Action{Op: action.OpClose, Rule: "r"}, Outcome: apply.OutcomeApplied, WorkItemID: "wb-1"}
	if got := Body(ev, zoned); !strings.HasSuffix(got, "at: 2026-10-05T12:00:00Z\n") {
		t.Fatalf("body %q", got)
	}
}

// actions is one of each applied external op, a deduped create, a failed
// update and an annotate.
func mixedActions() []action.Action {
	return []action.Action{
		{
			Op: action.OpCreate, Kind: "review-pr", Rule: "r.create", Facts: map[string]any{"k": "create"},
			Fields: action.Fields{Title: "t", Metadata: map[string]string{"dedup_key": "k1"}},
		},
		{Op: action.OpUpdate, Rule: "r.update", Target: str("wb-u"), Facts: map[string]any{"k": "update"}, Fields: action.Fields{Title: "new"}},
		{Op: action.OpReopen, Rule: "r.reopen", Target: str("wb-r"), Facts: map[string]any{"k": "reopen"}},
		{Op: action.OpClose, Rule: "r.close", Target: str("wb-c"), Facts: map[string]any{"k": "close"}},
		{Op: action.OpCreate, Kind: "review-pr", Rule: "r.dup", Fields: action.Fields{Title: "t2", Metadata: map[string]string{"dedup_key": "k2"}}},
		{Op: action.OpUpdate, Rule: "r.fail", Target: str("wb-fail"), Fields: action.Fields{Title: "x"}},
		{Op: action.OpAnnotate, Rule: "r.note", Target: str("some_key"), Fields: action.Fields{Value: str("v")}},
	}
}

func mixedResponder(failComment bool) func(string) (string, int) {
	return func(line string) (string, int) {
		switch {
		case strings.HasPrefix(line, "pg-connector issue list"):
			return `{"entities":[{"id":"wb-existing","metadata":{"dedup_key":"k2"}}]}`, 0
		case strings.HasPrefix(line, "pg-connector issue create"):
			return `{"result":{"id":"wb-new"}}`, 0
		case strings.HasPrefix(line, "pg-connector issue update wb-fail"):
			return `{"error":{"code":"unavailable","message":"down"}}`, 1
		case failComment && strings.HasPrefix(line, "pg-connector issue comment"):
			return `{"error":{"code":"unavailable","message":"comment down"}}`, 1
		}
		return `{"result":{}}`, 0
	}
}

func runMixed(t *testing.T, f *fake, cfg *config.Config, it *item.Routed) (apply.Result, string) {
	t.Helper()
	env := envFor(f, cfg)
	res := apply.Run(context.Background(), apply.Input{
		Type: "pr", ID: "acme/widgets#42",
		View:    &view.View{Type: "pr", ID: "acme/widgets#42"},
		Actions: mixedActions(), Item: it, Env: env, Hooks: []apply.Hook{New()},
	})
	return res, env.Stderr.(*bytes.Buffer).String()
}

func routed(seq int64) *item.Routed {
	it := &item.Routed{}
	it.Metadata.Seq = seq
	return it
}

func commentTarget(line string) string {
	f := strings.Fields(line)
	return f[3] // pg-connector issue comment <id>
}

func TestApplyWritesExactlyOneAuditComment(t *testing.T) {
	f := newFake(t, mixedResponder(false))
	res, stderr := runMixed(t, f, nil, routed(42))
	cs := f.comments()
	if len(cs) != 4 {
		t.Fatalf("want 4 comments, got %d: %q", len(cs), cs)
	}
	var targets []string
	for _, c := range cs {
		targets = append(targets, commentTarget(c))
	}
	if want := []string{"wb-new", "wb-u", "wb-r", "wb-c"}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("comment targets %q want %q", targets, want)
	}
	for i, rule := range []string{"r.create", "r.update", "r.reopen", "r.close"} {
		for _, must := range []string{"rule: " + rule, "seq: 42", "at: 2026-10-05T12:00:00Z", `facts: {"k":"`} {
			if !strings.Contains(cs[i], must) {
				t.Errorf("comment %d lacks %q: %q", i, must, cs[i])
			}
		}
	}
	// The failed update is the only reason for exit 2; no comment mentions it.
	if res.ExitCode != 2 || strings.Contains(stderr, "audit") {
		t.Fatalf("exit %d stderr %q", res.ExitCode, stderr)
	}
	for _, c := range cs {
		for _, other := range []string{"r.dup", "r.fail", "r.note"} {
			if strings.Contains(c, other) {
				t.Errorf("comment for a non-applied action %s: %q", other, c)
			}
		}
	}
}

func TestApplyWithoutFromItemWritesSeqNone(t *testing.T) {
	f := newFake(t, mixedResponder(false))
	runMixed(t, f, nil, nil)
	cs := f.comments()
	if len(cs) != 4 {
		t.Fatalf("comments: %q", cs)
	}
	for _, c := range cs {
		if !strings.Contains(c, "seq: none") {
			t.Errorf("comment lacks seq: none: %q", c)
		}
	}
}

func TestAFailedAuditCommentKeepsTheWriteReportsOnStderrAndExits2(t *testing.T) {
	f := newFake(t, func(line string) (string, int) {
		if strings.HasPrefix(line, "pg-connector issue comment") {
			return `{"error":{"code":"unavailable","message":"comment down"}}`, 1
		}
		return `{"result":{}}`, 0
	})
	env := envFor(f, nil)
	res := apply.Run(context.Background(), apply.Input{
		Type: "pr", ID: "x", Actions: []action.Action{{Op: action.OpClose, Rule: "r.close", Target: str("wb-c")}},
		Item: routed(7), Env: env, Hooks: []apply.Hook{New()},
	})
	stderr := env.Stderr.(*bytes.Buffer).String()
	if res.ExitCode != 2 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	if !strings.Contains(stderr, "audit comment") || !strings.Contains(stderr, "wb-c") || !strings.Contains(stderr, "r.close") {
		t.Fatalf("stderr %q", stderr)
	}
	// The action's own write stands: it was applied, not failed, and was not rolled back.
	if res.Events[0].Outcome != apply.OutcomeApplied {
		t.Fatalf("outcome %s", res.Events[0].Outcome)
	}
	// Exactly the write, its refresh and the (failed) comment: nothing undoes the write.
	if len(f.execs) != 3 || !strings.HasPrefix(f.execs[0], "pg-connector issue close wb-c") || !strings.HasPrefix(f.execs[2], "pg-connector issue comment wb-c") {
		t.Fatalf("execs %q", f.execs)
	}
}

func TestCommentCarriesTheBackendFlagAndBeadsDirLikeOtherIssueExecs(t *testing.T) {
	f := newFake(t, mixedResponder(false))
	cfg := &config.Config{AgentTrackerBackend: "trk", BeadsDir: "/tmp/beads"}
	runMixed(t, f, cfg, routed(1))
	for _, c := range f.comments() {
		if !strings.HasSuffix(c, " --backend trk") {
			t.Errorf("comment lacks --backend: %q", c)
		}
	}
	for _, prefix := range []string{"pg-connector issue comment", "pg-connector issue close"} {
		if got := f.beadsDirFor(prefix); got != "set:/tmp/beads" {
			t.Errorf("%s saw beads dir %q", prefix, got)
		}
	}
}

func TestCommentOmitsBackendAndBeadsDirWhenUnconfigured(t *testing.T) {
	t.Setenv("PG_CONNECTOR_ISSUE_BEADS_DIR", "")
	if err := os.Unsetenv("PG_CONNECTOR_ISSUE_BEADS_DIR"); err != nil {
		t.Fatal(err)
	}
	f := newFake(t, mixedResponder(false))
	runMixed(t, f, nil, routed(1))
	if got := f.beadsDirFor("pg-connector issue comment"); got != "unset" {
		t.Fatalf("beads dir %q", got)
	}
	if strings.Contains(f.comments()[0], "--backend") {
		t.Fatalf("comment: %q", f.comments()[0])
	}
}

func TestFinishDoesNothing(t *testing.T) {
	f := newFake(t, mixedResponder(false))
	if err := New().Finish(context.Background(), envFor(f, nil), []apply.Event{{Outcome: apply.OutcomeApplied}}); err != nil || len(f.execs) != 0 {
		t.Fatalf("err %v execs %q", err, f.execs)
	}
}

// A focus hold or release the live re-read abandons is skipped-stale: it writes
// nothing, so it posts no audit comment (INV-DECIDER-16), while an applied
// focus hold posts exactly one.
func TestSkippedStaleFocusHoldPostsNoComment(t *testing.T) {
	respond := func(line string) (string, int) {
		if strings.HasPrefix(line, "pg-connector issue show bd-f --fresh") {
			return `{"result":{"id":"bd-f","state":"open","assignee":"worker-1","served_from":"origin","stale":false}}`, 0
		}
		return `{"result":{}}`, 0
	}
	hold := action.Action{
		Op: action.OpUpdate, Kind: "focus-item", Target: str("bd-f"), Rule: "focus.item",
		Fields: action.Fields{Status: "deferred", Metadata: map[string]string{"focus_hold": "struck"}},
		Facts:  map[string]any{"transition": "hold"},
	}
	f := newFake(t, respond)
	res := apply.Run(context.Background(), apply.Input{
		Type: "pr", ID: "acme/widgets#42", View: &view.View{Type: "pr", ID: "acme/widgets#42"},
		Actions: []action.Action{hold}, Env: envFor(f, nil), Hooks: []apply.Hook{New()},
	})
	if len(res.Events) != 1 || res.Events[0].Outcome != apply.OutcomeSkippedStale || res.ExitCode != 0 {
		t.Fatalf("result %+v", res)
	}
	if cs := f.comments(); len(cs) != 0 {
		t.Fatalf("an abandoned write must post no comment: %q", cs)
	}
}
