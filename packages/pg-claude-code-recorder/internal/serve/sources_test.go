package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

const (
	registryV1 = `{"pid":111,"sessionId":"example-session-1","cwd":"/example/work","startedAt":1793375456251,"procStart":"Fri Oct 30 15:50:54 2026","version":"2.1.0","kind":"interactive","entrypoint":"cli","name":"example-1","status":"busy","updatedAt":1793388636910,"futureKey":1}`
	registryV2 = `{"pid":111,"sessionId":"example-session-1","cwd":"/example/work","startedAt":1793375456251,"procStart":"Fri Oct 30 15:50:54 2026","version":"2.1.0","kind":"interactive","entrypoint":"cli","name":"example-1","status":"idle","waitingFor":"input","updatedAt":1793388699999}`
	subMeta    = `{"agentType":"general-purpose","description":"find the example","requestNonInteractive":true,"requestShape":"shape-a","spawnDepth":1,"toolUseId":"toolu_example1"}`
	modeJSON   = `{"kind":"drain-beads","detail":"P1 only","state":"running","started_at":"2026-10-29T14:32:00Z","updated_at":"2026-10-29T14:40:00Z"}`
	compJSON   = `{"ts":1793388000,"trigger":"auto"}`
	statusJSON = `{"ts":1793388100,"session_id":"example-session-1","hostname":"example-host","five_hour_pct":12.5,"five_hour_resets_at":1793400000}` + "\n" +
		`{"ts":1793388200,"session_id":"example-session-1","hostname":"example-host","five_hour_pct":13.5,"seven_day_pct":4}` + "\n"
	transcript = `{"type":"assistant","uuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"toolu_example1","name":"Bash","input":{"command":"true"}},{"type":"tool_use","id":"toolu_example2","name":"Bash","input":{"command":"false"}}]}}` + "\n" +
		`{"type":"user","uuid":"u2","parentUuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_example1","content":"Output too large (3KB). Full output saved to: /example/claude/projects/example-proj/example-session-1/tool-results/toolu_example1.txt"},{"type":"tool_result","tool_use_id":"toolu_example2","content":"Output too large (9KB). Full output saved to: /example/claude/projects/example-proj/example-session-1/tool-results/hook-example.txt"}]}}` + "\n"
)

func (e *env) write(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(e.claude, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *env) settle(t *testing.T) {
	t.Helper()
	if err := e.d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Parser().Drain(); err != nil {
		t.Fatal(err)
	}
}

func (e *env) query(t *testing.T, req string) *query.Answer {
	t.Helper()
	ans, werr := e.d.opQuery(context.Background(), []byte(req))
	if werr != nil {
		t.Fatalf("%s: %v", req, werr)
	}
	return ans.(*query.Answer)
}

func rowsJSON(a *query.Answer) string { b, _ := json.Marshal(a.Rows); return string(b) }

func TestTheSixRemainingSourcesParseTheirFixturesAndLink(t *testing.T) {
	e := newEnv(t, false)
	e.write(t, "projects/example-proj/example-session-1.jsonl", transcript)
	e.write(t, "projects/example-proj/example-session-1/subagents/agent-a1.meta.json", subMeta)
	e.write(t, "projects/example-proj/example-session-1/tool-results/toolu_example1.txt", "the full output of the first call\n")
	e.write(t, "projects/example-proj/example-session-1/tool-results/hook-example.txt", "the full output of the hook\n")
	e.write(t, "projects/example-proj/example-session-1.status.jsonl", statusJSON)
	e.write(t, "projects/example-proj/example-session-1.session-mode.json", modeJSON)
	e.write(t, "projects/example-proj/example-session-1.compaction.json", compJSON)
	e.write(t, "sessions/111.json", registryV1)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	e.settle(t)

	count := e.query(t, `{"group":["source","type"],"aggregate":[{"fn":"count","as":"n"}],"order":[{"by":"source"},{"by":"type"}]}`)
	got := rowsJSON(count)
	for _, want := range []string{
		`["claude-status","status",2]`, `["claude-subagent-meta","subagent_meta",1]`, `["claude-tool-result-body","tool_result_body",2]`,
		`["session-compaction","compaction",1]`, `["session-mode","session_mode",1]`, `["session-registry","registry",1]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}

	one := func(req string) []any {
		a := e.query(t, req)
		if len(a.Rows) != 1 {
			t.Fatalf("%s: %s", req, rowsJSON(a))
		}
		return a.Rows[0]
	}
	reg := one(`{"where":{"field":"type","eq":"registry"},"select":["pid","proc_start","status","session","started_at"]}`)
	if fmt.Sprint(reg[0]) != "111" || reg[1] != "Fri Oct 30 15:50:54 2026" || reg[2] != "busy" || reg[3] != "example-session-1" || !strings.HasPrefix(fmt.Sprint(reg[4]), "2026-10-") {
		t.Errorf("registry row %v", reg)
	}
	if m := one(`{"where":{"field":"type","eq":"session_mode"},"select":["mode_kind","mode_state","session"]}`); m[0] != "drain-beads" || m[1] != "running" || m[2] != "example-session-1" {
		t.Errorf("session-mode row %v", m)
	}
	if c := one(`{"where":{"field":"type","eq":"compaction"},"select":["compaction_trigger","session"]}`); c[0] != "auto" || c[1] != "example-session-1" {
		t.Errorf("compaction row %v", c)
	}
	if s := e.query(t, `{"where":{"field":"type","eq":"status"},"select":["five_hour_pct","hostname"],"order":[{"by":"time"}]}`); len(s.Rows) != 2 || fmt.Sprint(s.Rows[1][0]) != "13.5" {
		t.Errorf("status rows %s", rowsJSON(s))
	}
	if m := one(`{"where":{"field":"type","eq":"subagent_meta"},"select":["agent_type","tool_use_id","agent_id","spawn_depth"]}`); m[0] != "general-purpose" || m[1] != "toolu_example1" || m[2] != "a1" {
		t.Errorf("subagent meta row %v", m)
	}
	if b := one(`{"where":{"all":[{"field":"type","eq":"tool_result_body"},{"field":"name","eq":"toolu_example1.txt"}]},"select":["tool_use_id","body_length"]}`); b[0] != "toolu_example1" || fmt.Sprint(b[1]) != "34" {
		t.Errorf("body row %v", b)
	}
}

func TestRegistryVersionsAndADeletionRecord(t *testing.T) {
	e := newEnv(t, false)
	e.write(t, "sessions/111.json", registryV1)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	e.settle(t)
	e.write(t, "sessions/111.json", registryV2)
	e.settle(t)
	cur := e.query(t, `{"where":{"field":"source","eq":"session-registry"},"select":["type","status","pid","registry_key"]}`)
	if len(cur.Rows) != 1 || cur.Rows[0][1] != "idle" || cur.Rows[0][2] != float64(111) && cur.Rows[0][2] != int64(111) {
		t.Fatalf("current version: %s", rowsJSON(cur))
	}
	all := e.query(t, `{"where":{"field":"source","eq":"session-registry"},"old_generations":true,"select":["status","registry_key","waiting_for"],"order":[{"by":"time"}]}`)
	if len(all.Rows) != 2 || all.Rows[0][0] != "busy" || all.Rows[1][0] != "idle" || all.Rows[1][2] != "input" {
		t.Fatalf("both versions: %s", rowsJSON(all))
	}
	k0, k1 := fmt.Sprint(all.Rows[0][1]), fmt.Sprint(all.Rows[1][1])
	if k0 == k1 || !strings.HasPrefix(k0, "111:Fri Oct 30 15:50:54 2026:") {
		t.Fatalf("keys %q %q", k0, k1)
	}
	if err := os.Remove(filepath.Join(e.claude, "sessions", "111.json")); err != nil {
		t.Fatal(err)
	}
	e.settle(t)
	del := e.query(t, `{"where":{"field":"type","eq":"registry_deleted"},"select":["file_pid","proc_start","session","registry_key"]}`)
	if len(del.Rows) != 1 || fmt.Sprint(del.Rows[0][1]) != "Fri Oct 30 15:50:54 2026" || del.Rows[0][2] != "example-session-1" || fmt.Sprint(del.Rows[0][3]) != k1 {
		t.Fatalf("deletion record: %s (want key %s)", rowsJSON(del), k1)
	}
	// A second sweep does not write it again.
	e.settle(t)
	if n := e.query(t, `{"where":{"field":"type","eq":"registry_deleted"}}`); len(n.Rows) != 1 {
		t.Fatalf("deletion repeated: %s", rowsJSON(n))
	}
	// An unknown key is reported, not dropped silently.
	cov, _ := e.d.coverage("summary")
	found := false
	for _, u := range cov.UnknownKeys {
		found = found || (u.Source == "session-registry" && u.Path == "futureKey")
	}
	if !found {
		t.Fatalf("unknown keys %+v", cov.UnknownKeys)
	}
}

func TestLinksFollowAcrossSources(t *testing.T) {
	e := newEnv(t, false)
	e.write(t, "projects/example-proj/example-session-1.jsonl", transcript)
	e.write(t, "projects/example-proj/example-session-1/subagents/agent-a1.meta.json", subMeta)
	e.write(t, "projects/example-proj/example-session-1/tool-results/toolu_example1.txt", "the full output of the first call\n")
	e.write(t, "projects/example-proj/example-session-1/tool-results/hook-example.txt", "the full output of the hook\n")
	e.write(t, "projects/example-proj/example-session-1.status.jsonl", statusJSON)
	e.write(t, "projects/example-proj/example-session-1.session-mode.json", modeJSON)
	e.write(t, "projects/example-proj/example-session-1.compaction.json", compJSON)
	e.write(t, "sessions/111.json", registryV1)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	e.settle(t)

	follow := func(where, follow string) [][]query.Link {
		a := e.query(t, fmt.Sprintf(`{"where":%s,"select":["type","tool_use_id","name","status","mode_kind","pid"],"follow":%q}`, where, follow))
		return a.Links
	}
	resultOf := func(id string) string {
		return fmt.Sprintf(`{"all":[{"field":"type","eq":"tool_result"},{"field":"tool_use_id","eq":%q}]}`, id)
	}

	// A tool_result links to its body by tool_use_id, or by the saved-to path.
	byID := follow(resultOf("toolu_example1"), "body")
	if len(byID) != 1 || len(byID[0]) != 1 || byID[0][0].Row[0] != "tool_result_body" {
		t.Fatalf("body by tool_use_id: %+v", byID)
	}
	byPath := follow(resultOf("toolu_example2"), "body")
	if len(byPath) != 1 || len(byPath[0]) != 1 || byPath[0][0].Row[0] != "tool_result_body" {
		t.Fatalf("body by saved-to path: %+v", byPath)
	}
	// And a body links back to its tool_result, either way.
	back := follow(`{"field":"type","eq":"tool_result_body"}`, "result")
	var types []string
	for _, l := range back {
		for _, x := range l {
			types = append(types, fmt.Sprint(x.Row[0], ":", x.Row[1]))
		}
	}
	if strings.Join(types, ",") != "tool_result:toolu_example1,tool_result:toolu_example2" && strings.Join(types, ",") != "tool_result:toolu_example2,tool_result:toolu_example1" {
		t.Fatalf("result of body: %v", types)
	}
	// A call to its result, a record to its parent.
	if res := follow(`{"all":[{"field":"type","eq":"tool_use"},{"field":"tool_use_id","eq":"toolu_example1"}]}`, "result"); len(res[0]) != 1 {
		t.Fatalf("call to result: %+v", res)
	}
	if par := follow(`{"field":"uuid","eq":"u2"}`, "parent"); len(par[0]) != 1 {
		t.Fatalf("record to parent: %+v", par)
	}
	// A Conversation to its subagents, status, mode, registry and compaction.
	root := `{"all":[{"field":"type","eq":"assistant"},{"field":"item","eq":-1}]}`
	for follow2, want := range map[string]int{"subagents": 1, "status": 2, "mode": 1, "registry": 1, "compaction": 1} {
		l := follow(root, follow2)
		if len(l) != 1 || len(l[0]) != want {
			t.Errorf("%s: %d links, want %d (%+v)", follow2, len(l[0]), want, l)
		}
	}
	// The subagent meta links to the tool call that spawned it.
	meta := follow(`{"field":"type","eq":"subagent_meta"}`, "call")
	if len(meta) != 1 || len(meta[0]) != 1 || meta[0][0].Row[0] != "tool_use" || meta[0][0].Row[2] != "Bash" {
		t.Fatalf("meta to call: %+v", meta)
	}
	if _, werr := e.d.opQuery(context.Background(), []byte(`{"follow":"nope"}`)); werr == nil || werr.Name != wire.ErrBadRequest {
		t.Fatalf("unknown follow: %v", werr)
	}
}

func TestHelloAndTheDaemonFeedOperations(t *testing.T) {
	e := newEnv(t, false)
	e.write(t, "projects/example-proj/example-session-1.jsonl", transcript)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	e.settle(t)
	h := d.hello().(wire.Hello)
	if h.ProtocolVersion != wire.ProtocolVersion || h.SchemaVersion < 1 || h.Epoch < 1 || h.Service == "" {
		t.Fatalf("hello %+v", h)
	}
	want := []string{"conversation.list", "conversation.show", "coverage", "feed", "hello", "process.list", "process.show", "query", "raw", "search", "snapshot", "status"}
	if strings.Join(h.Capabilities, ",") != strings.Join(want, ",") {
		t.Fatalf("capabilities %v, want %v", h.Capabilities, want)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !d.backfillDone.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h2 := d.hello().(wire.Hello); !h2.CaughtUp || h2.Degraded {
		t.Fatalf("after backfill: %+v", h2)
	}
}
