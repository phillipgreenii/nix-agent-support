package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeUserConfig installs body as the user configuration file the live
// commands read.
func writeUserConfig(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pg-gwm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestListExceptionsAndCheck(t *testing.T) {
	c := newCLI(t)
	writeUserConfig(t, "[repos.\""+c.repo.Dir+"\"]\n"+
		"[[repos.\""+c.repo.Dir+"\".exceptions]]\n"+
		"path = \"/tmp/forest/**\"\nreason = \"workforest sets\"\nowner = \"example-owner\"\nreview-by = \"2999-01-01\"\n")

	code, out, e := c.run("list", "--exceptions", "--format=text")
	if code != 0 {
		t.Fatalf("list --exceptions: %d %s", code, e)
	}
	for _, want := range []string{"PATH", "/tmp/forest/**", "workforest sets", "example-owner", "2999-01-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output lacks %q: %s", want, out)
		}
	}
	code, out, _ = c.run("list", "--exceptions", "--json")
	var rows []map[string]string
	if err := json.Unmarshal([]byte(out), &rows); err != nil || code != 0 || len(rows) != 1 ||
		rows[0]["path"] != "/tmp/forest/**" || rows[0]["reason"] != "workforest sets" ||
		rows[0]["owner"] != "example-owner" || rows[0]["review_by"] != "2999-01-01" {
		t.Errorf("json: %d %v %s", code, err, out)
	}

	code, out, _ = c.runRaw("config", "check")
	if code != 0 || strings.Contains(out, "error") || strings.Contains(out, "warning: "+c.repo.Dir+": exceptions") {
		t.Errorf("valid exception: exit %d, out %q", code, out)
	}
}

func TestConfigCheckExceptionFindings(t *testing.T) {
	c := newCLI(t)
	tbl := func(fields string) string {
		return writeConfig(t, "[[repos.\"/x\".exceptions]]\npath = \"/a/*\"\n"+fields)
	}
	cases := []struct {
		name, fields, want string
		code               int
	}{
		{"missing reason", "owner = \"o\"\nreview-by = \"2999-01-01\"\n", "reason is required", 2},
		{"missing owner", "reason = \"r\"\nreview-by = \"2999-01-01\"\n", "owner is required", 2},
		{"missing review-by", "reason = \"r\"\nowner = \"o\"\n", "review-by is required", 2},
		{"bad date", "reason = \"r\"\nowner = \"o\"\nreview-by = \"soon\"\n", "YYYY-MM-DD", 2},
		{"expired", "reason = \"r\"\nowner = \"o\"\nreview-by = \"2000-01-01\"\n", "warning: /x", 0},
		{"fine", "reason = \"r\"\nowner = \"o\"\nreview-by = \"2999-01-01\"\n", "ok:", 0},
	}
	for _, tc := range cases {
		code, out, _ := c.runRaw("config", "check", "--config", tbl(tc.fields))
		if code != tc.code || !strings.Contains(out, tc.want) {
			t.Errorf("%s: exit %d, out %q", tc.name, code, out)
		}
	}
	if code, out, _ := c.runRaw("config", "check", "--config", tbl("reason = \"r\"\nowner = \"o\"\nreview-by = \"2000-01-01\"\n")); code != 0 || !strings.Contains(out, "has passed") {
		t.Errorf("expired is a warning, not an error: %d %q", code, out)
	}
}
