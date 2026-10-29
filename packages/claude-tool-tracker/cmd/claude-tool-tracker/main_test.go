package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ctt-bin")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "claude-tool-tracker")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// run executes the real binary with XDG_STATE_HOME=state.
func run(t *testing.T, state, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state)
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return o.String(), e.String(), code
}

func eventsFile(state, session, source string) string {
	return filepath.Join(state, "claude-tool-tracker", "events", "tool-events."+session+"."+source+".jsonl")
}

func TestAppendWritesEnvelope(t *testing.T) {
	state := t.TempDir()
	_, errOut, code := run(t, state, `{"tool":"Bash","n":12345678901234567890}`,
		"append", "--session-id", "s1", "--source", "tracker", "--kind", "tracker.call_start",
		"--tool-use-id", "tu1", "--agent-id", "ag1", "--ts", "1700000000123", "--event-id", "ev1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	b, err := os.ReadFile(eventsFile(state, "s1", "tracker"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"tool-events/1","event_id":"ev1","ts":1700000000123,"source":"tracker","kind":"tracker.call_start","session_id":"s1","tool_use_id":"tu1","agent_id":"ag1","data":{"n":12345678901234567890,"tool":"Bash"}}` + "\n"
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

func TestAppendEmptyStdinIsEmptyData(t *testing.T) {
	state := t.TempDir()
	if _, e, code := run(t, state, "", "append", "--session-id", "s1", "--source", "ceta", "--kind", "ceta.trace", "--tool-use-id", "t"); code != 0 {
		t.Fatal(code, e)
	}
	b, _ := os.ReadFile(eventsFile(state, "s1", "ceta"))
	if !strings.Contains(string(b), `"data":{}`) {
		t.Fatal(string(b))
	}
}

func TestAppendRejectsBadInput(t *testing.T) {
	state := t.TempDir()
	base := []string{"append", "--session-id", "s1", "--source", "tracker", "--kind", "tracker.call_start", "--tool-use-id", "t"}
	for name, c := range map[string]struct {
		stdin string
		args  []string
	}{
		"bad json":       {"{x", base},
		"not object":     {"[1]", base},
		"trailing":       {`{} {}`, base},
		"no tool use id": {"{}", []string{"append", "--session-id", "s1", "--source", "tracker", "--kind", "tracker.call_start"}},
		"dotted source":  {"{}", []string{"append", "--session-id", "s1", "--source", "a.b", "--kind", "a.b.c", "--tool-use-id", "t"}},
		"path session":   {"{}", []string{"append", "--session-id", "../x", "--source", "tracker", "--kind", "tracker.call_start", "--tool-use-id", "t"}},
	} {
		if _, _, code := run(t, state, c.stdin, c.args...); code == 0 {
			t.Errorf("%s: exit 0", name)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(state, "claude-tool-tracker", "events")); len(ents) != 0 {
		t.Errorf("a rejected call created files: %v", ents)
	}
}

func TestConcurrentAppendsOneFile(t *testing.T) {
	state := t.TempDir()
	const procs, per = 24, 5
	big := strings.Repeat("x", 3000)
	var wg sync.WaitGroup
	for p := range procs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range per {
				payload := fmt.Sprintf(`{"p":%d,"i":%d,"pad":%q}`, p, i, big)
				if _, e, code := run(t, state, payload, "append", "--session-id", "conc", "--source", "tracker",
					"--kind", "tracker.call_start", "--tool-use-id", fmt.Sprintf("tu-%d-%d", p, i)); code != 0 {
					t.Errorf("append %d/%d exit %d: %s", p, i, code, e)
				}
			}
		}()
	}
	wg.Wait()
	b, err := os.ReadFile(eventsFile(state, "conc", "tracker"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != procs*per {
		t.Fatalf("lines = %d, want %d", len(lines), procs*per)
	}
	ids := map[string]bool{}
	for _, l := range lines {
		var e struct {
			EventID   string         `json:"event_id"`
			ToolUseID string         `json:"tool_use_id"`
			Data      map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("corrupt line: %v: %.80s", err, l)
		}
		if ids[e.EventID] || e.Data["pad"] != big {
			t.Fatalf("duplicate id or damaged data in %.80s", l)
		}
		ids[e.EventID] = true
	}
}

func TestModesUnderPermissiveUmask(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	script := fmt.Sprintf(`umask 000; XDG_STATE_HOME=%q exec %q append --session-id s1 --source tracker --kind tracker.session_end </dev/null`, state, binPath)
	if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	mode := func(p string) os.FileMode {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return st.Mode().Perm()
	}
	if m := mode(filepath.Join(state, "claude-tool-tracker", "events")); m != 0o700 {
		t.Errorf("events dir %v", m)
	}
	if m := mode(filepath.Join(state, "claude-tool-tracker")); m != 0o700 {
		t.Errorf("parent dir %v", m)
	}
	if m := mode(eventsFile(state, "s1", "tracker")); m != 0o600 {
		t.Errorf("file %v", m)
	}
}

const preTool = `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/w","permission_mode":"auto","tool_name":"Bash","tool_input":{},"tool_use_id":"tu1"}`

func TestHookModeWritesAndIsSilent(t *testing.T) {
	state := t.TempDir()
	out, errOut, code := run(t, state, preTool, "hook")
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	b, _ := os.ReadFile(eventsFile(state, "s1", "tracker"))
	if !strings.Contains(string(b), `"kind":"tracker.pre_tool"`) || !strings.Contains(string(b), `"permission_mode":"auto"`) {
		t.Fatalf("%s", b)
	}
}

func TestHookModeExitsZeroSilentlyOnBadInput(t *testing.T) {
	for name, in := range map[string]string{"empty": "", "malformed": "{not json", "unknown event": `{"hook_event_name":"Stop","session_id":"s"}`} {
		state := t.TempDir()
		out, errOut, code := run(t, state, in, "hook")
		if code != 0 || out != "" || errOut != "" {
			t.Errorf("%s: code %d stdout %q stderr %q", name, code, out, errOut)
		}
		if _, err := os.Stat(filepath.Join(state, "claude-tool-tracker")); err == nil {
			t.Errorf("%s: created state", name)
		}
	}
}

func TestHookModeExitsZeroWhenDirUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(state, 0o700)
	out, errOut, code := run(t, state, preTool, "hook")
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(state, "claude-tool-tracker")); err == nil {
		t.Fatal("wrote into an unwritable directory")
	}
}

func TestAppendFailsWhenDirUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	state := t.TempDir()
	_ = os.Chmod(state, 0o500)
	defer os.Chmod(state, 0o700)
	if _, _, code := run(t, state, "{}", "append", "--session-id", "s", "--source", "tracker", "--kind", "tracker.session_end"); code == 0 {
		t.Fatal("append should report the failure")
	}
}

func TestOversizedAppendIsOneShortenedLine(t *testing.T) {
	state := t.TempDir()
	payload := fmt.Sprintf(`{"input":{"content":%q}}`, strings.Repeat("y", 500000))
	if _, e, code := run(t, state, payload, "append", "--session-id", "s", "--source", "tracker", "--kind", "tracker.call_start", "--tool-use-id", "t"); code != 0 {
		t.Fatal(code, e)
	}
	b, _ := os.ReadFile(eventsFile(state, "s", "tracker"))
	if len(b) > 65536 || strings.Count(string(b), "\n") != 1 {
		t.Fatalf("size %d newlines %d", len(b), strings.Count(string(b), "\n"))
	}
	var e map[string]any
	if err := json.Unmarshal(b, &e); err != nil || e["data"].(map[string]any)["_truncated"] == nil {
		t.Fatalf("%v", err)
	}
}
