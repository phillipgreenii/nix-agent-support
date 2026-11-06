package serve

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

func TestNoteFilterFieldsLogsCountsNotValues(t *testing.T) {
	var buf bytes.Buffer
	d := &Daemon{obs: obs.New(NewLogger(&buf))}
	d.noteFilterFields("query", map[string]int{"type": 2, "cwd": 1})
	// Per-request line is debug level: absent from the default info log.
	if out := buf.String(); out != "" {
		t.Fatalf("info log carries per-request line: %s", out)
	}
	var dbg bytes.Buffer
	dd := &Daemon{obs: obs.New(slog.New(slog.NewJSONHandler(&dbg, &slog.HandlerOptions{Level: slog.LevelDebug})))}
	dd.noteFilterFields("query", map[string]int{"type": 2, "cwd": 1})
	out := dbg.String()
	if !strings.Contains(out, `"query_filter_fields"`) || !strings.Contains(out, `"fields":"cwd=1,type=2"`) || !strings.Contains(out, `"op":"query"`) {
		t.Fatalf("debug log line %s", out)
	}
	if v, _ := d.obs.Reg.Counter(obs.MFilterFields, map[string]string{"op": "query", "field": "type"}); v != 2 {
		t.Fatalf("counter %v", v)
	}
}
