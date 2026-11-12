package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

const rebuildFixtureSession = `{"type":"user","uuid":"u1","sessionId":"example-session-%d","timestamp":"2026-10-29T20:00:00Z","cwd":"/example/work","message":{"role":"user","content":"find the registry file"}}
{"type":"assistant","uuid":"u2","parentUuid":"u1","sessionId":"example-session-%d","timestamp":"2026-10-29T20:00:01Z","message":{"id":"m1","model":"example-model-a","role":"assistant","usage":{"input_tokens":3,"output_tokens":10},"content":[{"type":"text","text":"the registry file was deleted"}]}}
`

func jsonLogLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func TestRebuildLogsProgressAndASummaryWithConsistentNumbers(t *testing.T) {
	const files = 30
	k := testkit.New(t)
	for i := 0; i < files; i++ {
		k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", i), fmt.Sprintf(rebuildFixtureSession, i, i))
	}
	k.Reopen() // journal only; nothing parsed yet
	if err := k.Store.Close(); err != nil {
		t.Fatal(err)
	}
	sd := k.Dir
	cfgPath := filepath.Join(shortDir(t), "c.toml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("stateDir = %q\nclaudeDir = %q\n", sd, testkit.ClaudeDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	a, _, errw := testApp(map[string]string{config.EnvConfig: cfgPath})
	if code := a.run([]string{"rebuild", "--progress-interval", "1ms"}); code != wire.ExitOK {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	var sum, prof map[string]any
	prog := 0
	for _, m := range jsonLogLines(t, errw.String()) {
		switch m["event"] {
		case "rebuild_progress":
			prog++
			for _, f := range []string{"records_parsed", "records_per_sec", "commits", "commits_per_sec", "files_done", "files_total", "index_db_bytes", "wal_bytes"} {
				if _, ok := m[f]; !ok {
					t.Fatalf("progress line lacks %s: %v", f, m)
				}
			}
			// A line logged before ParseAll lists the files reports 0 of 0.
			if tot := m["files_total"].(float64); (tot != 0 && tot != files) || m["files_done"].(float64) > tot {
				t.Fatalf("files %v of %v", m["files_done"], tot)
			}
		case "rebuild_summary":
			sum = m
		case "rebuild_profile":
			prof = m
		}
	}
	if prof == nil {
		t.Fatalf("no rebuild_profile line in %s", errw.String())
	}
	for _, f := range []string{"wall_sec", "parse_sec", "finish_sec", "records", "records_per_sec", "batch_commits", "batch_commit_sec", "index_build_sec", "fts_optimize_sec", "checkpoint_sec", "index_db_bytes"} {
		if _, ok := prof[f]; !ok {
			t.Fatalf("profile lacks %s: %v", f, prof)
		}
	}
	if prof["wall_sec"].(float64) <= 0 || prof["records_per_sec"].(float64) <= 0 || prof["records"].(float64) <= 0 {
		t.Fatalf("profile %v", prof)
	}
	if prog == 0 {
		t.Fatalf("no progress line in %s", errw.String())
	}
	if sum == nil {
		t.Fatalf("no summary in %s", errw.String())
	}
	if sum["files_done"] != sum["files_total"] || sum["files_total"].(float64) != files {
		t.Fatalf("files %v/%v", sum["files_done"], sum["files_total"])
	}
	if sum["commits"].(float64) < 1 || sum["wall_sec"].(float64) <= 0 {
		t.Fatalf("summary %v", sum)
	}
	k.Reopen()
	var stored string
	if err := k.Store.Read().QueryRowContext(context.Background(), `SELECT value FROM meta WHERE key='last_rebuild_profile'`).Scan(&stored); err != nil || !strings.Contains(stored, `"records_per_sec"`) {
		t.Fatalf("the stored profile: %q, %v", stored, err)
	}
	if want := k.Count(`SELECT COUNT(*) FROM records`); want == 0 || sum["records_parsed"].(float64) != float64(want) {
		t.Fatalf("summary records %v, index has %d", sum["records_parsed"], want)
	}
}

func TestRebuildReporterProgressRateMatchesTheCounters(t *testing.T) {
	var buf strings.Builder
	cur := rebuildSample{}
	mu := make(chan struct{}, 1)
	mu <- struct{}{}
	read := func() rebuildSample { <-mu; defer func() { mu <- struct{}{} }(); return cur }
	r := startRebuildReporter(serve.NewLogger(&buf), t.TempDir(), 20*time.Millisecond, read)
	<-mu
	cur = rebuildSample{records: 1000, commits: 2, filesDone: 5, filesTotal: 10}
	mu <- struct{}{}
	time.Sleep(60 * time.Millisecond)
	<-mu
	cur = rebuildSample{records: 1500, commits: 3, filesDone: 10, filesTotal: 10}
	mu <- struct{}{}
	r.finish()
	var lines, last int
	var prevRec float64
	for _, m := range jsonLogLines(t, buf.String()) {
		if m["event"] == "rebuild_progress" {
			lines++
			// The rate is over the interval: it is positive exactly when the
			// count moved since the previous line.
			rec := m["records_parsed"].(float64)
			if moved := rec != prevRec; moved != (m["records_per_sec"].(float64) > 0) {
				t.Fatalf("rate %v does not follow the counter (%v -> %v)", m["records_per_sec"], prevRec, rec)
			}
			prevRec = rec
		}
		if m["event"] == "rebuild_summary" {
			last++
			if m["records_parsed"].(float64) != 1500 || m["commits"].(float64) != 3 {
				t.Fatalf("summary %v", m)
			}
		}
	}
	if lines == 0 || last != 1 {
		t.Fatalf("progress %d summary %d: %s", lines, last, buf.String())
	}
}
