package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenewReleaseCLI(t *testing.T) {
	c := newCLI(t)
	code, out, e := c.run("lease", "--name", "demo", "-b", "demo", "--ttl", "30m")
	if code != 0 {
		t.Fatalf("lease: %d %s", code, e)
	}
	first := decode(t, out)
	code, out, e = c.run("renew", "--name", "demo", "--ttl", "1h", "--metadata", "k=v")
	m := decode(t, out)
	if code != 0 || m["lease_id"] != first["lease_id"] || m["lease_ttl"] != "1h" || m["metadata"].(map[string]any)["k"] != "v" || m["schema_version"] != float64(1) {
		t.Fatalf("renew: %d %s %s", code, out, e)
	}
	if _, out, _ = c.run("renew", first["lease_id"].(string), "--print=path"); strings.TrimSpace(out) != first["worktree_path"] {
		t.Errorf("--print=path = %q", out)
	}
	code, out, _ = c.run("expire", "--name", "demo")
	if m := decode(t, out); code != 0 || m["status"] != "lease-expired" {
		t.Errorf("expire: %d %s", code, out)
	}
	code, out, _ = c.run("release", "--name", "demo")
	if m := decode(t, out); code != 0 || m["status"] != "released" {
		t.Errorf("release: %d %s", code, out)
	}
	code, out, _ = c.run("renew", "--name", "demo")
	if code != 24 || decode(t, out)["error"].(map[string]any)["name"] != "lease-released" {
		t.Errorf("renew after release: %d %s", code, out)
	}
	if code, _, _ := c.run("renew", "--name", "demo", "--lease-id", "x"); code != 2 {
		t.Errorf("unknown flag exit %d", code)
	}
	if code, _, _ := c.run("renew", "l-aaaaaaaaaa", "--name", "demo"); code != 2 {
		t.Errorf("id and name exit %d", code)
	}
}

func TestOrRenewCLI(t *testing.T) {
	c := newCLI(t)
	_, out, _ := c.run("lease", "--name", "w", "-b", "wb")
	first := decode(t, out)
	code, out, errs := c.run("lease", "--name", "w", "--or-renew", "-b", "other")
	m := decode(t, out)
	if code != 0 || m["renewed"] != true || m["lease_id"] != first["lease_id"] || m["worktree_path"] != first["worktree_path"] {
		t.Fatalf("or-renew: %d %s", code, out)
	}
	if !strings.Contains(errs, "warning:") || !strings.Contains(errs, "wb") {
		t.Errorf("stderr = %q", errs)
	}
	_, out, _ = c.run("lease", "--name", "fresh", "--or-renew", "--detach")
	if m := decode(t, out); m["renewed"] != false {
		t.Errorf("new lease: %s", out)
	}
}

func TestInfoAndListCLI(t *testing.T) {
	c := newCLI(t)
	_, out, _ := c.run("lease", "--name", "one", "-b", "one", "--owner", "me")
	one := decode(t, out)
	_, out, _ = c.run("lease", "--name", "two", "--detach")
	two := decode(t, out)

	for _, args := range [][]string{
		{"info", one["worktree_path"].(string)},
		{"info", one["id"].(string)},
		{"info", one["lease_id"].(string)},
		{"info", "one"},
		{"info", "--name", "one"},
		{"info", "--id", one["id"].(string)},
	} {
		code, out, e := c.run(args...)
		if m := decode(t, out); code != 0 || m["lease_id"] != one["lease_id"] || m["managed"] != true {
			t.Errorf("%v: %d %s %s", args, code, out, e)
		}
	}
	if _, out, _ = c.run("info", "one", "--print=path"); strings.TrimSpace(out) != one["worktree_path"] {
		t.Errorf("--print=path = %q", out)
	}

	code, out, errs := c.run("info", c.repo.Dir)
	m := decode(t, out)
	if code != 0 || m["managed"] != false || m["status"] != "unmanaged" || !strings.Contains(errs, "warning:") {
		t.Errorf("main checkout: %d %s %s", code, out, errs)
	}
	if code := c.a.run([]string{"info", "--repo", t.TempDir(), "x"}); code != 2 {
		t.Errorf("outside a repository: exit %d", code)
	}

	code, out, _ = c.run("list")
	var recs []map[string]any
	if err := json.Unmarshal([]byte(out), &recs); err != nil || code != 0 || len(recs) != 2 {
		t.Fatalf("list json: %d %s", code, out)
	}
	if recs[0]["lease_id"] != one["lease_id"] || recs[1]["lease_id"] != two["lease_id"] {
		t.Errorf("order = %v", recs)
	}
	_, out, _ = c.run("list", "--include-unmanaged")
	if err := json.Unmarshal([]byte(out), &recs); err != nil || len(recs) != 3 || recs[2]["managed"] != false {
		t.Errorf("include-unmanaged: %s", out)
	}
	c.tty = true
	_, out, _ = c.run("list")
	for _, want := range []string{"ID", "STATUS", "one", "two", "leased", filepath.Base(one["worktree_path"].(string))} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 2 {
		t.Errorf("want a header and two rows:\n%s", out)
	}
}
