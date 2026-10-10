// harness_test.go: the reentrant test-helper-process double for pg-connector
// and pg-desk. The test binary re-execs itself with GO_WANT_HELPER_PROCESS=1;
// TestHelperProcess then impersonates whichever binary was named: it appends
// one JSON record (argv, and the PG_CONNECTOR_ISSUE_BEADS_DIR environment
// variable when present) to the file named by GO_HELPER_LOG, then answers from
// the first rule of the JSON script named by GO_HELPER_SCRIPT whose match text
// occurs in "<binary> <args...>". Pattern: view/testmain_test.go.
package apply

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/config"
)

type record struct {
	Name     string   `json:"name"`
	Args     []string `json:"args"`
	BeadsDir *string  `json:"beads_dir"`
}

func (r record) line() string { return r.Name + " " + strings.Join(r.Args, " ") }

type respRule struct {
	Match string `json:"match"`
	// Nth, when non-zero, restricts the rule to the Nth exec (counting this
	// one) whose "<binary> <args...>" text contains Match, so a bead can read
	// one way before a write and another way after it.
	Nth    int    `json:"nth,omitempty"`
	Exit   int    `json:"exit"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	helperMain()
}

func helperChildArgs() []string {
	for i, a := range os.Args {
		if a == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

func helperMain() {
	argv := helperChildArgs()
	rec := record{Name: argv[0], Args: argv[1:]}
	if v, ok := os.LookupEnv("PG_CONNECTOR_ISSUE_BEADS_DIR"); ok {
		rec.BeadsDir = &v
	}
	seq := 1
	var prevLines []record
	if logPath := os.Getenv("GO_HELPER_LOG"); logPath != "" {
		if prev, err := os.ReadFile(logPath); err == nil {
			seq = strings.Count(string(prev), "\n") + 1
			for _, l := range strings.Split(strings.TrimSpace(string(prev)), "\n") {
				var pr record
				if json.Unmarshal([]byte(l), &pr) == nil {
					prevLines = append(prevLines, pr)
				}
			}
		}
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			b, _ := json.Marshal(rec)
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	var rules []respRule
	if p := os.Getenv("GO_HELPER_SCRIPT"); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &rules)
		}
	}
	line := rec.line()
	for _, r := range rules {
		if strings.Contains(line, r.Match) {
			if r.Nth > 0 {
				n := 1
				for _, pr := range prevLines {
					if strings.Contains(pr.line(), r.Match) {
						n++
					}
				}
				if n != r.Nth {
					continue
				}
			}
			_, _ = os.Stdout.WriteString(strings.ReplaceAll(r.Stdout, "{seq}", strconv.Itoa(seq)))
			_, _ = os.Stderr.WriteString(r.Stderr)
			os.Exit(r.Exit)
		}
	}
	if strings.HasPrefix(line, "pg-connector issue create") {
		_, _ = os.Stdout.WriteString(`{"result":{"id":"wb-` + strconv.Itoa(seq) + `"}}`)
		return
	}
	if strings.HasPrefix(line, "pg-connector issue list") {
		_, _ = os.Stdout.WriteString(`{"entities":[],"present_ids":[],"sources":[]}`)
		return
	}
	_, _ = os.Stdout.WriteString(`{"result":{}}`)
}

type double struct {
	t      *testing.T
	log    string
	script string
}

// newDouble returns a double answering with rules (first match wins). It also
// removes any ambient PG_CONNECTOR_ISSUE_BEADS_DIR for the test's duration so
// the absence assertions are meaningful.
func newDouble(t *testing.T, rules ...respRule) *double {
	t.Helper()
	if v, ok := os.LookupEnv("PG_CONNECTOR_ISSUE_BEADS_DIR"); ok {
		t.Cleanup(func() { _ = os.Setenv("PG_CONNECTOR_ISSUE_BEADS_DIR", v) })
		_ = os.Unsetenv("PG_CONNECTOR_ISSUE_BEADS_DIR")
	}
	dir := t.TempDir()
	d := &double{t: t, log: filepath.Join(dir, "calls.jsonl"), script: filepath.Join(dir, "script.json")}
	b, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.script, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d *double) factory() CmdFactory {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GO_HELPER_LOG="+d.log, "GO_HELPER_SCRIPT="+d.script)
		return cmd
	}
}

func (d *double) calls() []record {
	d.t.Helper()
	f, err := os.Open(d.log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		d.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			d.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// lines is the joined "<binary> <args>" text of every exec, in order.
func (d *double) lines() []string {
	var out []string
	for _, r := range d.calls() {
		out = append(out, r.line())
	}
	return out
}

func testEnv(d *double, cfg *config.Config) Env {
	if cfg == nil {
		cfg = &config.Config{}
	}
	fixed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	return Env{Command: d.factory(), Config: cfg, Clock: func() time.Time { return fixed }}
}
