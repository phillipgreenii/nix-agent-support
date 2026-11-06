package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
)

var t0 = time.UnixMilli(1793375000000).UTC()

func regJSON(pid int, session string, start time.Time, status string, updated int64) string {
	return fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/example/work","startedAt":%d,"procStart":%q,"kind":"interactive","status":%q,"updatedAt":%d}`,
		pid, session, start.UnixMilli(), proc.FormatStart(start), status, updated)
}

func (e *env) registry(t *testing.T, pid int, body string) {
	t.Helper()
	e.write(t, fmt.Sprintf("sessions/%d.json", pid), body)
}

func TestEndFilesAreParsedJoinedCountedAndRemovedAfterADay(t *testing.T) {
	e := newEnv(t, true)
	clk := clock.NewFake(t0)
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	defer func() { _ = d.Close() }()
	ends := e.cfg.EndsDir()
	good := filepath.Join(ends, "example-old.1793375050000.json")
	bad := filepath.Join(ends, "example-bad.1793375060000.json")
	_ = os.WriteFile(good, []byte(`{"sessionId":"example-old","reason":"clear","timeMs":1793375050000}`), 0o600)
	_ = os.WriteFile(bad, []byte(`{"sessionId":"example-bad","reason":"surprise","timeMs":1793375060000}`), 0o600)
	e.registry(t, 471, regJSON(471, "example-old", t0, "idle", 1))
	e.procs.Add(471, t0, nil)
	e.settle(t)
	ans := e.query(t, `{"where":{"field":"source","eq":"claude-control-end"},"group":["session","reason"],"aggregate":[{"fn":"count","as":"n"}],"order":[{"by":"session"}]}`)
	if got := rowsJSON(ans); !strings.Contains(got, `"example-old","clear",1`) {
		t.Fatalf("end records %s", got)
	}
	// The end file joins the old session: conversation.show carries its reason.
	c, werr := d.opConversationShow(context.Background(), []byte(`{"session_id":"example-old"}`))
	if werr != nil || c.(lifecycle.Conversation).EndReason != "clear" {
		t.Fatalf("show %+v %v", c, werr)
	}
	d.Tracker().Poll()
	reg := d.Obs().Reg
	if v, _ := reg.Counter(obs.MEndFiles, map[string]string{"outcome": "ingested"}); v != 1 {
		t.Fatalf("ingested %v", v)
	}
	if v, _ := reg.Counter(obs.MEndFiles, map[string]string{"outcome": "invalid"}); v != 1 {
		t.Fatalf("invalid %v", v)
	}
	d.Tracker().Poll()
	if v, _ := reg.Counter(obs.MEndFiles, map[string]string{"outcome": "ingested"}); v != 1 {
		t.Fatalf("a file was counted twice: %v", v)
	}
	if _, err := os.Stat(good); err != nil {
		t.Fatal("the end file was removed before a day passed")
	}
	clk.Advance(25 * time.Hour)
	d.Tracker().Poll()
	if _, err := os.Stat(good); err == nil {
		t.Fatal("the ingested end file was not removed after a day")
	}
	if _, err := os.Stat(bad); err == nil {
		t.Fatal("the ingested end file that is invalid by the schema was not removed after a day")
	}
	if v, _ := reg.Counter(obs.MEndFiles, map[string]string{"outcome": "removed"}); v != 2 {
		t.Fatalf("removed %v", v)
	}
	d.Tracker().Poll()
	if v, _ := reg.Counter(obs.MEndFiles, map[string]string{"outcome": "removed"}); v != 2 {
		t.Fatalf("removed counted again: %v", v)
	}
	if v, _ := reg.Counter(obs.MFallbackFound, map[string]string{"kind": "ends"}); v != 0 {
		t.Fatalf("the event path missed an end file: %v", v)
	}
}
