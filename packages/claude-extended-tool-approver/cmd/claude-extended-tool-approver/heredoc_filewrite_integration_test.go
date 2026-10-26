//go:build integration

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

// heredocFileWriteRow is one seeded ask-log row for the pg2-4godq replay test.
type heredocFileWriteRow struct {
	id      int
	command string
	// want is the replay_result the CURRENT engine must give the row.
	want string
	// wantModule / wantReason pin the attribution (empty = do not assert).
	wantModule string
	wantReason string
}

// TestEvaluate_HeredocFileWriteReplay replays rows through the REAL binary against a
// hermetic ask log (a temp XDG_DATA_HOME; the live decision database is never
// opened). The rows reproduce the bead pg2-4godq shapes: ids 1 and 2 are the
// `cat >> FILE <<'EOF'` group (live rows 600007 and 600020), id 3 the plain `cat
// <<'EOF'` row (live 162682), and 4-7 the unquoted-body fixtures. All seeded rows
// were `abstain` + user-approved, i.e. approve-candidates, so a row whose replay is
// `allow` moves from miss to correct.
func TestEvaluate_HeredocFileWriteReplay(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.1.md")
	outside := "/etc/pg2-4godq-not-allowed.md"

	rows := []heredocFileWriteRow{
		{1, "cat >> " + plan + " <<'EOF'\n## 3. Architecture\n```mermaid\nflowchart LR\n```\nEOF\necho ok", "allow", "", ""},
		{2, "cat > " + plan + " <<'EOF'\n$(rm -rf /) is literal text here\nEOF", "allow", "", ""},
		{3, "cat <<'EOF'\nAnalyzing set -e interactions\nEOF", "abstain", "engine", "heredoc body is not evaluable as a command"},
		{4, "cat >> " + plan + " <<EOF\nstamp: $(echo a)\nEOF", "allow", "", ""},
		{5, "cat >> " + plan + " <<EOF\nstamp: $(rm -rf /)\nEOF", "deny", "", "command substitution denied"},
		{6, "cat >> " + outside + " <<'EOF'\nx\nEOF", "", "", ""}, // asserted below: never allow
		{7, "cat >> " + plan + " <<EOF\n${x@P}\nEOF", "abstain", "engine", "heredoc body is not evaluable as a command"},
	}

	store, err := asklog.NewStore(filepath.Join(dir, "claude-extended-tool-approver", "asks.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		in, _ := json.Marshal(map[string]string{"command": r.command})
		if _, err := store.DB().Exec(`INSERT INTO tool_decisions
			(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
			 hook_decision, hook_reason, outcome, created_at)
			VALUES (?,'s',?,'Bash',?,?,?,'abstain','','approved','2026-01-01T00:00:00Z')`,
			r.id, dir, "h"+string(rune('0'+r.id)), string(in), strings.SplitN(r.command, "\n", 2)[0]); err != nil {
			t.Fatalf("seed row %d: %v", r.id, err)
		}
	}
	_ = store.Close()

	byID := evaluateJSONByID(t, dir)
	for _, r := range rows {
		row := byID[float64(r.id)]
		if row == nil {
			t.Fatalf("row %d missing from evaluate output", r.id)
		}
		got, _ := row["replay_result"].(string)
		module, _ := row["replay_module"].(string)
		reason, _ := row["replay_reason"].(string)
		t.Logf("row %d: replay_result=%q replay_module=%q replay_reason=%.80q category=%v", r.id, got, module, reason, row["category"])
		if r.id == 6 {
			if got == "allow" {
				t.Errorf("row 6 (write outside the allowlist) replayed allow; the floor must never be the only thing between a heredoc and a protected path")
			}
			continue
		}
		if got != r.want {
			t.Errorf("row %d replay_result = %q, want %q (module %q, reason %q)", r.id, got, r.want, module, reason)
		}
		if r.wantModule != "" && module != r.wantModule {
			t.Errorf("row %d replay_module = %q, want %q", r.id, module, r.wantModule)
		}
		if r.wantReason != "" && !strings.Contains(reason, r.wantReason) {
			t.Errorf("row %d replay_reason = %q, want it to contain %q", r.id, reason, r.wantReason)
		}
		// An approved row CETA now allows is a hit, never a miss.
		if r.want == "allow" && row["category"] != "correct" {
			t.Errorf("row %d category = %v, want correct (an approved row the hook now allows)", r.id, row["category"])
		}
	}
}

// TestHook_HeredocFileWrite drives the hook entry point (stdin JSON -> stdout
// decision) with the four shapes of bead pg2-4godq.
func TestHook_HeredocFileWrite(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	// Hermetic: no consumer rules.json and no developer ask log leak into the verdict.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	tests := []struct {
		name    string
		command string
		want    string // permissionDecision; "" = no decision (abstain)
	}{
		{"quoted delimiter, write-allowed path", "cat >> " + plan + " <<'EOF'\nhello\nEOF", "allow"},
		{"quoted delimiter, path outside the allowlist", "cat >> /etc/pg2-4godq-x <<'EOF'\nhello\nEOF", "!allow"},
		{"unquoted, unsafe expansion", "cat >> " + plan + " <<EOF\n$(rm -rf /)\nEOF", "deny"},
		{"unquoted, safe expansion", "cat >> " + plan + " <<EOF\n$(echo hi)\nEOF", "allow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, _ := json.Marshal(map[string]any{
				"tool_name":  "Bash",
				"tool_input": map[string]string{"command": tt.command},
				"cwd":        dir,
			})
			got := getDecision(runHook(t, string(in)))
			if strings.HasPrefix(tt.want, "!") {
				if got == strings.TrimPrefix(tt.want, "!") {
					t.Errorf("decision = %q for %q, want anything but %q", got, tt.command, strings.TrimPrefix(tt.want, "!"))
				}
				return
			}
			if got != tt.want {
				t.Errorf("decision = %q for %q, want %q", got, tt.command, tt.want)
			}
		})
	}
}
