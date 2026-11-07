package ccpool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var bg = context.Background()

// fakeCLI installs the fake ccpool script and returns the log of its
// commands. scenario is the content of the scenario file.
func fakeCLI(t *testing.T, scenario string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ccpool", "ccpool"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ccpool"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	sc := filepath.Join(dir, "scenario")
	if err := os.WriteFile(sc, []byte(scenario), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath = filepath.Join(dir, "log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_CCPOOL_SCENARIO", sc)
	t.Setenv("FAKE_CCPOOL_LOG", logPath)
	return logPath
}

func runAll(t *testing.T, c CLI) {
	t.Helper()
	if _, err := c.Capacity(bg); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(bg, StartOpts{ExternalID: "7", Model: "m", PermissionMode: "dontAsk", AllowedTools: []string{"Read", "Grep"}}); err != nil {
		t.Fatal(err)
	}
	turn, err := c.Reply(bg, "7", "hello")
	if err != nil || turn != "turn-1" {
		t.Fatalf("%q %v", turn, err)
	}
	if _, _, err := c.Result(bg, turn); err != nil {
		t.Fatal(err)
	}
	if _, err := c.State(bg, "7"); err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel(bg, "7"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(bg, "7", "handler"); err != nil {
		t.Fatal(err)
	}
}

func logLines(t *testing.T, p string) []string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestPoolDirSetIsPassedToEveryCommand(t *testing.T) {
	log := fakeCLI(t, "")
	t.Setenv(PoolEnv, "/parent/pool")
	runAll(t, CLI{PoolDir: "/cfg/pool"})
	lines := logLines(t, log)
	if len(lines) != 7 {
		t.Fatalf("want 7 commands, got %v", lines)
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, "CCPOOL_POOL=/cfg/pool") {
			t.Errorf("command saw %q", l)
		}
	}
}

func TestPoolDirUnsetLeavesCcpoolPoolUnsetEvenWhenParentHoldsOne(t *testing.T) {
	log := fakeCLI(t, "")
	t.Setenv(PoolEnv, "/parent/pool")
	runAll(t, CLI{})
	lines := logLines(t, log)
	if len(lines) != 7 {
		t.Fatalf("want 7 commands, got %v", lines)
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, "CCPOOL_POOL=UNSET") {
			t.Errorf("command saw %q", l)
		}
	}
}

func TestStartFlags(t *testing.T) {
	log := fakeCLI(t, "")
	err := CLI{}.Start(bg, StartOpts{
		ExternalID: "7", Cwd: "/n", Model: "m", Effort: "low", PermissionMode: "dontAsk",
		AllowedTools: []string{"Read", "Bash(pg-review agent q:*)"}, Env: []string{"A=1", "B=2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log + ".args")
	want := "new 7 --cwd /n --model m --effort low --permission-mode dontAsk --allowed-tools Read,Bash(pg-review agent q:*) --env A=1 --env B=2"
	if strings.TrimSpace(string(b)) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

func TestExitCodesAreTyped(t *testing.T) {
	fakeCLI(t, "new_exit=8\nreply_exit=5\n")
	c := CLI{}
	if err := c.Start(bg, StartOpts{ExternalID: "1"}); !IsUsageLimited(err) {
		t.Errorf("new exit 8: %v", err)
	}
	if _, err := c.Reply(bg, "1", "p"); !IsBusy(err) {
		t.Errorf("reply exit 5: %v", err)
	}
}

func TestResultExitCodes(t *testing.T) {
	for exit, want := range map[string]ResultStatus{"0": ResultDone, "2": ResultPending, "1": ResultUnknown} {
		fakeCLI(t, "result_exit="+exit+"\n")
		_, st, err := CLI{}.Result(bg, "t")
		if err != nil || st != want {
			t.Errorf("exit %s: %v %v", exit, st, err)
		}
	}
}

func TestCapacityReportsUsageLimit(t *testing.T) {
	fakeCLI(t, `capacity_json='{"max_sessions":4,"free":0,"usage_limit":{"window":"5h","used_pct":100,"resets_at":"2026-03-10T15:00:00Z"}}'`+"\n")
	c, err := CLI{}.Capacity(bg)
	if err != nil || c.Free != 0 || c.UsageLimit == nil || c.UsageLimit.Window != "5h" {
		t.Fatalf("%+v %v", c, err)
	}
}
