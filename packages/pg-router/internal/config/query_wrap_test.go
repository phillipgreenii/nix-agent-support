package config

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/query"
)

// queryWrapCfg renders a config with one command query carrying extra
// [[query]]-level lines (strict_base_env, ...) and one role.
func queryWrapCfg(extra string) string {
	return `
[[query]]
name = "probe"
emits = ["e"]
type = "command"
` + extra + `
[query.command]
argv = ["/bin/true"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.command]
argv = ["x"]
`
}

func TestLoad_queryStrictBaseEnvDecode(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, queryWrapCfg(`strict_base_env = true`))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	q, ok := c.Queries[0].Query.(query.CommandQuery)
	if !ok {
		t.Fatalf("query is %T, want query.CommandQuery", c.Queries[0].Query)
	}
	if !q.StrictBaseEnv {
		t.Fatal("StrictBaseEnv = false, want true")
	}
}

func TestLoad_queryStrictBaseEnvDefaultOff(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, queryWrapCfg(``))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	q := c.Queries[0].Query.(query.CommandQuery)
	if q.StrictBaseEnv {
		t.Fatal("StrictBaseEnv = true by default, want false")
	}
}

// A [[query]] that still carries the removed wrap key fails to load, names the
// query, and points at pg-wrap run, whatever shape the old value had.
func TestLoad_queryWrapKeyRemovedPointsAtPgWrapRun(t *testing.T) {
	for _, extra := range []string{
		`wrap = ["/usr/bin/env WRAPPED=1", ["/usr/bin/nice", "-n", "5"]]`,
		`wrap = []`,
		`wrap = "/usr/bin/env"`,
	} {
		t.Run(extra, func(t *testing.T) {
			absentGlobalConfig(t)
			writeCfg(t, queryWrapCfg(extra))
			_, err := Load()
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			if !strings.Contains(err.Error(), `"probe"`) {
				t.Errorf("error does not name the query: %v", err)
			}
			if !strings.Contains(err.Error(), "pg-wrap run") {
				t.Errorf("error %q lacks the pointer to pg-wrap run", err)
			}
		})
	}
}

// Every other rejected shape fails config load and names the query.
func TestLoad_queryKeyRejections(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  string
	}{
		{"misspelled wraps", `wraps = ["/usr/bin/env X=1"]`, "wraps"},
		{"misspelled strictBaseEnv", `strictBaseEnv = true`, "strictBaseEnv"},
		{"strict_base_env not a bool", `strict_base_env = "yes"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			absentGlobalConfig(t)
			writeCfg(t, queryWrapCfg(tc.extra))
			_, err := Load()
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			if tc.name != "strict_base_env not a bool" && !strings.Contains(err.Error(), `"probe"`) {
				t.Errorf("error does not name the query: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q lacks %q", err, tc.want)
			}
		})
	}
}

// A strict_base_env on a timer query (no command) is a load error that
// names the query.
func TestLoad_queryWrapOnTimerQueryRejected(t *testing.T) {
	for _, extra := range []string{`strict_base_env = true`, `strict_base_env = false`} {
		t.Run(extra, func(t *testing.T) {
			absentGlobalConfig(t)
			writeCfg(t, `
[[query]]
name = "tick"
emits = ["e"]
type = "timer"
`+extra+`

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.command]
argv = ["x"]
`)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), `"tick"`) || !strings.Contains(err.Error(), "command") {
				t.Fatalf("Load error = %v, want one naming query \"tick\" and the missing command", err)
			}
		})
	}
}

// Existing [[query]] keys keep loading: strictness covers only keys queryTOML
// does not consume.
func TestLoad_queryKnownKeysStillLoad(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "probe"
emits = ["e"]
type = "command"
description = "d"
non_blocking_gates = ["LOW_DISK_USAGE"]
expected_interval = "5m"
[query.trigger]
kind = "period"
every = "1m"
[query.failure_backoff]
retries = 1
[query.command]
argv = ["/bin/true"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.command]
argv = ["x"]
`)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}
