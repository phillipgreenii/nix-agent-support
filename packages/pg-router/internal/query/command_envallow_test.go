package query

import (
	"context"
	"strings"
	"testing"
)

// env_allow puts a command query's process on the minimal base environment plus
// the listed names: a variable that is not listed is not passed. RunRaw's extra
// variables are set by the router itself and bypass the list for those names only.
func TestRunRaw_envAllowPassesOnlyListedNames(t *testing.T) {
	t.Setenv("PGR_LISTED", "yes")
	t.Setenv("PGR_UNLISTED", "no")
	q := CommandQuery{Argv: []string{"/usr/bin/env"}, Format: FormatJSONL, EnvAllow: []string{"PGR_LISTED"}}
	out, err := RunRaw(context.Background(), Env{}, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "PGR_LISTED=yes") {
		t.Errorf("the listed variable was not passed:\n%s", got)
	}
	if strings.Contains(got, "PGR_UNLISTED") {
		t.Errorf("a variable that is not in env_allow was passed:\n%s", got)
	}

	out, err = RunRaw(context.Background(), Env{}, q, []string{"PG_ROUTER_LEVEL_REFRESH=1"})
	if err != nil {
		t.Fatal(err)
	}
	got = string(out)
	if !strings.Contains(got, "PG_ROUTER_LEVEL_REFRESH=1") || !strings.Contains(got, "PGR_LISTED=yes") || strings.Contains(got, "PGR_UNLISTED") {
		t.Errorf("extra variable must be added without widening the allowlist:\n%s", got)
	}
}

// Without env_allow the process still starts from the minimal base env: a
// variable present only in the router's env does not reach it, and an extra
// variable is added to the base env.
func TestRunRaw_noAllowlistDropsAmbientEnvironment(t *testing.T) {
	t.Setenv("PGR_AMBIENT", "yes")
	q := &CommandQuery{Argv: []string{"/usr/bin/env"}, Format: FormatJSONL}
	out, err := RunRaw(context.Background(), Env{}, q, []string{"PG_ROUTER_LEVEL_REFRESH=1"})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if strings.Contains(got, "PGR_AMBIENT") || !strings.Contains(got, "PG_ROUTER_LEVEL_REFRESH=1") {
		t.Errorf("env = \n%s", got)
	}
}

func TestRunRaw_rejectsNonCommandQuery(t *testing.T) {
	if _, err := RunRaw(context.Background(), Env{}, TimerQuery{}, nil); err == nil || !strings.Contains(err.Error(), "command query") {
		t.Fatalf("err = %v", err)
	}
}
