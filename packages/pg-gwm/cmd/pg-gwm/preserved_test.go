package main

import (
	"strings"
	"testing"
)

func TestPreservedCommands(t *testing.T) {
	c := newCLI(t)
	head := c.git("rev-parse", "HEAD")
	orphan := c.git("commit-tree", "HEAD^{tree}", "-p", head, "-m", "wip")
	const (
		old    = "pg-gwm/dirty-reaped-20200101T000000Z-old-x"
		merged = "pg-gwm/dirty-reaped-20200101T000000Z-merged-x"
	)
	c.git("branch", old, orphan)
	c.git("branch", merged, head)

	code, out, errOut := c.run("preserved", "list", "--json")
	if code != 0 {
		t.Fatalf("list exit %d: %s", code, errOut)
	}
	m := decode(t, out)
	list, _ := m["preserved"].([]any)
	if m["schema_version"] != float64(1) || len(list) != 2 {
		t.Fatalf("list = %s", out)
	}
	e := list[0].(map[string]any)
	if e["branch"] != merged || e["name"] != nil || e["lease_id"] != nil || e["at"] != "2020-01-01T00:00:00Z" {
		t.Errorf("entry = %v", e)
	}

	code, out, _ = c.run("preserved", "prune", "--dry-run", "--json")
	m = decode(t, out)
	if code != 0 || m["dry_run"] != true || len(m["deleted"].([]any)) != 1 || m["deleted"].([]any)[0] != merged {
		t.Errorf("dry run = %d %s", code, out)
	}
	code, out, _ = c.run("preserved", "prune", "--older-than", "1d", "--json")
	m = decode(t, out)
	if code != 0 || m["dry_run"] != false || len(m["deleted"].([]any)) != 2 {
		t.Errorf("prune = %d %s", code, out)
	}
	if left := c.git("branch", "--list", "pg-gwm/*"); left != "" {
		t.Errorf("branches left: %s", left)
	}

	code, out, _ = c.run("preserved", "prune", "--json")
	m = decode(t, out)
	if d, ok := m["deleted"].([]any); code != 0 || !ok || len(d) != 0 {
		t.Errorf("empty prune = %d %s (deleted must be an array)", code, out)
	}

	c.tty = true
	if code, out, _ = c.run("preserved", "list"); code != 0 || !strings.Contains(out, "BRANCH") {
		t.Errorf("text list = %d %q", code, out)
	}
	if code, _, _ = c.run("preserved"); code != 2 {
		t.Errorf("no subcommand exit %d, want 2", code)
	}
	if code, _, _ = c.run("preserved", "prune", "--older-than", "soon"); code != 2 {
		t.Errorf("bad --older-than exit %d, want 2", code)
	}
}
