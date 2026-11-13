package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

type replayFixture struct {
	db, shapes, out string
	proj            string
}

// newReplayFixture builds a throwaway ask log (never the production one) and
// the shapes.jsonl for it through the real shapes command.
func newReplayFixture(t *testing.T) replayFixture {
	t.Helper()
	// The replay's filesystem twin builds its empty project under os.TempDir();
	// inside a nix build sandbox that is under /nix, which the evaluator treats
	// as read-only, so every in-project write in the twin would flip. /var/tmp
	// is neither /tmp nor /nix.
	if tmp := os.TempDir(); tmp == "/nix" || strings.HasPrefix(tmp, "/nix/") {
		dir, err := os.MkdirTemp("/var/tmp", "ceta-replay-test-tmp-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		t.Setenv("TMPDIR", dir)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	for _, d := range []string{proj, filepath.Join(root, "outside")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The fixture path escapes the project through a symlink, so its verdict
	// depends on the filesystem.
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(proj, "link")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))

	db := filepath.Join(t.TempDir(), "asks.db")
	store, err := asklog.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	type fx struct {
		id             int
		tool, input    string
		decision       string // "" is NULL
		permissionMode string
	}
	rows := []fx{
		{1, "Bash", `{"command":"ls -la"}`, "allow", "default"},                                   // approve -> approve
		{2, "Bash", `{"command":"echo hi"}`, "deny", "default"},                                   // old reject -> new approve
		{3, "Read", `{"file_path":"` + proj + `/a.txt"}`, "allow", ""},                            // Read through the adapter
		{4, "Glob", `{"pattern":"*.go"}`, "allow", "default"},                                     // Glob through the adapter
		{5, "Grep", `{"pattern":"x"}`, "allow", "default"},                                        // Grep through the adapter
		{6, "Skill", `{"skill":"s1"}`, "ask", "default"},                                          // unmapped tool
		{7, "Write", `{"file_path":"` + proj + `/link/a.txt","content":"x"}`, "allow", "default"}, // state-dependent
		{8, "Bash", `{"command":"true"}`, "", "default"},                                          // unrecorded
		{9, "Bash", `{"command":"ls -la"}`, "deny", "default"},                                    // same shape as row 1
		{10, "Bash", `{"command":"ls -la"}`, "allow", "default"},                                  // same shape as row 1
	}
	for _, r := range rows {
		var dec, mode any
		if r.decision != "" {
			dec = r.decision
		}
		if r.permissionMode != "" {
			mode = r.permissionMode
		}
		_, err := store.DB().ExecContext(context.Background(), `INSERT INTO tool_decisions
			(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
			 hook_decision, outcome, created_at, permission_mode)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			r.id, "s", proj, r.tool, fmt.Sprintf("h%d", r.id), r.input, "x", dec, "approved", "2026-10-05T00:00:00Z", mode)
		if err != nil {
			t.Fatalf("seed row %d: %v", r.id, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	f := replayFixture{
		db: db, proj: proj,
		shapes: filepath.Join(t.TempDir(), "shapes.jsonl"),
		out:    filepath.Join(t.TempDir(), "replay.jsonl"),
	}
	if err := runShapes(&bytes.Buffer{}, shapesOpts{since: "2026-10-02", db: db, out: f.shapes, src: sourceFlags{source: sourceCeta}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func readReplay(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	out := map[string]map[string]any{}
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out[m["shape_key"].(string)] = m
	}
	return out
}

// find returns the single entry whose tool is tool and whose shape_key
// contains frag.
func find(t *testing.T, all map[string]map[string]any, tool, frag string) map[string]any {
	t.Helper()
	var hit map[string]any
	for _, m := range all {
		if m["tool"] == tool && (frag == "" || m["program"] == frag) {
			if hit != nil {
				t.Fatalf("more than one %s shape contains %q", tool, frag)
			}
			hit = m
		}
	}
	if hit == nil {
		t.Fatalf("no %s shape with program %q in %d shapes", tool, frag, len(all))
	}
	return hit
}

func TestReplayCommandVerdicts(t *testing.T) {
	f := newReplayFixture(t)
	before, err := os.ReadFile(f.db)
	if err != nil {
		t.Fatal(err)
	}
	st1, _ := os.Stat(f.db)

	var stdout, stderr bytes.Buffer
	err = runReplay(&stdout, &stderr, replayOpts{since: "2026-10-02", db: f.db, shapes: f.shapes, out: f.out, src: sourceFlags{source: sourceCeta}})
	if err != nil {
		t.Fatal(err)
	}
	all := readReplay(t, f.out)

	// approve -> approve; the shape has 3 rows (allow, deny, allow): most
	// common recorded decision is allow.
	ls := find(t, all, "Bash", "ls")
	if ls["old_decision"] != "approve" || ls["new_decision"] != "approve" || ls["transition"] != "approve->approve" ||
		ls["less_restrictive"] != false || ls["state_dependent"] != false {
		t.Errorf("ls shape: %v", ls)
	}
	if ls["rows"].(float64) != 3 {
		t.Errorf("ls rows = %v, want 3", ls["rows"])
	}

	// old reject -> new approve is flagged less_restrictive.
	echo := find(t, all, "Bash", "echo")
	if echo["old_decision"] != "reject" || echo["new_decision"] != "approve" || echo["transition"] != "reject->approve" || echo["less_restrictive"] != true {
		t.Errorf("echo hi shape: %v", echo)
	}

	// An unrecorded old decision is never less restrictive.
	bye := find(t, all, "Bash", "true")
	if bye["old_decision"] != "unrecorded" || bye["less_restrictive"] != false || bye["transition"] != "unrecorded->approve" {
		t.Errorf("true shape: %v", bye)
	}

	// Read, Glob and Grep are routed through the adapter, not left unmapped.
	for _, tool := range []string{"Read", "Glob", "Grep"} {
		m := find(t, all, tool, "")
		if m["new_decision"] != "approve" || strings.Contains(m["new_reason"].(string), "unsupported") {
			t.Errorf("%s shape: %v", tool, m)
		}
	}

	// An unmapped tool is abstain, with the adapter's message, and counted.
	skill := find(t, all, "Skill", "")
	if skill["new_decision"] != "abstain" || skill["old_decision"] != "ask" || skill["transition"] != "ask->abstain" ||
		!strings.Contains(skill["new_reason"].(string), "unsupported tool Skill") {
		t.Errorf("Skill shape: %v", skill)
	}

	// The symlink Write depends on the filesystem.
	w := find(t, all, "Write", "")
	consulted, _ := w["state_consulted"].([]any)
	if w["state_dependent"] != true || len(consulted) == 0 || consulted[0] != "filesystem" || w["new_decision"] == "approve" {
		t.Errorf("Write shape: %v", w)
	}

	// Every shapes.jsonl field is carried through, and arrays are never null.
	for _, k := range []string{
		"shape_key", "tool", "program", "subcommand", "flags", "path_classes", "env_names", "programs", "family", "rows", "example_row_ids",
		"old_decision", "new_decision", "new_reason", "transition", "less_restrictive", "state_dependent", "state_consulted",
	} {
		if v, ok := ls[k]; !ok || v == nil {
			t.Errorf("field %q missing or null: %v", k, ls)
		}
	}

	// Summary lines, in order.
	out := stdout.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[0] != "shapes=8 rows=10" {
		t.Errorf("first line = %q, want shapes=8 rows=10\n%s", lines[0], out)
	}
	for _, want := range []string{
		"transition approve->approve shapes=",
		"transition ask->abstain shapes=1 rows=1",
		"transition reject->approve shapes=1 rows=1",
		"transition unrecorded->approve shapes=1 rows=1",
		"less_restrictive shapes=2 rows=2", // reject->approve and, under approve < abstain < ask < reject, ask->abstain
		"abstain-unmapped shapes=1 rows=1",
		"unreadable shapes=0",
		"## State-dependent (excluded from gates)",
		"state-dependent shapes=1 rows=1",
		"consulted=filesystem",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	var trans []string
	for _, l := range lines {
		if strings.HasPrefix(l, "transition ") {
			trans = append(trans, strings.Fields(l)[1])
		}
	}
	for i := 1; i < len(trans); i++ {
		if trans[i-1] >= trans[i] {
			t.Errorf("transition lines not sorted: %v", trans)
		}
	}
	if strings.Index(out, "less_restrictive shapes") > strings.Index(out, "## State-dependent") {
		t.Errorf("section order wrong:\n%s", out)
	}

	// The ask log is untouched.
	after, _ := os.ReadFile(f.db)
	st2, _ := os.Stat(f.db)
	if !bytes.Equal(before, after) || !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("replay modified the ask-log database")
	}
}

func TestReplayUnreadableRepresentativeRowIsAbstainAndCounted(t *testing.T) {
	f := newReplayFixture(t)
	line := `{"shape_key":"Bash|ghost","tool":"Bash","program":"ghost","subcommand":"","flags":[],"path_classes":[],"env_names":[],"programs":["ghost"],"family":"ghost","rows":4,"example_row_ids":[9999]}` + "\n"
	if err := os.WriteFile(f.shapes, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runReplay(&stdout, &stderr, replayOpts{since: "2026-10-02", db: f.db, shapes: f.shapes, out: f.out, src: sourceFlags{source: sourceCeta}}); err != nil {
		t.Fatal(err)
	}
	m := readReplay(t, f.out)["Bash|ghost"]
	if m["new_decision"] != "abstain" || m["new_reason"] != "representative-row-unreadable" || m["old_decision"] != "unrecorded" {
		t.Fatalf("entry = %v", m)
	}
	if !strings.Contains(stderr.String(), "Bash|ghost") || !strings.Contains(stdout.String(), "unreadable shapes=1") {
		t.Fatalf("stderr=%q stdout=%q", stderr.String(), stdout.String())
	}
}

func TestReplayDefaultPathsUseART(t *testing.T) {
	f := newReplayFixture(t)
	state := os.Getenv("XDG_STATE_HOME")
	art := filepath.Join(state, "claude-extended-tool-approver", "phase4")
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.shapes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, "shapes.jsonl"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runReplay(&bytes.Buffer{}, &bytes.Buffer{}, replayOpts{since: "2026-10-02", db: f.db, src: sourceFlags{source: sourceCeta}}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(art, "replay.jsonl")); err != nil || fi.Size() == 0 {
		t.Fatalf("ART/replay.jsonl not written: %v", err)
	}
}

func TestReplayMissingShapesFileIsAnError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	err := runReplay(&bytes.Buffer{}, &bytes.Buffer{}, replayOpts{src: sourceFlags{source: sourceCeta}})
	if err == nil || !strings.Contains(err.Error(), "shapes") {
		t.Fatalf("got %v", err)
	}
}

func TestReplayRejectsUnknownSource(t *testing.T) {
	err := runReplay(&bytes.Buffer{}, &bytes.Buffer{}, replayOpts{src: sourceFlags{source: "bogus"}})
	if err == nil || !strings.Contains(err.Error(), "--source") {
		t.Fatalf("want a --source error, got %v", err)
	}
}

func TestReplaySubcommandRegistered(t *testing.T) {
	if !knownSubcommands["replay"] {
		t.Fatal("replay missing from knownSubcommands")
	}
	cmd, _, err := newRootCmd().Find([]string{"replay"})
	if err != nil || cmd.Name() != "replay" {
		t.Fatalf("replay not on the root command: %v", err)
	}
}

func TestReplayOldDecisionTiesGoToMoreRestrictive(t *testing.T) {
	allow, deny := "allow", "deny"
	mk := func(id int, d *string) asklog.DecisionRow {
		return asklog.DecisionRow{ID: id, ToolName: "Bash", ToolInputJSON: `{"command":"ls x"}`, CWD: "/w", HookDecision: d}
	}
	rows := []asklog.DecisionRow{mk(1, &allow), mk(2, &deny), mk(3, nil)}
	got := oldDecisions(rows)
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	for _, d := range got {
		if d != "reject" {
			t.Fatalf("tie decision = %q, want reject", d)
		}
	}
}
